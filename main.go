package main

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strconv"
	"time"

	"github.com/esmakov/bittorrent-client/hash"
	"github.com/esmakov/bittorrent-client/parser"
)

type torrent struct {
	metaInfoFileName string
	trackerHostname  string
	infoHash         []byte
	totalSize        int
	files            int
	comment          string
	left             int
	downloaded       int
	uploaded         int
	trackerId        string
	seeders          int
	leechers         int
}

func newTorrent(metaInfoFileName, trackerHostname, comment string, infoHash []byte) *torrent {
	return &torrent{
		metaInfoFileName: metaInfoFileName,
		trackerHostname:  trackerHostname,
		comment:          comment,
		infoHash:         infoHash,
		files:            1,
	}
}

func (t torrent) String() string {
	str := fmt.Sprintln(
		fmt.Sprintln("-------------Torrent Info---------------"),
		fmt.Sprintln("File path:", t.metaInfoFileName),
		fmt.Sprintln("Tracker:", t.trackerHostname),
		fmt.Sprintf("Hash: % x\n", t.infoHash),
		fmt.Sprintf("Total size: %v\n", t.totalSize),
		fmt.Sprintf("# of files: %v\n", t.files))

	if t.comment != "" {
		str += fmt.Sprintln("Comment:", t.comment)
	}
	return str
}

func main() {
	if len(os.Args) < 2 {
		fmt.Println("USAGE: bittorrent-client [*.torrent]")
		os.Exit(1)
	}

	shouldPrettyPrint := flag.Bool("print", false, "Pretty print the metainfo file parse tree")
	flag.Parse()

	p := parser.New(*shouldPrettyPrint)

	metaInfoFileName := flag.Args()[0]
	metaInfoMap, infoHash, err := p.ParseMetaInfoFile(metaInfoFileName)
	if err != nil {
		log.Println(err)
	}

	// Fields common to single-file and multi-file torrents
	trackerHostname := metaInfoMap["announce"].(string)
	infoDict := metaInfoMap["info"].(map[string]any)
	pieceLength := infoDict["piece length"].(int)
	_ = pieceLength
	concatPieceHashes := infoDict["pieces"].(string)
	_ = concatPieceHashes

	// Optional common fields
	isPrivate := 0
	if isPrivateEntry, ok := infoDict["private"]; ok {
		isPrivate = isPrivateEntry.(int)
	}
	_ = isPrivate
	comment := ""
	if commentEntry, ok := metaInfoMap["comment"]; ok {
		comment = commentEntry.(string)
	}

	var fileMode string
	files := infoDict["files"]
	if files != nil {
		fileMode = "multiple"
	} else {
		fileMode = "single"
	}

	// pieceHashes, err := hash.HashPieces(pieces, pieceLength)
	// if err != nil {
	// 	log.Println(err)
	// }
	t := newTorrent(metaInfoFileName, trackerHostname, comment, infoHash)

	if fileMode == "multiple" {
		files := files.([]any)
		t.files = len(files)

		for _, v := range files {
			fileDict := v.(map[string]any)

			length := fileDict["length"].(int)
			t.totalSize += length

			pl := fileDict["path"].([]any)
			var pathList []string
			for _, v := range pl {
				pathList = append(pathList, v.(string))
			}
			// TODO: Concatenate all files together and then extract piece hashes
			// fmt.Println(strings.Join(pathList, "/"), length, "bytes")
		}
	} else if fileMode == "single" {
		name := infoDict["name"].(string)
		_ = name
		length := infoDict["length"].(int)
		t.totalSize += length
	}

	t.left = t.totalSize
	fmt.Println(t)

	myPeerId, myPeerIdBytes := getPeerId()
	portForTrackerResponse := getNextFreePort()
	event := "started" // TODO: Enum

	trackerResponse, err := sendTrackerMessage(*t, myPeerId, portForTrackerResponse, trackerHostname, event)
	if err != nil {
		log.Println(err)
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
		t.seeders = numSeeders.(int)
	}

	if numLeechers, ok := trackerResponse["incomplete"]; ok {
		t.leechers = numLeechers.(int)
	}

	// TODO: Handle dictionary model of peer list
	peersStr := trackerResponse["peers"].(string)
	peerList, err := extractCompactPeers(peersStr)
	if err != nil {
		log.Println(err)
	}

	if len(peerList) == 0 {
		// TODO: Keep intermittently checking for peers in a separate goroutine
		log.Println("No peers for torrent:", metaInfoFileName)
	}

	handshakeMsg := createHandshakeMsg(infoHash, myPeerIdBytes)

	// TODO: Create multiple concurrent connections
	const MAX_PEERS = 5

	// One goroutine has to listen for incoming connections
	// Another goroutine has to keep talking to the tracker
	// The last has to establish them:
	// While # of connections <= MAX_PEERS, go through list of peers and attempt to establish a conn, passing in connPool
	// If don't get EOF, add to connPool and start loop of messages
	//		Need to be able to close connection and continue seeking again (optimistic unchoking)
	//		As piece messages come in, mutate our shared state (pieces we have), but how to avoid data races? Mutex?

	for _, peer := range peerList {
		if err := establishPeerConnection(handshakeMsg, t.infoHash, peer); err != nil {
			log.Println(err)
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
	partial
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
	kind           messageKinds
	endIdxInMsg    int
	pieceIdxInFile int
	bitfield       []byte
	blockOffset    int
	blockLen       int
	blockData      []byte
}

// TODO: Check if peer_id is as expected (if dictionary model)
func establishPeerConnection(handshakeMsg, expectedInfoHash []byte, peerAddr string) error {
	conn, err := net.DialTimeout("tcp", peerAddr, time.Millisecond*500)
	if err != nil {
		return err
	}
	defer conn.Close()

	log.Println("Connected to peer", peerAddr)

	if _, err := conn.Write(handshakeMsg); err != nil {
		return err
	}

	msgBuf := make([]byte, 512)
	connState := newConnState()

	for {
		numRead, err := conn.Read(msgBuf) // I hope it only appends
		if err != nil {
			return err
		}

		fmt.Println("Got response of length:", numRead)
		msgList, err := parseMultiMessage(msgBuf[:numRead], expectedInfoHash)
		if err != nil {
			return err
		}

		for _, msg := range msgList {
			switch msg.kind {
			case choke:
				connState.peer_choking = true
			case unchoke:
				connState.peer_choking = false
			case interested:
				connState.peer_interested = true
			case uninterested:
				connState.peer_interested = false
			}
		}

		// if last message was fragment:
		// clear buffer only up to the start of fragment
		clearUpTo := len(msgBuf)

		for i := 0; i < len(msgList)-1; i++ {
			if msgList[i+1].kind == partial {
				clearUpTo = msgList[i].endIdxInMsg
			}
		}

		for i := 0; i < clearUpTo; i++ {
			msgBuf[i] = 0
		}
		// read bitfield and choose one of its pieces
		// send request msg
	}

}

// Peers sometimes send multiple messages per packet
func parseMultiMessage(buf, expectedInfoHash []byte) ([]peerMessage, error) {
	var msgList []peerMessage
	for i := 0; i < len(buf)-1; {
		msg, err := parseMessage(buf[i:], expectedInfoHash)
		if err != nil {
			return nil, err
		}
		fmt.Println(msg.kind)
		msgList = append(msgList, msg)
		i += msg.endIdxInMsg
	}
	return msgList, nil
}

func parseMessage(buf, expectedInfoHash []byte) (msg peerMessage, e error) {
	// If a peer supplies a handshake, validate it first
	// TODO: More robust check for handshake presence
	if int(buf[0]) == 19 {
		msg, e = parseHandshake(buf, expectedInfoHash)
		// if e != nil {
		// 	return
		// }
		return
	}

	if len(buf) == 4 {
		msg.kind = keepalive
		msg.endIdxInMsg = 4
		return
	}

	lenBytes := binary.BigEndian.Uint32(buf[:4])
	lenVal := int(lenBytes)

	if lenVal > len(buf) {
		// Message needs to be reassembled from multiple TCP packets
		msg.kind = partial
		msg.endIdxInMsg = len(buf)
		return
	}

	messageKind := int(buf[4])

	var lenWithoutPrefix int

	switch messageKinds(messageKind) {
	case choke:
		msg.kind = choke
		lenWithoutPrefix = 1
	case unchoke:
		msg.kind = unchoke
		lenWithoutPrefix = 1
	case interested:
		msg.kind = interested
		lenWithoutPrefix = 1
	case uninterested:
		msg.kind = uninterested
		lenWithoutPrefix = 1
	case have:
		msg.kind = have
		lenWithoutPrefix = 5
	case bitfield:
		msg.kind = bitfield
		lenWithoutPrefix = 1 + lenVal
	case request:
		msg.kind = request
		lenWithoutPrefix = 13
	case piece:
		msg.kind = piece
		lenWithoutPrefix = 9 + lenVal
	case cancel:
		msg.kind = cancel
		lenWithoutPrefix = 13
	case port:
		msg.kind = port
		lenWithoutPrefix = 3
	}

	if msg.kind == have || msg.kind == request || msg.kind == piece || msg.kind == cancel {
		msg.pieceIdxInFile = int(binary.BigEndian.Uint32(buf[5:9]))
	}

	// TODO: why not 4?
	msg.endIdxInMsg = lenWithoutPrefix + 3

	if msg.endIdxInMsg > len(buf) {
		panic(fmt.Sprintf("Out of bounds of msg buffer: %v / %v", msg.endIdxInMsg, len(buf)))
	}

	switch msg.kind {
	case bitfield:
		msg.bitfield = buf[5:msg.endIdxInMsg]
	case request:
		msg.blockOffset = int(binary.BigEndian.Uint32(buf[9:13]))
		msg.blockLen = int(binary.BigEndian.Uint32(buf[13:msg.endIdxInMsg]))
	case piece:
		msg.blockOffset = int(binary.BigEndian.Uint32(buf[9:13]))
		msg.blockData = buf[13:msg.endIdxInMsg]
	}

	return
}

func parseHandshake(buf []byte, expectedInfoHash []byte) (peerMessage, error) {
	msg := peerMessage{}
	protocolLen := int(buf[0]) // Should be 19

	// protocolBytes := buf[1 : protocolLen+1]
	// fmt.Println(string(protocolBytes))
	// reservedBytes := buf[protocolLen+1 : protocolLen+9]

	theirInfoHash := buf[protocolLen+9 : protocolLen+29]
	if !slices.Equal(theirInfoHash, expectedInfoHash) {
		return msg, errors.New("Peer did not respond with correct info hash")
	}
	peerId := buf[protocolLen+29 : protocolLen+49]
	fmt.Println("Peer has id", string(peerId))

	msg.kind = handshake
	msg.endIdxInMsg = protocolLen + 49
	return msg, nil
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

func sendTrackerMessage(t torrent, peerId, portForTrackerResponse, trackerHostname, event string) (map[string]any, error) {
	reqURL := url.URL{
		Opaque: trackerHostname,
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

// handshake: <pstrlen><pstr><reserved><info_hash><peer_id>
func createHandshakeMsg(infoHash []byte, peerId []byte) []byte {
	pstr := "BitTorrent protocol"
	pstrBytes := make([]byte, len(pstr))
	copy(pstrBytes, pstr)

	pstrLenByte := []byte{byte(len(pstr))}
	reserved := []byte{0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}

	return concatMultipleSlices([][]byte{pstrLenByte, pstrBytes, reserved, infoHash, peerId})
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
