package torrent

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"io/fs"
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
	pieceSize        int
	numPieces        int
	totalSize        int
	seeders          int
	leechers         int
	piecesDownloaded int
	piecesUploaded   int
	bitfield         []byte
	files            []*fileInfo
	// TODO: state field (paused, seeding, etc), maybe related to tracker "event"
	sync.Mutex
}

type fileInfo struct {
	fd        *os.File
	path      string
	finalSize int64
}

func New(metaInfoFileName string, metaInfoMap map[string]any, infoHash []byte) (*Torrent, error) {
	// Fields common to single-file and multi-file torrents
	trackerHostname := metaInfoMap["announce"].(string)
	infoMap := metaInfoMap["info"].(map[string]any)

	pieceSize := infoMap["piece length"].(int)
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
	filesList := infoMap["files"]
	if filesList != nil {
		fileStructure = "multiple"
	} else {
		fileStructure = "single"
	}

	totalSize := 0

	var files []*fileInfo
	var dirName string

	if fileStructure == "multiple" {
		fl := filesList.([]any)

		dirName = infoMap["name"].(string)

		for _, e := range fl {
			fileMap := e.(map[string]any)

			fileLength := fileMap["length"].(int)
			totalSize += fileLength

			pathList := fileMap["path"].([]any)

			var pathSegments []string
			for _, f := range pathList {
				pathSegments = append(pathSegments, f.(string))
			}
			completePath := strings.Join(pathSegments, string(os.PathSeparator))

			files = append(files, &fileInfo{
				path:      completePath,
				finalSize: int64(fileLength),
			})
		}
	} else if fileStructure == "single" {
		f := infoMap["name"].(string)
		totalSize = infoMap["length"].(int)
		files = append(files, &fileInfo{
			path:      f,
			finalSize: int64(totalSize),
		})
	}

	piecesDownloaded := 0
	var filesToCheck []*fileInfo
	if dirName != "" {
		os.Mkdir(dirName, 0766)
	}
	for _, f := range files {
		if dirName != "" {
			f.path = dirName + string(os.PathSeparator) + f.path
		}

		fd, err := os.OpenFile(f.path, os.O_RDWR, 0)
		if errors.Is(err, fs.ErrNotExist) {
			fd, err = os.Create(f.path)
			if err != nil {
				return nil, err
			}
			f.fd = fd
		} else if err != nil {
			return nil, err
		} else {
			f.fd = fd
			filesToCheck = append(filesToCheck, f)
		}
	}

	existingPieces, err := checkPiecesOnDisk(filesToCheck, pieceSize, piecesStr)
	if err != nil {
		return nil, err
	}

	for _, v := range existingPieces {
		updateBitfield(&bitfield, v)
		piecesDownloaded++
	}

	fmt.Println("Aready have", existingPieces)

	return &Torrent{
		metaInfoFileName: metaInfoFileName,
		trackerHostname:  trackerHostname,
		infoHash:         infoHash,
		comment:          comment,
		isPrivate:        isPrivate == 1,
		piecesStr:        piecesStr,
		pieceSize:        pieceSize,
		numPieces:        numPieces,
		piecesDownloaded: piecesDownloaded,
		totalSize:        totalSize,
		bitfield:         bitfield,
		files:            files,
	}, nil
}

func checkPiecesOnDisk(files []*fileInfo, pieceSize int, piecesStr string) ([]int, error) {
	p := NewPieceData(pieceSize)
	var pieceNums []int
	offset := int64(0) // 0-based index into the "stream" of pieces
	fileEndIdx := int64(0)

	for _, f := range files {
		fileEndIdx += f.finalSize

		fi, err := os.Stat(f.path)
		if err != nil {
			return nil, err
		}

		for {
			if fi.Size() < f.finalSize {
				if offset >= fi.Size() {
					break
				}
			} else {
				if offset >= fileEndIdx {
					break
				}
			}

			bytesFromEnd := fileEndIdx - offset

			readSize := math.MaxInt
			limits := []int{pieceSize, int(f.finalSize), int(bytesFromEnd)}
			for _, v := range limits {
				if v < readSize {
					readSize = v
				}
			}

			fmt.Println(readSize)

			if readSize < pieceSize {
				// FIX: Restore full capacity, check pieces that cross file boundaries
				p.data = p.data[:readSize]
			}

			_, err := f.fd.ReadAt(p.data, offset)
			if err != nil {
				return nil, err
			}

			pieceEmpty := true
			// TODO: Cache this
			for i := 0; i < len(p.data); i++ {
				if p.data[i] != 0 {
					pieceEmpty = false
					break
				}
			}

			if !pieceEmpty {
				correct, err := checkPieceHash(*p, piecesStr, readSize)
				if err != nil {
					return nil, err
				}

				if correct {
					pieceNums = append(pieceNums, p.num)
				} else {
					return nil, errors.New(fmt.Sprintf("Piece %v failed hash check\n", p.num))
				}
			}

			clear(p.data) // Necessary?
			p.num++
			offset += int64(pieceSize)
		}
	}
	return pieceNums, nil
}

func (t *Torrent) IsInProgress() bool {
	return t.piecesDownloaded > 0 && t.piecesDownloaded < t.numPieces
}

func (t *Torrent) String() string {
	sb := strings.Builder{}
	str := fmt.Sprint(
		fmt.Sprintln("-------------Torrent Info---------------"),
		fmt.Sprintln("Torrent file:", t.metaInfoFileName),
		fmt.Sprintln("Tracker:", t.trackerHostname),
		fmt.Sprintln("Total size: ", t.totalSize),
		fmt.Sprintf("%v file(s): \n", len(t.files)),
	)

	sb.WriteString(str)
	for _, file := range t.files {
		sb.WriteString(fmt.Sprintf("  %v\n", file.fd.Name()))
	}

	if t.comment != "" {
		sb.WriteString(fmt.Sprint("Comment:", t.comment))
	}
	return sb.String()
}

const MAX_PEERS = 1

func (t *Torrent) Start() error {
	myPeerId := getPeerId()
	portForTrackerResponse := getNextFreePort()

	trackerResponse, err := sendTrackerMessage(t, myPeerId, portForTrackerResponse, "started")
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
		t.storeSeeders(numSeeders.(int))
	}

	if numLeechers, ok := trackerResponse["incomplete"]; ok {
		t.storeLeechers(numLeechers.(int))
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

	// TODO:
	// Maintain N active peers
	// One goroutine has to listen for incoming connections on the port we announced to tracker
	// Another goroutine has to keep talking to the tracker
	// Send peers have msg when a piece is downloaded

	numPeers := 0
	for numPeers < MAX_PEERS {
		// TODO: Choose more intelligently
		peer := getRandPeer(peerList)
		go handleConnection(t, []byte(myPeerId), peer, signalErrors, signalDone)
		numPeers++
	}

	for {
		select {
		case e := <-signalErrors:
			log.Println("Error caught in start:", e)
			peer := getRandPeer(peerList)
			go handleConnection(t, []byte(myPeerId), peer, signalErrors, signalDone)
		case <-signalDone:
			fmt.Println("Completed", t.metaInfoFileName)
			sendTrackerMessage(t, myPeerId, portForTrackerResponse, "completed")
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
	num  int
	data []byte
}

func NewPieceData(pieceLen int) *pieceData {
	data := make([]byte, pieceLen)
	return &pieceData{data: data}
}

func (p *pieceData) updateWithCopy(msg peerMessage, blockSize int) {
	block := p.data[msg.blockOffset : msg.blockOffset+blockSize]
	// TODO: Don't copy each block twice
	copy(block, msg.blockData)
}

func checkPieceHash(p pieceData, piecesStr string, pieceSize int) (bool, error) {
	givenHash, err := hash.HashSHA1(p.data[:pieceSize])
	if err != nil {
		return false, err
	}
	return string(givenHash) == piecesStr[p.num*20:p.num*20+20], nil
}

func handleConnection(t *Torrent, myPeerId []byte, peerAddr string, signalErrors chan error, signalDone chan struct{}) {
	parsedMessages := make(chan peerMessage, MAX_PEERS)
	outboundMsgs := make(chan []byte, MAX_PEERS)
	defer close(outboundMsgs)

	go connectAndParse(t.infoHash, myPeerId, peerAddr, parsedMessages, outboundMsgs, signalErrors)

	connState := newConnState()

	bfLen := int(math.Ceil(float64(t.numPieces) / 8.0))
	peerBitfield := make([]byte, bfLen)

	blockOffset := 0
	numBlocks := t.pieceSize / BLOCK_SIZE
	lastBlockOffset := (numBlocks - 1) * BLOCK_SIZE

	p := NewPieceData(t.pieceSize)
	lastPieceNum := t.numPieces - 1 // 0-based

	numBytesToRequest := BLOCK_SIZE
	currPieceSize := t.pieceSize

	for {
		select {
		case msg := <-parsedMessages:
			if msg.kind != fragment {
				fmt.Printf("%v sent a %v\n", peerAddr, msg.kind)
			}

			switch msg.kind {
			case handshake:
				// fmt.Printf("%v has peer id %v\n", peerAddr, string(msg.peerId))
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
				// if connState.peer_choking {
				// 	continue
				// }
				peerBitfield = msg.bitfield

				unchokeMsg := createUnchokeMsg()
				outboundMsgs <- unchokeMsg
				fmt.Printf("Sending unchoke to %v\n", peerAddr)

				// Start wherever for now
				// var err error
				// p.num, err = randAvailablePieceIdx(t.numPieces, peerBitfield, t.bitfield)
				// if err != nil {
				// 	signalErrors <- err
				// 	return
				// }

				p.num = lastPieceNum
				reqMsg := createRequestMsg(p.num, blockOffset, BLOCK_SIZE)
				outboundMsgs <- reqMsg
				fmt.Printf("Requesting piece %v/%v from %v\n", p.num, lastPieceNum, peerAddr)
			case piece:
				p.updateWithCopy(msg, numBytesToRequest)

				if p.num == lastPieceNum {
					currPieceSize = t.totalSize - p.num*t.pieceSize
					numBlocks = int(math.Ceil(float64(currPieceSize) / float64(BLOCK_SIZE)))
					lastBlockOffset = (numBlocks - 1) * BLOCK_SIZE
				}

				if blockOffset < lastBlockOffset { // Incomplete piece
					blockOffset += BLOCK_SIZE // Account for block just downloaded
					remaining := currPieceSize - blockOffset
					if remaining < BLOCK_SIZE {
						numBytesToRequest = remaining
					}
					fmt.Println(blockOffset, "/", currPieceSize, "bytes")
				} else {
					fmt.Printf("Completed piece %v, checking...\n", p.num)
					correct, err := checkPieceHash(*p, t.piecesStr, currPieceSize)
					if err != nil {
						signalErrors <- err
						return
					}

					// TODO: Add peer to blacklist
					if !correct {
						err = fmt.Errorf("Piece %v from %v failed hash check", p.num, peerAddr)
						signalErrors <- err
						return
					}

					err = t.savePiece(p, currPieceSize)
					if err != nil {
						signalErrors <- err
						return
					}

					t.storeDownloaded(t.piecesDownloaded + 1)
					updateBitfield(&t.bitfield, msg.pieceNum)
					fmt.Printf("%b\n", t.bitfield)

					if t.piecesDownloaded == t.numPieces {
						signalDone <- struct{}{}
						return
					}

					blockOffset = 0
					currPieceSize = t.pieceSize
					numBlocks = t.pieceSize / BLOCK_SIZE
					lastBlockOffset = (numBlocks - 1) * BLOCK_SIZE
					numBytesToRequest = BLOCK_SIZE
					clear(p.data) // In case we pause in the middle of a piece
					p.num, err = nextAvailablePieceIdx(p.num, t.numPieces, peerBitfield, t.bitfield)
					if err != nil {
						signalErrors <- err
						return
					}
				}

				reqMsg := createRequestMsg(p.num, blockOffset, numBytesToRequest)
				outboundMsgs <- reqMsg
				fmt.Printf("Requesting %v bytes for piece %v/%v from %v\n", numBytesToRequest, p.num, lastPieceNum, peerAddr)
			case have:
				updateBitfield(&peerBitfield, msg.pieceNum)
			}
		}
	}
}

func (t *Torrent) savePiece(p *pieceData, pieceSize int) error {
	pieceStartIdx := int64(p.num * t.pieceSize)
	pieceEndIdx := pieceStartIdx + int64(pieceSize)

	if len(t.files) == 1 {
		_, err := t.files[0].fd.WriteAt(p.data, pieceStartIdx)
		return err
	}

	// "For the purposes of piece boundaries in the multi-file case, consider the file data
	// as one long continuous stream, composed of the concatenation of each file in the order
	// listed in the files list. The number of pieces and their boundaries are then determined
	// in the same manner as the case of a single file. Pieces may overlap file boundaries."

	fileStartIdx := int64(0)

	for i := 0; i < len(t.files); i++ {
		currFile := t.files[i]
		currFd := currFile.fd
		fileEndIdx := fileStartIdx + currFile.finalSize

		if pieceStartIdx >= fileStartIdx && pieceEndIdx <= fileEndIdx {
			_, err := currFd.WriteAt(p.data, pieceStartIdx)
			return err
		} else if pieceStartIdx >= fileEndIdx {
			fileStartIdx += currFile.finalSize
			// fmt.Println("Piece belongs in next file")
			// } else if pieceStartIdx > fileStartIdx && pieceEndIdx > fileEndIdx {
		} else {
			// fmt.Println("Piece crosses file boundary")
			nextFile := t.files[i+1]
			nextFd := nextFile.fd

			boundaryIdx := fileEndIdx - pieceStartIdx
			leftHalf := p.data[:boundaryIdx]
			rightHalf := p.data[boundaryIdx:]

			_, err := currFd.WriteAt(leftHalf, pieceStartIdx)
			if err != nil {
				return err
			}

			_, err = nextFd.WriteAt(rightHalf, 0)
			if err != nil {
				return err
			}

			return nil
		}
	}

	return nil
}

func updateBitfield(bitfield *[]byte, pieceNum int) {
	pieceByteIdx := pieceNum / 8
	bitsFromRight := 7 - (pieceNum % 8)
	pieceByte := &((*bitfield)[pieceByteIdx])
	b := uint8(0x01) << bitsFromRight
	*pieceByte |= b
}

func connectAndParse(infoHash, myPeerId []byte, peerAddr string, parsedMessages chan<- peerMessage, outboundMsgs <-chan []byte, signalErrors chan<- error) {
	conn, err := net.DialTimeout("tcp", peerAddr, 1*time.Second)
	if err != nil {
		signalErrors <- err
		return
	}
	defer conn.Close()

	log.Println("Connected to peer", peerAddr)

	handshakeMsg := createHandshakeMsg(infoHash, myPeerId)
	if _, err := conn.Write(handshakeMsg); err != nil {
		signalErrors <- err
		return
	}

	msgBuf := make([]byte, 32*1024)
	msgBufEndIdx := 0

	waitDuration := 2 * time.Second
	t := time.NewTimer(waitDuration)
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
		case <-t.C:
			signalErrors <- errors.New("Waited too long")
			return
		default:
			// fmt.Println("Nothing to write")
		}

		const MAX_RESPONSE_SIZE = BLOCK_SIZE + 13 // Include piece message header
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

		if !t.Stop() {
			<-t.C
		}
		t.Reset(waitDuration)

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

		// Passing a slice of msgBuf to the parser would create msg structs that get cleared too
		copyToParse := slices.Clone(msgBuf[:msgBufEndIdx])
		msgList, err := parseMultiMessage(copyToParse, infoHash)
		if err != nil {
			signalErrors <- err
			return
		}

		firstFragmentIdx := 0
		for _, msg := range msgList {
			parsedMessages <- msg

			if msg.kind != fragment {
				firstFragmentIdx += msg.totalSize
			}
		}

		for i := 0; i < firstFragmentIdx; i++ {
			msgBuf[i] = 0
		}

		if firstFragmentIdx == msgBufEndIdx {
			msgBufEndIdx = 0
		} else if firstFragmentIdx != 0 {
			// Copy over leading 0s
			copy(msgBuf, msgBuf[firstFragmentIdx:])
			msgBufEndIdx = msgBufEndIdx - firstFragmentIdx
		}
	}
}

func getPeerId() string {
	return "edededededededededed"
}

// TODO: Do I need more than one?
func getNextFreePort() string {
	return "6881"
}

func createHandshakeMsg(infoHash []byte, peerId []byte) []byte {
	// <pstrlen><pstr><reserved><info_hash><peer_id>
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
		index := 5 + i*4
		binary.BigEndian.PutUint32(bytes[index:index+4], v)
	}

	return bytes
}

func randAvailablePieceIdx(numPieces int, peerBitfield, ourBitfield []byte) (int, error) {
	attempts := 0
	for attempts < numPieces {
		randByteIdx := rand.Intn(len(peerBitfield))
		b := peerBitfield[randByteIdx]
		for bitsFromRight := 7; bitsFromRight >= 0; bitsFromRight-- {
			mask := byte(1 << bitsFromRight)
			if b&mask != 0 && ourBitfield[randByteIdx]&mask == 0 {
				return randByteIdx*8 + (7 - bitsFromRight), nil
			}
			attempts++
		}
	}
	return 0, errors.New("Peer has no pieces we want")
}

func nextAvailablePieceIdx(currPieceNum, numPieces int, peerBitfield, ourBitfield []byte) (int, error) {
	nextPieceNum := currPieceNum + 1
	byteToCheckIdx := nextPieceNum / 8
	// if byteToCheckIdx == len(peerBitfield) || nextPieceNum == numPieces {
	if nextPieceNum == numPieces {
		byteToCheckIdx = 0
		nextPieceNum = 0
	}
	b := peerBitfield[byteToCheckIdx]

	attempts := 0
	for attempts < numPieces {
		for bitsFromRight := 7 - nextPieceNum%8; bitsFromRight >= 0; bitsFromRight-- {
			mask := byte(1 << bitsFromRight)
			if b&mask != 0 && ourBitfield[byteToCheckIdx]&mask == 0 {
				return byteToCheckIdx*8 + (7 - bitsFromRight), nil
			}
			attempts++
		}

		byteToCheckIdx++
		if byteToCheckIdx == len(peerBitfield) {
			byteToCheckIdx = 0
		}
		b = peerBitfield[byteToCheckIdx]
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
	totalSize   int
	peerId      []byte
	pieceNum    int
	bitfield    []byte
	blockOffset int
	blockSize   int
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
		i += msg.totalSize
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
		msg.totalSize = 4
		return
	}

	lenBytes := binary.BigEndian.Uint32(buf[:4])
	lenVal := int(lenBytes)

	if lenVal > len(buf) || len(buf) < 4 {
		// Message needs to be reassembled from this packet and the next
		// fmt.Printf("Message is %v bytes long but we only have %v so far\n", lenVal, len(buf))
		msg.kind = fragment
		msg.totalSize = len(buf)
		return
	}

	messageKind := int(buf[4])
	msg.kind = messageKinds(messageKind)

	if msg.kind == have || msg.kind == request || msg.kind == piece || msg.kind == cancel {
		msg.pieceNum = int(binary.BigEndian.Uint32(buf[5:9]))
	}

	msg.totalSize = lenVal + 4 // Include length prefix

	if msg.totalSize > len(buf) {
		panic(fmt.Sprintf("Out of bounds of msg buffer: %v / %v", msg.totalSize, len(buf)))
	}

	switch msg.kind {
	case bitfield:
		msg.bitfield = buf[5:msg.totalSize]
	case request:
		msg.blockOffset = int(binary.BigEndian.Uint32(buf[9:13]))
		msg.blockSize = int(binary.BigEndian.Uint32(buf[13:msg.totalSize]))
	case piece:
		msg.blockOffset = int(binary.BigEndian.Uint32(buf[9:13]))
		msg.blockData = buf[13:msg.totalSize]
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
	msg.totalSize = protocolLen + 49
	msg.peerId = peerId
	return msg, nil
}

func (t *Torrent) storeSeeders(n int) {
	t.Lock()
	defer t.Unlock()
	t.seeders = n
}

func (t *Torrent) storeLeechers(n int) {
	t.Lock()
	defer t.Unlock()
	t.leechers = n
}

func (t *Torrent) storeUploaded(n int) {
	t.Lock()
	defer t.Unlock()
	t.piecesUploaded = n
}

func (t *Torrent) storeDownloaded(n int) {
	t.Lock()
	defer t.Unlock()
	t.piecesDownloaded = n
}
