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
	"strconv"

	"github.com/esmakov/bittorrent-client/hash"
	"github.com/esmakov/bittorrent-client/parser"
)

func die(e error) {
	if e != nil {
		log.Fatalln(e)
	}
}

func main() {
	if len(os.Args) != 2 {
		die(errors.New("USAGE: bittorrent-client [*.torrent]"))
	}

	p := parser.New()
	metaInfoMap, infoHash := p.ParseMetaInfoFile(os.Args[1])
	escapedInfoHash := hash.CustomURLEscape(infoHash)

	trackerURL := metaInfoMap["announce"].(string)
	comment := metaInfoMap["comment"]

	infoDict := metaInfoMap["info"].(map[string]any)

	// Fields common to single-file and multi-file torrents
	pieceLength := infoDict["piece length"].(int)
	_ = pieceLength
	pieces := infoDict["pieces"].(string)
	_ = pieces
	isPrivate := infoDict["private"]
	_ = isPrivate

	peerId, peerIdBytes := getPeerId()

	var fileMode string
	files := infoDict["files"]
	if files != nil {
		fileMode = "multiple"
	} else {
		fileMode = "single"
	}

	var totalSize int
	numFiles := 1

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

			// fmt.Println(strings.Join(pathList, "/"), length, "bytes")
		}
	} else if fileMode == "single" {
		name := infoDict["name"].(string)
		_ = name
		length := infoDict["length"].(int)
		totalSize += length
	}

	printStatus := func() {
		fmt.Println("------------------------------------------")
		fmt.Println("File path:", os.Args[1])
		fmt.Println("Tracker:", trackerURL)
		fmt.Println("Hash:", fmt.Sprintf("% x", infoHash))
		fmt.Println("Total size:", totalSize)
		fmt.Println("# of files:", numFiles)
		if comment != nil {
			fmt.Println("Comment:", comment)
		}
	}
	printStatus()

	portForTrackerResponse := getNextFreePort()
	uploadedParam := "0"
	downloadedParam := "0"
	leftParam := strconv.Itoa(totalSize)

	trackerStartURL := url.URL{
		Opaque: trackerURL,
	}
	queryParams := url.Values{}
	queryParams.Set("peer_id", peerId)
	queryParams.Set("port", portForTrackerResponse)
	queryParams.Set("uploaded", uploadedParam)
	queryParams.Set("downloaded", downloadedParam)
	queryParams.Set("left", leftParam)
	queryParams.Set("event", "started")
	queryParams.Set("compact", "1")
	trackerStartURL.RawQuery = "info_hash=" + escapedInfoHash + "&" + queryParams.Encode()

	startReq, err := http.NewRequest("GET", trackerStartURL.String(), nil)
	die(err)

	startReq.Close = true
	startReq.Header.Set("Connection", "close")

	// fmt.Println(startReq.Header)
	// fmt.Println(trackerStartURL.String())
	// return

	client := &http.Client{}
	resp, err := client.Do(startReq)
	die(err)
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	die(err)

	response := p.ParseResponse(bufio.NewReader(bytes.NewReader(body)))
	fmt.Println(response)

	if failureReason, ok := response["failure reason"]; ok {
		die(errors.New(failureReason.(string)))
	}

	if warning, ok := response["warning message"]; ok {
		log.Println("TRACKER WARNING:", warning.(string))
	}

	waitInterval := response["interval"].(int)
	_ = waitInterval

	if trackerId, ok := response["tracker id"]; ok {
		_ = trackerId.(string)
	}

	if numSeeders, ok := response["complete"]; ok {
		_ = numSeeders.(int)
	}

	if numLeechers, ok := response["incomplete"]; ok {
		_ = numLeechers.(int)
	}

	// TODO: Handle dictionary model of peer list
	peerBytes := response["peers"].(string)
	peerList, err := extractCompactPeers(peerBytes)
	die(err)

	if len(peerList) == 0 {
		// TODO: Keep intermittently checking for peers in a separate goroutine
		log.Fatalln("No peers for torrent:", os.Args[1])
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

func extractCompactPeers(s string) ([]string, error) {
	list := []string{}

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

func establishPeerConnection(handshakeMsg []byte, peerAddr string) {
	c, err := net.Dial("tcp", peerAddr)
	die(err)
	defer c.Close()

	if _, err := c.Write(handshakeMsg); err != nil {
		log.Fatalln(err)
	}
	// for {
	bytes, err := io.ReadAll(c)
	die(err)
	fmt.Println(bytes)
	// }

	// TODO: Check if peer_id is as expected (if dictionary model)
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
