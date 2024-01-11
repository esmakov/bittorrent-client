package torrent

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/esmakov/bittorrent-client/hash"
	"github.com/esmakov/bittorrent-client/parser"
)

type pieceData struct {
	data *[]byte
	idx  int
}

func (p pieceData) update(msg peerMessage) {
	block := (*p.data)[msg.blockOffset : msg.blockOffset+BLOCK_SIZE]
	copy(block, msg.blockData)
}

func newPieceData(pieceLength, idx int) *pieceData {
	data := make([]byte, pieceLength)
	return &pieceData{data: &data, idx: idx}
}

func (p pieceData) isValidPiece(pieceHashes []string) (bool, error) {
	if len(*p.data) == 0 {
		return false, errors.New("Not enough data was supplied")
	}
	givenHash, err := hash.HashSHA1(*p.data)
	if err != nil {
		return false, err
	}
	return string(givenHash) == pieceHashes[p.idx], nil
}

func (p pieceData) isValidPiece2(piecesStr string) (bool, error) {
	if len(*p.data) == 0 {
		return false, errors.New("Not enough data was supplied")
	}
	givenHash, err := hash.HashSHA1(*p.data)
	if err != nil {
		return false, err
	}
	return string(givenHash) == piecesStr[p.idx*20:p.idx*20+20], nil
}

type Torrent struct {
	metaInfoFileName string
	trackerHostname  string
	trackerId        string
	comment          string
	infoHash         []byte
	isPrivate        bool
	piecesStr        string
	pieceLength      int
	numFiles         int
	totalSize        int
	seeders          int
	leechers         int
	piecesDownloaded int
	piecesUploaded   int
	piecesLeft       int
	bitfield         []byte
	// TODO: state field (paused, seeding, etc), maybe related to tracker "event"
	sync.Mutex
}

func splitPieceHashes(concatPieceHashes string) []string {
	numPieces := len(concatPieceHashes) / 20
	s := make([]string, numPieces)

	j := 0
	for i := 0; i < numPieces; i++ {
		hash := concatPieceHashes[j : j+20]
		s[i] = hash
		j += 20
	}
	return s
}

func New(metaInfoFileName string, metaInfoMap map[string]any, infoHash []byte) *Torrent {
	// Fields common to single-file and multi-file torrents
	trackerHostname := metaInfoMap["announce"].(string)
	infoMap := metaInfoMap["info"].(map[string]any)

	pieceLength := infoMap["piece length"].(int)
	piecesStr := infoMap["pieces"].(string)

	numPieces := len(piecesStr) / 20
	bitmaskLen := int(math.Ceil(float64(numPieces) / 8.0))
	bitmask := make([]byte, bitmaskLen)

	// Optional common fields
	comment := ""
	if commentEntry, ok := metaInfoMap["comment"]; ok {
		comment = commentEntry.(string)
	}

	isPrivate := 0
	if isPrivateEntry, ok := infoMap["private"]; ok {
		isPrivate = isPrivateEntry.(int)
	}

	fileMode := ""
	files := infoMap["files"]
	if files != nil {
		fileMode = "multiple"
	} else {
		fileMode = "single"
	}

	totalSize := 0
	fileNum := 1

	if fileMode == "multiple" {
		files := files.([]any)
		fileNum = len(files)

		for _, v := range files {
			fileDict := v.(map[string]any)

			length := fileDict["length"].(int)
			totalSize += length

			pl := fileDict["path"].([]any)
			var pathList []string
			for _, v := range pl {
				pathList = append(pathList, v.(string))
			}
			// TODO: Concatenate all files together and then extract piece hashes
			// fmt.Println(strings.Join(pathList, "/"), length, "bytes")
		}
	} else if fileMode == "single" {
		name := infoMap["name"].(string)
		_ = name
		length := infoMap["length"].(int)
		totalSize += length
	}

	return &Torrent{
		metaInfoFileName: metaInfoFileName,
		trackerHostname:  trackerHostname,
		infoHash:         infoHash,
		comment:          comment,
		isPrivate:        isPrivate == 1,
		piecesStr:        piecesStr,
		pieceLength:      pieceLength,
		numFiles:         fileNum,
		totalSize:        totalSize,
		piecesLeft:       totalSize,
		bitfield:         bitmask,
	}
}

func (t *Torrent) String() string {
	str := fmt.Sprint(
		fmt.Sprintln("-------------Torrent Info---------------"),
		fmt.Sprintln("File path:", t.metaInfoFileName),
		fmt.Sprintln("Tracker:", t.trackerHostname),
		fmt.Sprintf("Hash: % x\n", t.infoHash),
		fmt.Sprintln("Total size: ", t.totalSize),
		fmt.Sprintln("# of files: ", t.numFiles),
		// fmt.Sprintf("Bitmask: %08b\n", t.bitmask),
	)

	if t.comment != "" {
		str += fmt.Sprint("Comment:", t.comment)
	}
	str += "\n"
	return str
}

/*
One goroutine has to listen for incoming connections on the port we announced to tracker

Another goroutine has to keep talking to the tracker

While # of connections <= MAX_PEERS, go through list of peers and attempt to establish a conn
Spawn a goroutine for each connection that has to:
As piece messages come in, store data block in memory
As pieces complete:
Check hash and die if incorrect, also blacklist peer
Signal main goroutine to update torrent state
Save piece to disk

Want to write each piece to disk as it completes, but:
- pieces may be out of order
- pieces may cross file boundaries
- can't store all pieces in memory at same time
One strategy:
- preallocate enough disk space for entire torrent
- as each piece comes in, do buffered writes to its offset in the file

Either each goroutine needs (synchronized) access to the torrent state (which is simpler), or
There needs to be a main goroutine sending each "connection handler" goroutine updates about
which pieces are downloaded
*/

func (t *Torrent) Start() error {
	myPeerId, myPeerIdBytes := getPeerId()
	_ = myPeerIdBytes
	portForTrackerResponse := getNextFreePort()
	event := "started" // TODO: Enum

	trackerResponse, err := sendTrackerMessage(t, myPeerId, portForTrackerResponse, event)
	if err != nil {
		return err
	}

	if warning, ok := trackerResponse["warning message"]; ok {
		log.Println("TRACKER WARNING:", warning.(string))
	}

	waitInterval := trackerResponse["interval"].(int)
	_ = waitInterval

	// Optional tracker fields
	if trackerId, ok := trackerResponse["tracker id"]; ok {
		t.trackerId = trackerId.(string)
	}

	if numSeeders, ok := trackerResponse["complete"]; ok {
		t.StoreSeeders(numSeeders.(int))
	}

	if numLeechers, ok := trackerResponse["incomplete"]; ok {
		t.StoreLeechers(numLeechers.(int))
	}

	peersStr := trackerResponse["peers"].(string)
	peerList, err := extractCompactPeers(peersStr)
	if err != nil {
		return err
	}

	if len(peerList) == 0 {
		// TODO: Keep intermittently checking for peers in a separate goroutine
	}

	errors := make(chan error, 5)
	done := make(chan struct{}, 5)

	// TODO: Maintain N active peers
	const MAX_PEERS = 1
	numPeers := 0
	for numPeers < MAX_PEERS {
		peer := getNextPeer(peerList)
		go handleConnection(t, myPeerIdBytes, peer, errors, done)
		numPeers++
	}

	for {
		select {
		case e := <-errors:
			log.Println("Error caught in start:", e)
			numPeers--

			for x := 0; x < MAX_PEERS-numPeers; x++ {
				peer := getNextPeer(peerList)
				go handleConnection(t, myPeerIdBytes, peer, errors, done)
			}
		case <-done:
			fmt.Println("Completed in start")
			numPeers--
		}
	}
	// TODO: Run in background
}

// TODO: Choose more intelligently
func getNextPeer(peerList []string) string {
	return peerList[rand.Intn(len(peerList))]
}

const BLOCK_SIZE = 16 * 1024

func handleConnection(t *Torrent, myPeerIdBytes []byte, peerAddr string, errors chan error, done chan struct{}) {
	parsedMessages := make(chan peerMessage, 5)
	outboundMsgs := make(chan []byte, 5)
	go connectAndParse(t.infoHash, myPeerIdBytes, peerAddr, parsedMessages, outboundMsgs, errors, done)

	connState := newConnState()

	currPieceIdx := 0
	byteOffset := 0

	numPieces := len(t.piecesStr) / 20
	bitmaskLen := int(math.Ceil(float64(numPieces) / 8.0))

	peerBitfield := make([]byte, bitmaskLen)

	maxByteOffset := (numPieces - 1) * BLOCK_SIZE

	for {
		select {
		case msg := <-parsedMessages:
			switch msg.kind {
			case handshake:
				fmt.Printf("%v has peer id %v\n", peerAddr, string(msg.peerId))
			case have:
			case request:
			case keepalive:
			case choke:
				connState.peer_choking = true
			case unchoke:
				connState.peer_choking = false
			case interested:
				connState.peer_interested = true
			case uninterested:
				connState.peer_interested = false
			case bitfield:
				peerBitfield = msg.bitfield

				unchokeMsg := createUnchokeMsg()
				outboundMsgs <- unchokeMsg
				fmt.Printf("Sending unchoke to %v\n", peerAddr)

				// Start in a random location
				var err error
				currPieceIdx, err = randomNextPieceIdx(peerBitfield)
				if err != nil {
					errors <- err
					// TODO: Signal connectAndParse to close connection
					return
				}

				reqMsg := createRequestMsg(currPieceIdx, byteOffset, BLOCK_SIZE)
				outboundMsgs <- reqMsg
				fmt.Printf("Sent request for piece %v with block offset %v to %v\n", currPieceIdx, byteOffset, peerAddr)
			case piece:
				// fmt.Println(msg.blockData)
				// Store block

				if byteOffset < maxByteOffset { // Incomplete piece
					byteOffset += BLOCK_SIZE
				} else {
					updateBitfield(&t.bitfield, msg)
					currPieceIdx++
					byteOffset = 0
					// nextSequentialPieceIdx(&currPieceIdx)
					// fmt.Printf("%08b\n", t.bitmask)
				}

				reqMsg := createRequestMsg(currPieceIdx, byteOffset, BLOCK_SIZE)
				outboundMsgs <- reqMsg
				fmt.Printf("Sent request for piece %v with block offset %v to %v\n", currPieceIdx, byteOffset, peerAddr)
			}
		case e := <-errors:
			log.Println("Error caught in handler:", e)
			return
		case <-done:
			fmt.Println("Completed in handler")
			return
		}
	}
}

func updateBitfield(bitfield *[]byte, msg peerMessage) {
	pieceByteIdx := msg.pieceIdx / 8
	bitsFromRight := 7 - (msg.pieceIdx % 8)
	pieceByte := &(*bitfield)[pieceByteIdx]
	b := uint8(0x01) << bitsFromRight
	*pieceByte |= b
}

func connectAndParse(infoHash, myPeerIdBytes []byte, peerAddr string, parsedMessages chan<- peerMessage, outboundMsgs <-chan []byte, errs chan<- error, done chan<- struct{}) {
	const MAX_RESPONSE_SIZE = BLOCK_SIZE + 13 // Piece message header

	fmt.Println("Connecting to peer", peerAddr, "...")

	conn, err := net.DialTimeout("tcp", peerAddr, 1*time.Second)
	if err != nil {
		errs <- err
		return
	}
	defer conn.Close()

	fmt.Println("Connected to peer", peerAddr)

	handshakeMsg := createHandshakeMsg(infoHash, myPeerIdBytes)
	if _, err := conn.Write(handshakeMsg); err != nil {
		errs <- err
		return
	}

	msgBuf := make([]byte, 32*1024)
	msgBufEndIdx := 0

	for {
		select {
		case msg := <-outboundMsgs:
			// fmt.Println("About to write", msg)
			if _, err := conn.Write(msg); err != nil {
				errs <- err
				return
			}
		default:
			// fmt.Println("Nothing to write")
		}

		tempBuf := make([]byte, MAX_RESPONSE_SIZE)

		conn.SetReadDeadline(time.Now().Add(time.Millisecond * 500))

		numRead, err := conn.Read(tempBuf)
		if errors.Is(err, os.ErrDeadlineExceeded) {
			// fmt.Println("Nothing to read")
			continue
		} else if err != nil {
			errs <- err
			return
		}
		fmt.Printf("Read %v bytes from %v, parsing...\n", numRead, peerAddr)

		// TODO: More versatile check
		if binary.BigEndian.Uint64(msgBuf) == 0 {
			// fmt.Println("Didn't already contain fragment(s)")
			copy(msgBuf, tempBuf)
		} else {
			fmt.Println("Appending to earlier fragment")
			for i := 0; i < numRead; i++ {
				msgBuf[msgBufEndIdx+i] = tempBuf[i]
			}
		}

		msgBufEndIdx += numRead

		copyToParse := slices.Clone(msgBuf[:msgBufEndIdx]) // Passing a slice of msgBuf to the msg structs causes them to be cleared as well
		msgList, err := parseMultiMessage(copyToParse, infoHash)
		if err != nil {
			errs <- err
			return
		}

		firstFragmentIdx := 0
		for _, msg := range msgList {
			fmt.Println("Parsed", msg.kind)
			parsedMessages <- msg

			if msg.kind != fragment {
				firstFragmentIdx += msg.totalLen
			}
		}

		for i := 0; i < firstFragmentIdx; i++ {
			msgBuf[i] = 0
		}

		if firstFragmentIdx == msgBufEndIdx {
			msgBufEndIdx = 0
		} else if firstFragmentIdx != 0 {
			// Copy over leading 0s
			dataLen := msgBufEndIdx - firstFragmentIdx
			copy(msgBuf, msgBuf[firstFragmentIdx:])
			msgBufEndIdx = dataLen
		}

		fmt.Println()
	}
}

func getPeerId() (string, []byte) {
	peerId := "edededededededededed"
	peerIdBytes := make([]byte, len(peerId))
	copy(peerIdBytes, peerId)
	return peerId, peerIdBytes
}

func getNextFreePort() string {
	return "6881"
}

// <pstrlen><pstr><reserved><info_hash><peer_id>
func createHandshakeMsg(infoHash []byte, peerId []byte) []byte {
	protocol := "BitTorrent protocol"
	protocolBytes := make([]byte, len(protocol))
	copy(protocolBytes, protocol)

	length := []byte{byte(len(protocol))}
	reserved := []byte{0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}

	return concatMultipleSlices([][]byte{length, protocolBytes, reserved, infoHash, peerId})
}

// Credit: https://freshman.tech/snippets/go/concatenate-slices/#concatenating-multiple-slices-at-once
func concatMultipleSlices[T any](slices [][]T) []T {
	var totalLen int

	for _, s := range slices {
		totalLen += len(s)
	}

	result := make([]T, totalLen)

	var i int

	for _, s := range slices {
		i += copy(result[i:], s)
	}

	return result
}

func sendTrackerMessage(t *Torrent, peerId, portForTrackerResponse, event string) (map[string]any, error) {
	reqURL := url.URL{
		Opaque: t.trackerHostname,
	}
	queryParams := url.Values{}
	queryParams.Set("peer_id", peerId)
	queryParams.Set("port", portForTrackerResponse)
	queryParams.Set("uploaded", strconv.Itoa(t.piecesUploaded))
	queryParams.Set("downloaded", strconv.Itoa(t.piecesDownloaded))
	queryParams.Set("left", strconv.Itoa(t.piecesLeft))
	queryParams.Set("event", event)
	queryParams.Set("compact", "1")
	if t.trackerId != "" {
		queryParams.Set("trackerid", t.trackerId)
	}
	reqURL.RawQuery = "info_hash=" + hash.CustomURLEscape(t.infoHash) + "&" + queryParams.Encode()

	req, err := http.NewRequest("GET", reqURL.String(), nil)
	if err != nil {
		return nil, err
	}

	req.Close = true
	req.Header.Set("Connection", "close")

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	p := parser.New(false)
	response, err := p.ParseResponse(bufio.NewReader(bytes.NewReader(body)))
	if err != nil {
		return nil, err
	}

	if failureReason, ok := response["failure reason"]; ok {
		return nil, errors.New("TRACKER FAILURE REASON: " + failureReason.(string))
	}

	return response, nil
}

func extractCompactPeers(s string) ([]string, error) {
	var list []string

	if len(s)%6 != 0 {
		return nil, errors.New("Peer information must be a multiple of 6 bytes")
	}

	for i := 0; i < len(s)-2; {
		ip := ""
		for j := 0; j < 4; j++ {
			ip += fmt.Sprintf("%d", s[i])
			if j != 3 {
				ip += "."
			}
			i++
		}
		ip += ":"

		var portVal uint16
		portBytes := []byte(s[i : i+2])
		portVal = binary.BigEndian.Uint16(portBytes)
		ip += strconv.Itoa(int(portVal))

		// Skip 2 bytes for the port
		i += 2

		list = append(list, ip)
	}

	return list, nil
}

func createUnchokeMsg() []byte {
	return []byte{0x00, 0x00, 0x00, 0x01, 0x01}
}

func createRequestMsg(pieceIdx, offset, length int) []byte {
	lengthAndID := []byte{
		0x00,
		0x00,
		0x00,
		0x0D, // length without prefix = 13
		0x06, // message ID
	}
	bytes := make([]byte, 4+13)
	copy(bytes, lengthAndID)

	payload := []uint32{uint32(pieceIdx), uint32(offset), uint32(length)}
	for i, v := range payload {
		index := i*4 + 5
		binary.BigEndian.PutUint32(bytes[index:index+4], v)
	}

	return bytes
}

// TODO: Cross-reference against the bitfield representing pieces we already have
func randomNextPieceIdx(bitfield []byte) (int, error) {
	attempts := 0
	for attempts < len(bitfield) {
		randByteIdx := rand.Intn(len(bitfield))
		randByte := bitfield[randByteIdx]
		if randByte == 0 {
			// No completed pieces here
			attempts++
			// fmt.Println("Didn't find completed pieces this byte, continuing after attempt", attempts)
			continue
		}
		// From right to left, find first bit == 1
		for bitIdx := 0; bitIdx < 8; bitIdx++ {
			mask := byte(1 << bitIdx)
			if randByte&mask == 1 {
				// Get idx starting from left-hand side of bitfield
				return randByteIdx + (7 - bitIdx), nil
			}
		}
	}
	return 0, errors.New("Peer has no pieces we want")
}

type connState struct {
	am_choking      bool
	am_interested   bool
	peer_choking    bool
	peer_interested bool
}

func newConnState() *connState {
	return &connState{
		true,
		false,
		true,
		false,
	}
}

type messageKinds int

const (
	choke messageKinds = iota
	unchoke
	interested
	uninterested
	have
	bitfield
	request
	piece
	cancel
	port
	keepalive
	handshake
	fragment
)

func (m messageKinds) String() string {
	switch m {
	case choke:
		return "choke"
	case unchoke:
		return "unchoke"
	case interested:
		return "interested"
	case uninterested:
		return "uninterested"
	case have:
		return "have"
	case bitfield:
		return "bitfield"
	case request:
		return "request"
	case piece:
		return "piece"
	case cancel:
		return "cancel"
	case port:
		return "port"
	case keepalive:
		return "keepalive"
	case handshake:
		return "handshake"
	default:
		return "unknown or fragment"
	}
}

type peerMessage struct {
	kind        messageKinds
	totalLen    int
	peerId      []byte
	pieceIdx    int
	bitfield    []byte
	blockOffset int
	blockLen    int
	blockData   []byte
}

// Sometimes multiple messages will be read from the stream at once
func parseMultiMessage(buf, infoHash []byte) ([]peerMessage, error) {
	var msgList []peerMessage
	for i := 0; i < len(buf)-1; {
		// Chop off one message at a time
		msg, err := parseMessage(buf[i:], infoHash)
		if err != nil {
			return nil, err
		}
		msgList = append(msgList, msg)
		i += msg.totalLen
	}
	return msgList, nil
}

func parseMessage(buf, infoHash []byte) (msg peerMessage, e error) {
	handshakeStart := []byte{0x13, 0x42, 0x69, 0x74, 0x54}
	bufStart := buf[:len(handshakeStart)]
	if slices.Equal(bufStart, handshakeStart) {
		msg, e = parseHandshake(buf, infoHash)
		return
	}

	if len(buf) == 4 {
		msg.kind = keepalive
		msg.totalLen = 4
		return
	}

	lenBytes := binary.BigEndian.Uint32(buf[:4])
	lenVal := int(lenBytes)

	if lenVal > len(buf) || len(buf) < 4 {
		// Message needs to be reassembled from this packet and the next
		msg.kind = fragment
		msg.totalLen = len(buf)
		fmt.Printf("Message is %v bytes long but we only have %v so far\n", lenVal, len(buf))
		return
	}

	messageKind := int(buf[4])
	msg.kind = messageKinds(messageKind)

	if msg.kind == have || msg.kind == request || msg.kind == piece || msg.kind == cancel {
		msg.pieceIdx = int(binary.BigEndian.Uint32(buf[5:9]))
	}

	msg.totalLen = lenVal + 4 // Include length prefix

	if msg.totalLen > len(buf) {
		panic(fmt.Sprintf("Out of bounds of msg buffer: %v / %v", msg.totalLen, len(buf)))
	}

	switch msg.kind {
	case bitfield:
		msg.bitfield = buf[5:msg.totalLen]
	case request:
		msg.blockOffset = int(binary.BigEndian.Uint32(buf[9:13]))
		msg.blockLen = int(binary.BigEndian.Uint32(buf[13:msg.totalLen]))
	case piece:
		msg.blockOffset = int(binary.BigEndian.Uint32(buf[9:13]))
		msg.blockData = buf[13:msg.totalLen]
	}

	return
}

func parseHandshake(buf []byte, expectedInfoHash []byte) (peerMessage, error) {
	msg := peerMessage{}
	protocolLen := int(buf[0]) // Should be 19 or 0x13

	// protocolBytes := buf[1 : protocolLen+1]
	// reservedBytes := buf[protocolLen+1 : protocolLen+9]

	theirInfoHash := buf[protocolLen+9 : protocolLen+29]
	if !slices.Equal(theirInfoHash, expectedInfoHash) {
		return msg, errors.New("Peer did not respond with correct info hash")
	}

	peerId := buf[protocolLen+29 : protocolLen+49]

	msg.kind = handshake
	msg.totalLen = protocolLen + 49
	msg.peerId = peerId
	return msg, nil
}

func (t *Torrent) StoreSeeders(n int) {
	t.Lock()
	defer t.Unlock()
	t.seeders = n
}

func (t *Torrent) StoreLeechers(n int) {
	t.Lock()
	defer t.Unlock()
	t.leechers = n
}

func (t *Torrent) StoreUploaded(n int) {
	t.Lock()
	defer t.Unlock()
	t.piecesUploaded = n
}

func (t *Torrent) StoreDownloaded(n int) {
	t.Lock()
	defer t.Unlock()
	t.piecesDownloaded = n
}

func (t *Torrent) StoreLeft(n int) {
	t.Lock()
	defer t.Unlock()
	t.piecesLeft = n
}
