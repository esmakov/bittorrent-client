package torrent

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/esmakov/bittorrent-client/hash"
	"github.com/esmakov/bittorrent-client/parser"
)

type Torrent struct {
	metaInfoFileName string
	trackerHostname  string
	comment          string
	trackerId        string
	infoHash         []byte
	isPrivate        bool
	pieceHashes      map[int]string
	files            int
	totalSize        int
	downloaded       int
	uploaded         int
	left             int
	seeders          int
	leechers         int
	sync.Mutex
}

func New(metaInfoFileName string, metaInfoMap map[string]any, infoHash []byte, pieceHashes map[int]string) *Torrent {
	// Fields common to single-file and multi-file torrents
	trackerHostname := metaInfoMap["announce"].(string)
	infoMap := metaInfoMap["info"].(map[string]any)

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
		trackerHostname:  trackerHostname,
		infoHash:         infoHash,
		metaInfoFileName: metaInfoFileName,
		comment:          comment,
		files:            fileNum,
		pieceHashes:      pieceHashes,
		isPrivate:        isPrivate == 1,
		totalSize:        totalSize,
		left:             totalSize,
	}
}

func (t *Torrent) String() string {
	str := fmt.Sprint(
		fmt.Sprintln("-------------Torrent Info---------------"),
		fmt.Sprintln("File path:", t.metaInfoFileName),
		fmt.Sprintln("Tracker:", t.trackerHostname),
		fmt.Sprintf("Hash: % x\n", t.infoHash),
		fmt.Sprintln("Total size: ", t.totalSize),
		fmt.Sprintln("# of files: ", t.files))

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
	const MAX_PEERS = 1

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

	numPeers := 0

	// TODO: Maintain N active peers
	for numPeers < MAX_PEERS {
		peer := peerList[rand.Intn(len(peerList))]
		go establishPeerConnection(t.infoHash, myPeerIdBytes, peer, errors, done)
		numPeers++
	}

	for {
		select {
		case e := <-errors:
			numPeers--
			log.Println(e)
		case <-done:
			fmt.Println("Completed")
		}
	}
	// TODO: Run in background
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
	queryParams.Set("uploaded", strconv.Itoa(t.uploaded))
	queryParams.Set("downloaded", strconv.Itoa(t.downloaded))
	queryParams.Set("left", strconv.Itoa(t.left))
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

func establishPeerConnection(expectedInfoHash, myPeerIdBytes []byte, peerAddr string, errors chan<- error, done chan<- struct{}) {
	const BLOCK_SIZE = 16 * 1024
	const MAX_RESPONSE_SIZE = BLOCK_SIZE + 13 // Piece message header

	peerHost, _, err := net.SplitHostPort(peerAddr)
	if err != nil {
		errors <- err
		return
	}

	fmt.Println("Connecting to peer", peerHost, "...")

	conn, err := net.DialTimeout("tcp", peerAddr, 1*time.Second)
	if err != nil {
		errors <- err
		return
	}
	defer conn.Close()

	fmt.Println("Connected to peer", peerHost)

	handshakeMsg := createHandshakeMsg(expectedInfoHash, myPeerIdBytes)

	if _, err := conn.Write(handshakeMsg); err != nil {
		errors <- err
		return
	}

	msgBuf := make([]byte, 32*1024)
	msgDataEndIdx := 0
	connState := newConnState()

	for {
		// fmt.Println("Buffer before reading [first 100]")
		// for i := 0; i < 100; i++ {
		// 	fmt.Printf("% x", msgBuf[i])
		// }

		tempBuf := make([]byte, MAX_RESPONSE_SIZE)
		numRead, err := conn.Read(tempBuf)
		if err != nil {
			errors <- err
			return
		}

		fmt.Printf("Read %v bytes from %v, parsing...\n", numRead, peerHost)
		msgDataEndIdx += numRead

		// TODO: Replace
		if binary.BigEndian.Uint64(msgBuf) == 0 {
			// fmt.Println("Didn't already contain fragment(s)")
			copy(msgBuf, tempBuf)
		} else {
			fmt.Println("Appended to earlier fragment")
			for i := 0; i < numRead; i++ {
				msgBuf[msgDataEndIdx+i] = tempBuf[i]
			}
		}

		msgList, err := parseMultiMessage(msgBuf[:msgDataEndIdx], expectedInfoHash)
		if err != nil {
			e := fmt.Errorf("Error when talking to %v:\n %w", peerHost, err)
			errors <- e
			return
		}

		for _, msg := range msgList {
			switch msg.kind {
			case handshake:
				fmt.Printf("%v has peer id %v\n", peerHost, string(msg.peerId))
			case choke:
				connState.peer_choking = true
			case unchoke:
				connState.peer_choking = false
			case interested:
				connState.peer_interested = true
			case uninterested:
				connState.peer_interested = false
			case bitfield:
				unchokeMsg := createUnchokeMessage()
				fmt.Printf("Sending unchoke to %v\n", peerHost)
				if _, err := conn.Write(unchokeMsg); err != nil {
					errors <- err
					return
				}

				chosenPieceIdx := uint32(randomNextPieceIdx(msg.bitfield))
				offset := uint32(0)
				length := uint32(BLOCK_SIZE)
				reqMsg := createRequestMessage(chosenPieceIdx, offset, length)

				fmt.Printf("Sending request for piece %v with block offset %v to %v\n", chosenPieceIdx, offset, peerHost)
				if _, err := conn.Write(reqMsg); err != nil {
					errors <- err
					return
				}
			case piece:
				fmt.Println("Block data (truncated):", msg.blockData[:100])
				// Update my bitfield
				// Save block
				// Update offset and request another block (update block selection to use our bitfield)
			}
		}

		startOfFirstFragment := 0
		for _, msg := range msgList {
			if msg.kind != fragment {
				startOfFirstFragment += msg.totalLen
			}
		}

		fmt.Printf("Clearing first %v bytes\n", startOfFirstFragment)
		for i := 0; i < startOfFirstFragment; i++ {
			msgBuf[i] = 0
		}

		if startOfFirstFragment == msgDataEndIdx {
			// fmt.Println("No fragments, cleared entire buf")
			msgDataEndIdx = 0
		} else if startOfFirstFragment != 0 {
			// Copy data to start to eliminate leading 0s
			dataLen := msgDataEndIdx - startOfFirstFragment
			fmt.Printf("Copying over first %v bytes\n", startOfFirstFragment)
			// for i := 0; i < dataLen; i++ {
			// 	msgBuf[i] = msgBuf[startOfFirstFragment+i]
			// }
			copy(msgBuf, msgBuf[startOfFirstFragment:])
			msgDataEndIdx = dataLen
		}

		fmt.Println()
	}
}

func createUnchokeMessage() []byte {
	return []byte{0x00, 0x00, 0x00, 0x01, 0x01}
}

func createRequestMessage(idx, offset, length uint32) []byte {
	lengthAndID := []byte{
		0x00,
		0x00,
		0x00,
		0x0D, // length without prefix = 13
		0x06, // message ID
	}
	bytes := make([]byte, 4+13)
	copy(bytes, lengthAndID)

	payload := []uint32{idx, offset, length}
	for i, v := range payload {
		index := i*4 + 5
		binary.BigEndian.PutUint32(bytes[index:index+4], v)
	}

	return bytes
}

// TODO: Cross-reference against the bitfield representing pieces we already have
func randomNextPieceIdx(bitfield []byte) int {
	for {
		randByteIdx := rand.Intn(len(bitfield))
		randByte := bitfield[randByteIdx]
		if randByte == 0 {
			// No completed pieces here
			continue
		}
		// From right to left, find first bit == 1
		for bitIdx := 0; bitIdx < 8; bitIdx++ {
			mask := byte(1 << bitIdx)
			if randByte&mask == 1 {
				// Get idx starting from left-hand side of bitfield
				return randByteIdx + (7 - bitIdx)
			}
		}
	}
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

type pieceData struct {
	data                  []byte
	hashIdxInMetaInfoFile int
}

func (p pieceData) isValidPiece(pieceHashes map[int]string) (bool, error) {
	if len(p.data) == 0 {
		return false, errors.New("Not enough data was supplied")
	}
	givenHash, err := hash.HashSHA1(p.data)
	if err != nil {
		return false, err
	}
	return string(givenHash) == pieceHashes[p.hashIdxInMetaInfoFile], nil
}

// Sometimes multiple messages will be read from the stream at once
func parseMultiMessage(buf, expectedInfoHash []byte) ([]peerMessage, error) {
	var msgList []peerMessage
	for i := 0; i < len(buf)-1; {
		// Chop off one message at a time
		msg, err := parseMessage(buf[i:], expectedInfoHash)
		if err != nil {
			return nil, err
		}
		msgList = append(msgList, msg)
		i += msg.totalLen
	}
	return msgList, nil
}

func parseMessage(buf, expectedInfoHash []byte) (msg peerMessage, e error) {
	handshakeStart := []byte{0x13, 0x42, 0x69, 0x74, 0x54}
	bufStart := buf[:len(handshakeStart)]
	if slices.Equal(bufStart, handshakeStart) {
		msg, e = parseHandshake(buf, expectedInfoHash)
		fmt.Println("Peer sent", msg.kind)
		return
	}

	if len(buf) == 4 {
		msg.kind = keepalive
		msg.totalLen = 4
		fmt.Println("Peer sent", msg.kind)
		return
	}

	lenBytes := binary.BigEndian.Uint32(buf[:4])
	lenVal := int(lenBytes)

	if lenVal > len(buf) {
		// Message needs to be reassembled from this packet and the next
		msg.kind = fragment
		msg.totalLen = len(buf)
		fmt.Printf("Message is %v bytes long but we only have %v so far\n", lenVal, len(buf))
		fmt.Println("Peer sent", msg.kind)
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

	fmt.Println("Peer sent", msg.kind)
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
	t.uploaded = n
}

func (t *Torrent) LoadUploaded() int {
	t.Lock()
	defer t.Unlock()
	return t.uploaded
}

func (t *Torrent) StoreDownloaded(n int) {
	t.Lock()
	defer t.Unlock()
	t.downloaded = n
}

func (t *Torrent) StoreLeft(n int) {
	t.Lock()
	defer t.Unlock()
	t.left = n
}
