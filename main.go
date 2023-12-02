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

func die(e error) {
	if e != nil {
		panic(e)
	}
}

type torrentInfo struct {
	inputFileName   string
	trackerHostname string
	infoHash        []byte
	totalSize       int
	numFiles        int
	comment         string
}

func (t torrentInfo) String() string {
	str := fmt.Sprintln(
		fmt.Sprintln("-------------Torrent Info---------------"),
		fmt.Sprintln("File path:", t.inputFileName),
		fmt.Sprintln("Tracker:", t.trackerHostname),
		fmt.Sprintf("Hash: % x\n", t.infoHash),
		fmt.Sprintf("Total size: %v\n", t.totalSize),
		fmt.Sprintf("# of files: %v\n", t.numFiles))

	if t.comment != "" {
		str += fmt.Sprintln("Comment:", t.comment)
	}
	return str
}

func main() {
	if len(os.Args) < 2 {
		die(errors.New("USAGE: bittorrent-client [*.torrent]"))
	}

	shouldPrettyPrint := flag.Bool("print", false, "Pretty print the metainfo file parse tree")
	flag.Parse()

	p := parser.New(*shouldPrettyPrint)

	inputFileName := os.Args[1]
	metaInfoMap, infoHash, err := p.ParseMetaInfoFile(inputFileName)
	if err != nil {
		log.Println(err)
	}

	trackerHostname := metaInfoMap["announce"].(string)
	infoDict := metaInfoMap["info"].(map[string]any)
	comment := metaInfoMap["comment"].(string)

	// Fields common to single-file and multi-file torrents
	// pieceLength := infoDict["piece length"].(int)
	// concatPieceHashes := infoDict["pieces"].(string)

	isPrivate := infoDict["private"]
	_ = isPrivate

	var fileMode string
	files := infoDict["files"]
	if files != nil {
		fileMode = "multiple"
	} else {
		fileMode = "single"
	}

	var totalSize int
	numFiles := 1

	// pieceHashes, err := hash.HashPieces(pieces, pieceLength)
	// if err != nil {
	// 	log.Println(err)
	// }

	if fileMode == "multiple" {
		files := files.([]any)
		numFiles = len(files)

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
		name := infoDict["name"].(string)
		_ = name
		length := infoDict["length"].(int)
		totalSize += length
	}

	t := torrentInfo{inputFileName, trackerHostname, infoHash, totalSize, numFiles, comment}
	fmt.Println(t)

	escapedInfoHash := hash.CustomURLEscape(t.infoHash)
	leftParam := strconv.Itoa(t.totalSize)
	uploadedParam := "0"
	downloadedParam := "0"
	event := "started"
	peerId, peerIdBytes := getPeerId()
	portForTrackerResponse := getNextFreePort()

	trackerResponse, err := sendTrackerMessage(peerId, portForTrackerResponse, uploadedParam, downloadedParam, leftParam, escapedInfoHash, trackerHostname, event)
	if err != nil {
		log.Println(err)
	}
	log.Println("Tracker reponse:", trackerResponse)

	if warning, ok := trackerResponse["warning message"]; ok {
		log.Println("TRACKER WARNING:", warning.(string))
	}

	// 	waitInterval := trackerResponse["interval"].(int)
	// 	_ = waitInterval

	// 	if trackerId, ok := trackerResponse["tracker id"]; ok {
	// 		// TODO: Include on future requests
	// 		_ = trackerId.(string)
	// 	}

	// 	if numSeeders, ok := trackerResponse["complete"]; ok {
	// 		_ = numSeeders.(int)
	// 	}

	// 	if numLeechers, ok := trackerResponse["incomplete"]; ok {
	// 		_ = numLeechers.(int)
	// 	}

	// TODO: Handle dictionary model of peer list
	peersStr := trackerResponse["peers"].(string)
	peerList, err := extractCompactPeers(peersStr)
	if err != nil {
		log.Println(err)
	}

	if len(peerList) == 0 {
		// TODO: Keep intermittently checking for peers in a separate goroutine
		log.Println("No peers for torrent:", inputFileName)
	}

	// fmt.Println(peerList)

	handshakeMsg := createHandshakeMsg(infoHash, peerIdBytes)
	// TODO: Create multiple concurrent connections
	for _, peer := range peerList {
		if err := establishPeerConnection(handshakeMsg, t.infoHash, peer); err != nil {
			log.Println(err)
		}
	}
}

// Messages of form: <4-byte big-Endian length>[message ID][payload]
var (
	keepaliveBytes     = []byte{0x00, 0x00, 0x00, 0x00}
	chokeBytes         = []byte{0x00, 0x00, 0x00, 0x01, 0x00}
	unchokeBytes       = []byte{0x00, 0x00, 0x00, 0x01, 0x01}
	interestedBytes    = []byte{0x00, 0x00, 0x00, 0x01, 0x02}
	notinterestedBytes = []byte{0x00, 0x00, 0x00, 0x01, 0x03}
	// TODO: have, bitfield, request, piece, cancel, port
)

type peerConnState struct {
	am_choking      bool
	am_interested   bool
	peer_choking    bool
	peer_interested bool
}

// TODO: Check if peer_id is as expected (if dictionary model)
func establishPeerConnection(handshakeMsg, expectedInfoHash []byte, peerAddr string) error {
	conn, err := net.DialTimeout("tcp", peerAddr, time.Millisecond*500)
	if err != nil {
		return err
	}
	defer conn.Close()

	log.Println("Connection established with peer", peerAddr)

	if _, err := conn.Write(handshakeMsg); err != nil {
		return err
	}

	msgBuf := make([]byte, 512)

	// for {
	numRead, err := conn.Read(msgBuf)
	if err != nil {
		return err
	}

	fmt.Println("Saw one message of length:", numRead)
	msgList, err := parseMultiMessage(msgBuf[:numRead], expectedInfoHash)
	_ = msgList
	if err != nil {
		return err
	}

	clear(msgBuf)
	// }

	// connState := peerConnState{}
	// fmt.Printf("%+v\n", connState)

	// read bitfield and choose one of its nonzero indices
	// send request msg

	return nil
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
)

type peerMessage struct {
	kind        messageKinds
	endIdx      int
	pieceIdx    int
	bitfield    []byte
	blockOffset int
	blockLen    int
	blockData   []byte
}

func parseMultiMessage(buf, expectedInfoHash []byte) ([]peerMessage, error) {
	var msgList []peerMessage
	for i := 0; i < len(buf)-1; {
		fmt.Println("Starting to parse at idx:", i)
		msg, err := parseMessage(buf[i:], expectedInfoHash)
		if err != nil {
			return nil, err
		}
		msgList = append(msgList, msg)
		fmt.Printf("Messages: %+v\n", msgList)
		i += msg.endIdx
	}
	return msgList, nil
}

func parseMessage(buf, expectedInfoHash []byte) (peerMessage, error) {
	var msg peerMessage

	// If a peer supplies an invalid handshake, close the connection
	if int(buf[0]) == 19 {
		msg, err := verifyHandshake(buf, expectedInfoHash)
		if err != nil {
			return *new(peerMessage), err
		}
		return msg, nil
	}

	if len(buf) == 4 {
		msg.kind = keepalive
		msg.endIdx = 4
		return msg, nil
	}

	fmt.Printf("Msg contents: % x\n", buf)
	fmt.Printf("Bytes for msg length: % x\n", buf[:4])
	lenVal := int(binary.BigEndian.Uint32(buf[:4]))
	fmt.Println("Message length as int:", lenVal)

	messageKind := int(buf[4])
	// fmt.Println("Message kind", messageKind)

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
		msg.pieceIdx = int(binary.BigEndian.Uint32(buf[5:9]))
	}

	msg.endIdx = lenWithoutPrefix + 3 // Add bytes for length prefix

	if msg.endIdx > len(buf) {
		return *new(peerMessage), errors.New("Out of bounds")
	}

	// Handle variable length messages
	// if !(msg.kind == bitfield || msg.kind == piece) {
	// 	return msg, nil
	// }

	switch msg.kind {
	case bitfield:
		msg.bitfield = buf[5:msg.endIdx]
	case request:
		msg.blockOffset = int(binary.BigEndian.Uint32(buf[9:13]))
		msg.blockLen = int(binary.BigEndian.Uint32(buf[13:msg.endIdx]))
	case piece:
		msg.blockOffset = int(binary.BigEndian.Uint32(buf[9:13]))
		msg.blockData = buf[13:msg.endIdx]
	}

	return msg, nil
}

func verifyHandshake(buf []byte, expectedInfoHash []byte) (peerMessage, error) {
	// if len(buf) != 68 {
	// 	return errors.New("Handshake is not typical for BitTorrent Protocol 1.0")
	// }
	msg := peerMessage{}
	protocolLen := int(buf[0]) // Should be 19
	// protocolBytes := buf[1 : protocolLen+1]
	// fmt.Println(string(protocolBytes))
	// reservedBytes := buf[protocolLen+1 : protocolLen+9]

	// Skip reserved bytes
	theirInfoHash := buf[protocolLen+9 : protocolLen+29]
	if !slices.Equal(theirInfoHash, expectedInfoHash) {
		return msg, errors.New("Peer did not respond with correct info hash")
	}
	peerId := buf[protocolLen+29 : protocolLen+49]
	fmt.Println("Peer has id", string(peerId))

	msg.kind = handshake
	msg.endIdx = protocolLen + 49
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

func sendTrackerMessage(peerId, portForTrackerResponse, uploadedParam, downloadedParam, leftParam, escapedInfoHash, trackerHostname, event string) (map[string]any, error) {
	reqURL := url.URL{
		Opaque: trackerHostname,
	}
	queryParams := url.Values{}
	queryParams.Set("peer_id", peerId)
	queryParams.Set("port", portForTrackerResponse)
	queryParams.Set("uploaded", uploadedParam)
	queryParams.Set("downloaded", downloadedParam)
	queryParams.Set("left", leftParam)
	queryParams.Set("event", event)
	queryParams.Set("compact", "1")
	reqURL.RawQuery = "info_hash=" + escapedInfoHash + "&" + queryParams.Encode()

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

	pstrLenBytes := []byte{byte(len(pstr))}
	reserved := []byte{0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}

	return concatMultipleSlices([][]byte{pstrLenBytes, pstrBytes, reserved, infoHash, peerId})
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
