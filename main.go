package main

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strconv"

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

	var shouldPrettyPrint bool
	if slices.Contains(os.Args, "-print") {
		shouldPrettyPrint = true
	}
	p := parser.New(shouldPrettyPrint)

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

	waitInterval := trackerResponse["interval"].(int)
	_ = waitInterval

	if trackerId, ok := trackerResponse["tracker id"]; ok {
		// TODO: Include on future requests
		_ = trackerId.(string)
	}

	if numSeeders, ok := trackerResponse["complete"]; ok {
		_ = numSeeders.(int)
	}

	if numLeechers, ok := trackerResponse["incomplete"]; ok {
		_ = numLeechers.(int)
	}

	// TODO: Handle dictionary model of peer list
	peerStr := trackerResponse["peers"].(string)
	peerList, err := extractCompactPeers(peerStr)
	die(err)

	if len(peerList) == 0 {
		// TODO: Keep intermittently checking for peers in a separate goroutine
		log.Fatalln("No peers for torrent:", inputFileName)
	}

	fmt.Println(peerList)

	handshakeMsg := getHandshake(infoHash, peerIdBytes)
	establishPeerConnection(handshakeMsg, peerList[0])
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
	if event != "" {
		queryParams.Set("event", event)
	}
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
func getHandshake(infoHash []byte, peerId []byte) []byte {
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

// TODO: Check if peer_id is as expected (if dictionary model)
func establishPeerConnection(handshakeMsg []byte, peerAddr string) error {
	c, err := net.Dial("tcp", peerAddr)
	if err != nil {
		return err
	}
	defer c.Close()

	log.Println("Connection established with:", peerAddr)

	if _, err := c.Write(handshakeMsg); err != nil {
		return err
	}

	// for {
	bytes, err := io.ReadAll(c)
	if err != nil {
		return err
	}
	fmt.Println(len(bytes), bytes)
	// }

	return nil
}

// Messages of form: <4-byte big-Endian length>[message ID][payload]
var (
	keepalive     = []byte{0x00, 0x00, 0x00, 0x00}
	choke         = []byte{0x00, 0x00, 0x00, 0x01, 0x00}
	unchoke       = []byte{0x00, 0x00, 0x00, 0x01, 0x01}
	interested    = []byte{0x00, 0x00, 0x00, 0x01, 0x02}
	notinterested = []byte{0x00, 0x00, 0x00, 0x01, 0x03}
	// TODO: have, bitfield, request, piece, cancel, port
)

type peerConnectionState struct {
	am_choking      bool
	am_interested   bool
	peer_choking    bool
	peer_interested bool
}
