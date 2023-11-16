package main

import (
    "strings"
	"fmt"
	"github.com/esmakov/bittorrent-client/parser"
	"os"
    _"net/url"
)

func die(e error) {
	if e != nil {
		panic(e)
	}
}

func main() {
	if len(os.Args) != 2 {
		die(fmt.Errorf("USAGE: bittorrent-client [*.torrent]"))
	}
	fh, err := os.Open(os.Args[1])
	die(err)
	defer fh.Close()

	metaInfoMap := parser.ParseMetaInfo(fh)

	trackerURL := metaInfoMap["announce"]
	suggestedTitle := metaInfoMap["title"]

	// Fields common to single-file and multi-file torrents
	infoDict := metaInfoMap["info"].(map[string]any)
	pieceLength := infoDict["piece length"]
	pieces := infoDict["pieces"]
	isPrivate := infoDict["private"]
    _ = isPrivate
    
	files := infoDict["files"]

	var fileMode string
	if files != nil {
		fileMode = "multiple"
	} else {
		fileMode = "single"
	}

	fmt.Println(suggestedTitle)
    fmt.Println(trackerURL)
	fmt.Println(pieceLength)
	fmt.Println(pieces)

	if fileMode == "multiple" {
		for _, v := range files.([]any) {
			fileDict := v.(map[string]any)

			length := fileDict["length"]

			pl := fileDict["path"].([]any)
            pathList := make([]string, len(pl))
            for i, v := range pl {
                pathList[i] = v.(string)
            }
			fmt.Println(strings.Join(pathList, "/"), length, "bytes")
		}
        // fmt.Println(url.PathEscape())
	}
}
