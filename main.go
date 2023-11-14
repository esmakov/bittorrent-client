package main

import (
	"fmt"
	"os"
	"github.com/esmakov/bittorrent-client/parser"
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

    metaInfoMap := parser.ParseInfo(fh)

    trackerURL := metaInfoMap["announce"]
    suggestedTitle := metaInfoMap["title"]
    urlList := metaInfoMap["url-list"]

    // Fields common to single-file and multi-file torrents
    infoDict := metaInfoMap["info"].(map[string]any)
    pieceLength := infoDict["piece length"]
    pieces := infoDict["pieces"]
    isPrivate := infoDict["private"]
    files := infoDict["files"]

    var fileMode string
    if files != nil {
        fileMode = "multiple"
    } else {
        fileMode = "single"
    }

    fmt.Println(suggestedTitle, trackerURL, urlList)
    fmt.Println(pieceLength)
    fmt.Println(pieces)
    fmt.Println(isPrivate)

    if fileMode == "multiple" {
        for _, v := range files.([]any) {
            fileDict := v.(map[string]any)
            length := fileDict["length"]
            pathList := fileDict["path"]
            // fmt.Println(strings.Join(pathList, "/"))
            fmt.Println(pathList.([]any))
            fmt.Println(length)
        }
    }
}
