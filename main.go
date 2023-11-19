package main

import (
	"fmt"
	"io"
	"log"
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

	metaInfoMap, infoHash := ParseMetaInfoFile(os.Args[1])
	escapedInfoHash := CustomURLEscape(infoHash)

	trackerURL := metaInfoMap["announce"].(string)
	suggestedTitle := metaInfoMap["title"]
	comment := metaInfoMap["comment"]

	// Fields common to single-file and multi-file torrents
	infoDict := metaInfoMap["info"].(map[string]any)
	// pieceLength := infoDict["piece length"]
	// pieces := infoDict["pieces"]
	// isPrivate := infoDict["private"]

	files := infoDict["files"].([]any)

	peerId := url.QueryEscape(makePeerId())

	var totalSize int
	var numFiles = len(files)
	var fileMode string
	if files != nil {
		fileMode = "multiple"
	} else {
		fileMode = "single"
	}

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
		fmt.Println("Name:", suggestedTitle)
		fmt.Println("Hash:", fmt.Sprintf("%x", infoHash))
		fmt.Println("Path:", os.Args[1])
		fmt.Println("Total size:", totalSize)
		fmt.Println("# of files:", numFiles)
		fmt.Println("Comments:", comment)
		fmt.Println("Tracker:", trackerURL)
	}
	printStatus()

	port := findNextFreePort()
	uploaded := "0"
	downloaded := "0"
	left := "0"

	uri := url.URL{
		Opaque: trackerURL,
	}
	queryParams := url.Values{}
	// queryParams.Set("info_hash", escapedInfoHash)
	queryParams.Set("peerId", peerId)
	queryParams.Set("uploaded", uploaded)
	queryParams.Set("downloaded", downloaded)
	queryParams.Set("left", left)
	uri.RawQuery = "info_hash" + escapedInfoHash + "&" + queryParams.Encode()
	// uri.RawQuery = queryParams.Encode()
	fmt.Println(uri.String())

	req, err := http.NewRequest("GET", "", nil)
	die(err)
	return

	client := &http.Client{}
	resp, err := client.Do(req)
	die(err)
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	die(err)
	fmt.Println(body)

	http.HandleFunc("/", func(w http.ResponseWriter, req *http.Request) {
		fmt.Println(req)
	})
	http.ListenAndServe(":"+port, nil)
}

func makePeerId() string {
	return "edededededededededed"
}
func findNextFreePort() string {
	return "6881"
}
