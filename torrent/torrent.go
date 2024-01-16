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
	"strings"
	"sync"
	"time"

	"github.com/esmakov/bittorrent-client/hash"
	"github.com/esmakov/bittorrent-client/parser"
)

type Torrent struct {
	metaInfoFileName string
	trackerHostname  string
	trackerId        string
	comment          string
	infoHash         []byte
	isPrivate        bool
	piecesStr        string
	pieceLen         int
	numPieces        int
	numFiles         int
	totalSize        int
	seeders          int
	leechers         int
	piecesDownloaded int
	piecesUploaded   int
	bitfield         []byte
	fileDescs        []*os.File
	// TODO: state field (paused, seeding, etc), maybe related to tracker "event"
	sync.Mutex
}

func New(metaInfoFileName string, metaInfoMap map[string]any, infoHash []byte) (*Torrent, error) {
	// Fields common to single-file and multi-file torrents
	trackerHostname := metaInfoMap["announce"].(string)
	infoMap := metaInfoMap["info"].(map[string]any)

	pieceLen := infoMap["piece length"].(int)
	piecesStr := infoMap["pieces"].(string)
	numPieces := len(piecesStr) / 20

	bitfieldLen := int(math.Ceil(float64(numPieces) / 8.0))
	bitfield := make([]byte, bitfieldLen)

	// Optional common fields
	comment := ""
	if commentEntry, ok := metaInfoMap["comment"]; ok {
		comment = commentEntry.(string)
	}

	isPrivate := 0
	if isPrivateEntry, ok := infoMap["private"]; ok {
		isPrivate = isPrivateEntry.(int)
	}

	fileStructure := ""
	files := infoMap["files"]
	if files != nil {
		fileStructure = "multiple"
	} else {
		fileStructure = "single"
	}

	totalSize := 0
	fileNum := 1

	var filePaths []string

	if fileStructure == "multiple" {
		files := files.([]any)
		fileNum = len(files)

		dirName := infoMap["name"].(string)
		_ = dirName

		for _, v := range files {
			fileMap := v.(map[string]any)

			fileLength := fileMap["length"].(int)
			totalSize += fileLength

			pl := fileMap["path"].([]any)
			var pathList []string
			for _, v := range pl {
				pathList = append(pathList, v.(string))
			}

			// TODO: Concatenate all files together and then split the result into pieces
			completePath := strings.Join(pathList, "/")
			filePaths = append(filePaths, completePath)
		}
	} else if fileStructure == "single" {
		fileName := infoMap["name"].(string)
		filePaths = append(filePaths, fileName)
		totalSize = infoMap["length"].(int)
	}

	// TODO: Make sure this doesn't overwrite data if the file already exists
	// TODO: Use os.DirEntry?
	// TOOD: If file(s) already exist(s), check its piece hashes and populate t.piecesDownloaded and t.bitfield

	var fileDescs []*os.File
	for _, fileName := range filePaths {
		fd, err := os.Create(fileName)
		if err != nil {
			return nil, err
		}
		fileDescs = append(fileDescs, fd)
	}

	return &Torrent{
		metaInfoFileName: metaInfoFileName,
		trackerHostname:  trackerHostname,
		infoHash:         infoHash,
		comment:          comment,
		isPrivate:        isPrivate == 1,
		piecesStr:        piecesStr,
		pieceLen:         pieceLen,
		numPieces:        totalSize / pieceLen,
		numFiles:         fileNum,
		totalSize:        totalSize,
		bitfield:         bitfield,
		fileDescs:        fileDescs,
	}, nil
}

func (t *Torrent) IsInProgress() bool {
	return t.piecesDownloaded > 0 && t.piecesDownloaded < t.numPieces
}

func (t *Torrent) String() string {
	str := fmt.Sprint(
		fmt.Sprintln("-------------Torrent Info---------------"),
		fmt.Sprintln("File path:", t.metaInfoFileName),
		fmt.Sprintln("Tracker:", t.trackerHostname),
		fmt.Sprintln("Total size: ", t.totalSize),
		fmt.Sprintln("# of files: ", t.numFiles),
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

Want to write each piece to disk as it completes, but:
- pieces may be out of order
- pieces may cross file boundaries
- can't store all pieces in memory at same time
One strategy:
- preallocate enough disk space for entire torrent
- as each piece comes in, do buffered writes to its offset in the file
*/

const MAX_PEERS = 1

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

	signalErrors := make(chan error, MAX_PEERS)
	signalDone := make(chan struct{}, MAX_PEERS)

	// TODO: Maintain N active peers
	numPeers := 0
	for numPeers < MAX_PEERS {
		// TODO: Choose more intelligently
		peer := getRandPeer(peerList)
		go handleConnection(t, myPeerIdBytes, peer, signalErrors, signalDone)
		numPeers++
	}

	for {
		select {
		case e := <-signalErrors:
			log.Println("Error caught in start:", e)
			numPeers--

			peer := getRandPeer(peerList)
			go handleConnection(t, myPeerIdBytes, peer, signalErrors, signalDone)
			numPeers++
		case <-signalDone:
			fmt.Println("Completed", t.metaInfoFileName)
			numPeers--
			return nil
		}
	}
	// TODO: Run in background
}

func getRandPeer(peerList []string) string {
	return peerList[rand.Intn(len(peerList))]
}

const BLOCK_SIZE = 16 * 1024

type pieceData struct {
	data []byte
	idx  int
}

func (p *pieceData) updateWithCopy(msg peerMessage) {
	block := p.data[msg.blockOffset : msg.blockOffset+BLOCK_SIZE]
	// TODO: Don't copy each block twice
	copy(block, msg.blockData)
}

func NewPieceData(pieceLength int) *pieceData {
	// Pre-allocate enough to store an entire piece worth of blocks sequentially
	data := make([]byte, pieceLength)
	return &pieceData{data: data}
}

func (t *Torrent) checkHash(p pieceData) (bool, error) {
	givenHash, err := hash.HashSHA1(p.data)
	if err != nil {
		return false, err
	}
	return string(givenHash) == t.piecesStr[p.idx*20:p.idx*20+20], nil
}

func handleConnection(t *Torrent, myPeerIdBytes []byte, peerAddr string, signalErrors chan error, signalDone chan struct{}) {
	parsedMessages := make(chan peerMessage, MAX_PEERS)
	outboundMsgs := make(chan []byte, MAX_PEERS)
	defer close(outboundMsgs)

	go connectAndParse(t.infoHash, myPeerIdBytes, peerAddr, parsedMessages, outboundMsgs, signalErrors)

	connState := newConnState()

	bitfieldLen := int(math.Ceil(float64(t.numPieces) / 8.0))
	peerBitfield := make([]byte, bitfieldLen)

	numBlocks := t.pieceLen / BLOCK_SIZE
	maxByteOffset := (numBlocks - 1) * BLOCK_SIZE

	byteOffset := 0
	p := NewPieceData(t.pieceLen)

	for {
		select {
		case msg := <-parsedMessages:
			if msg.kind != fragment {
				fmt.Println("Parsed a", msg.kind, "from", peerAddr)
			}

			switch msg.kind {
			case handshake:
				fmt.Printf("%v has peer id %v\n", peerAddr, string(msg.peerId))
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
				p.idx, err = randAvailablePieceIdx(t.numPieces, peerBitfield, t.bitfield)
				if err != nil {
					signalErrors <- err
					return
				}

				reqMsg := createRequestMsg(p.idx, byteOffset, BLOCK_SIZE)
				outboundMsgs <- reqMsg
				fmt.Printf("Sent request for piece %v with block offset %v to %v\n", p.idx, byteOffset, peerAddr)
			case piece:
				p.updateWithCopy(msg)

				if byteOffset < maxByteOffset { // Incomplete piece
					byteOffset += BLOCK_SIZE
					fmt.Println(byteOffset, "/", t.pieceLen, "bytes")
				} else {
					fmt.Println("Completed piece", p.idx, ", checking...")
					correct, err := t.checkHash(*p)
					if err != nil {
						signalErrors <- err
						return
					}

					if !correct {
						// TODO: Add peer to blacklist
						err = fmt.Errorf("Piece %v from %v failed hash check", p.idx, peerAddr)
						signalErrors <- err
						return
					}

					t.piecesDownloaded++
					updateBitfield(&t.bitfield, msg.pieceIdx)

					fmt.Printf("%b\n", t.bitfield)

					fd := t.getFdForPiece(p)
					off := int64(p.idx * t.pieceLen)

					_, err = fd.WriteAt(p.data, off)
					if err != nil {
						signalErrors <- err
						return
					}

					testPiece := make([]byte, t.pieceLen)
					_, err = fd.ReadAt(testPiece, off)
					if err != nil {
						signalErrors <- err
						return
					}

					fileInfo, err := os.Stat(fd.Name())
					fmt.Println("Wrote", testPiece[:10], "to offset", off, "- file is now", fileInfo.Size(), "bytes")

					byteOffset = 0
					clear(p.data) // In case we pause in the middle of a piece and want to resume later
					p.idx, err = nextAvailablePieceIdx(p.idx, t.numPieces, peerBitfield, t.bitfield)
					if err != nil {
						signalErrors <- err
						return
					}
				}

				reqMsg := createRequestMsg(p.idx, byteOffset, BLOCK_SIZE)
				outboundMsgs <- reqMsg
				fmt.Printf("Sent request for piece %v with block offset %v to %v\n", p.idx, byteOffset, peerAddr)
			case have:
				updateBitfield(&peerBitfield, msg.pieceIdx)
			}
		}
	}
}

// TODO: Handle multi-file torrents
func (t *Torrent) getFdForPiece(p *pieceData) *os.File {
	return t.fileDescs[0]
}

func updateBitfield(bitfield *[]byte, pieceIdx int) {
	pieceByteIdx := pieceIdx / 8
	bitsFromRight := 7 - (pieceIdx % 8)
	pieceByte := &((*bitfield)[pieceByteIdx])
	b := uint8(0x01) << bitsFromRight
	*pieceByte |= b
}

func connectAndParse(infoHash, myPeerIdBytes []byte, peerAddr string, parsedMessages chan<- peerMessage, outboundMsgs <-chan []byte, signalErrors chan<- error) {
	const MAX_RESPONSE_SIZE = BLOCK_SIZE + 13 // Include piece message header

	// fmt.Println("Connecting to peer", peerAddr, "...")

	conn, err := net.DialTimeout("tcp", peerAddr, 1*time.Second)
	if err != nil {
		signalErrors <- err
		return
	}
	defer conn.Close()

	fmt.Println("Connected to peer", peerAddr)

	handshakeMsg := createHandshakeMsg(infoHash, myPeerIdBytes)
	if _, err := conn.Write(handshakeMsg); err != nil {
		signalErrors <- err
		return
	}

	msgBuf := make([]byte, 32*1024)
	msgBufEndIdx := 0

	for {
		select {
		case msg, more := <-outboundMsgs:
			if !more {
				log.Println("Handler signaled to drop connection to", peerAddr)
				return
			}

			if _, err := conn.Write(msg); err != nil {
				signalErrors <- err
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
			signalErrors <- err
			return
		}
		// fmt.Printf("Read %v bytes from %v, parsing...\n", numRead, peerAddr)

		// TODO: More versatile check
		if binary.BigEndian.Uint64(msgBuf) == 0 {
			// fmt.Println("Didn't already contain fragment(s)")
			copy(msgBuf, tempBuf)
		} else {
			// fmt.Println("Appending to earlier fragment")
			newSection := msgBuf[msgBufEndIdx : msgBufEndIdx+numRead]
			copy(newSection, tempBuf)
		}

		msgBufEndIdx += numRead

		copyToParse := slices.Clone(msgBuf[:msgBufEndIdx]) // Passing a slice of msgBuf to the msg structs causes them to be cleared as well
		msgList, err := parseMultiMessage(copyToParse, infoHash)
		if err != nil {
			signalErrors <- err
			return
		}

		firstFragmentIdx := 0
		for _, msg := range msgList {
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
	queryParams.Set("left", strconv.Itoa(t.numPieces-t.piecesDownloaded))
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

func randAvailablePieceIdx(numPieces int, peerBitfield, ourBitfield []byte) (int, error) {
	attempts := 0
	for attempts < numPieces {
		randByteIdx := rand.Intn(len(peerBitfield))
		byteToCheck := peerBitfield[randByteIdx]
		for bitsFromRight := 7; bitsFromRight >= 0; bitsFromRight-- {
			mask := byte(1 << bitsFromRight)
			if byteToCheck&mask != 0 && ourBitfield[randByteIdx]&mask == 0 {
				return randByteIdx*8 + (7 - bitsFromRight), nil
			}
			attempts++
		}
	}
	return 0, errors.New("Peer has no pieces we want")
}

func nextAvailablePieceIdx(currBitIdx, numPieces int, peerBitfield, ourBitfield []byte) (int, error) {
	bitToCheckIdx := currBitIdx + 1
	byteToCheckIdx := bitToCheckIdx / 8
	byteToCheck := peerBitfield[byteToCheckIdx]
	bitsFromLeft := 7 - bitToCheckIdx%8

	attempts := 0
	for attempts < numPieces {
		for bitsFromRight := bitsFromLeft; bitsFromRight >= 0; bitsFromRight-- {
			mask := byte(1 << bitsFromRight)
			if byteToCheck&mask == mask && ourBitfield[byteToCheckIdx]&mask != mask {
				return byteToCheckIdx*8 + (7 - bitsFromRight), nil
			}
			attempts++
		}

		byteToCheckIdx++
		if byteToCheckIdx == len(peerBitfield) {
			byteToCheckIdx = 0
		}
		byteToCheck = peerBitfield[byteToCheckIdx]
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
		msg, e = parseAndVerifyHandshake(buf, infoHash)
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
		// fmt.Printf("Message is %v bytes long but we only have %v so far\n", lenVal, len(buf))
		msg.kind = fragment
		msg.totalLen = len(buf)
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

func parseAndVerifyHandshake(buf []byte, expectedInfoHash []byte) (peerMessage, error) {
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
