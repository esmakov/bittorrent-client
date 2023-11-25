package main

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
)

func die(e error) {
	if e != nil {
		log.Fatalln(e)
	}
}

func main() {
	if len(os.Args) != 2 {
		die(fmt.Errorf("USAGE: bittorrent-client [*.torrent]"))
	}

	p := newParser()
	metaInfoMap, infoHash := p.ParseMetaInfoFile(os.Args[1])
	escapedInfoHash := CustomURLEscape(infoHash)

	trackerURL := metaInfoMap["announce"].(string)
	suggestedTitle := metaInfoMap["title"]
	comment := metaInfoMap["comment"]

	// Fields common to single-file and multi-file torrents
	infoDict := metaInfoMap["info"].(map[string]any)
	pieceLength := infoDict["piece length"]
	_ = pieceLength
	pieces := infoDict["pieces"]
	_ = pieces
	isPrivate := infoDict["private"]
	_ = isPrivate

	files := infoDict["files"].([]any)

	peerId := getPeerId()
	peerIdBytes := make([]byte, len(peerId))
	copy(peerIdBytes, peerId)

	var totalSize int
	var numFiles = len(files)
	var fileMode string
	if files != nil {
		fileMode = "multiple"
	} else {
		fileMode = "single"
	}

	// TODO: Handle single-file mode

	if fileMode == "multiple" {
		for _, v := range files {
			fileDict := v.(map[string]any)

			length := fileDict["length"]
			totalSize += length.(int)

			pl := fileDict["path"].([]any)
			pathList := make([]string, len(pl))
			for i, v := range pl {
				pathList[i] = v.(string)
			}
			// fmt.Println(strings.Join(pathList, "/"), length, "bytes")
		}
	}

	printStatus := func() {
		fmt.Println("------------------------------------------")
		fmt.Println("Name:", suggestedTitle)
		fmt.Println("Hash:", fmt.Sprintf("%x", infoHash))
		fmt.Println("Path:", os.Args[1])
		fmt.Println("Total size:", totalSize)
		fmt.Println("# of files:", numFiles)
		fmt.Println("Comments:", comment)
		fmt.Println("Tracker:", trackerURL)
	}
	printStatus()

	myPort := getNextFreePort()
	uploadedParan := "0"
	downloadedParam := "0"
	leftParam := "0"

	uri := url.URL{
		Opaque: trackerURL,
	}
	queryParams := url.Values{}
	queryParams.Set("peerId", peerId)
	queryParams.Set("port", myPort)
	queryParams.Set("uploaded", uploadedParan)
	queryParams.Set("downloaded", downloadedParam)
	queryParams.Set("left", leftParam)
	queryParams.Set("event", "started")
	uri.RawQuery = "info_hash=" + escapedInfoHash + "&" + queryParams.Encode()

	startReq, err := http.NewRequest("GET", uri.String(), nil)
	die(err)

	// fmt.Println(uri.String())
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

	numSeeders := response["complete"].(int)
	_ = numSeeders

	numLeechers := response["incomplete"].(int)
	_ = numLeechers

	// TODO: Handle dictionary model
	peerBytes := response["peers"].(string)

	// TODO: Handle case where peer is specified by host name
	peerList, err := extractCompactPeers(peerBytes)
	die(err)

	if len(peerList) == 0 {
		log.Fatalln("No peers for torrent:", suggestedTitle)
	}

	fmt.Println(peerList)

	handshakeMsg := getHandshake(infoHash, peerIdBytes)

	establishPeerConnection(handshakeMsg, peerList[0])
}

func getPeerId() string {
	return "edededededededededed"
}

func getNextFreePort() string {
	return "6881"
}

func extractCompactPeers(s string) ([]string, error) {
	list := []string{}

	if len(s)%6 != 0 {
		return nil, errors.New("Peer information must be a multiple of 6 bytes")
	}

	for i := 0; i < len(s); {
		ip := ""
		for j := 0; j < 4; j++ {
			ip += fmt.Sprintf("%d", s[i])
			if j != 3 {
				ip += "."
			}
			i++
		}
		ip += ":"
		for j := 0; j < 2; j++ {
			ip += fmt.Sprintf("%d", s[i])
			i++
		}
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

	if _, err := c.Write(handshakeMsg); err != nil {
		log.Fatalln(err)
	}
	// for {
	var response string
	fmt.Fscan(c, &response)
	fmt.Println(response)
	// }
	c.Close()
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
