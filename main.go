package main

import (
	"fmt"
	"log"
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

	metaInfoMap := ParseMetaInfoFile(os.Args[1])

	// trackerURL := metaInfoMap["announce"]
	// suggestedTitle := metaInfoMap["title"]

	// Fields common to single-file and multi-file torrents
	infoDict := metaInfoMap["info"].(map[string]any)

	// pieceLength := infoDict["piece length"]
	// pieces := infoDict["pieces"]
	// isPrivate := infoDict["private"]

	files := infoDict["files"]

	var fileMode string
	if files != nil {
		fileMode = "multiple"
	} else {
		fileMode = "single"
	}

	if fileMode == "multiple" {
		for _, v := range files.([]any) {
			fileDict := v.(map[string]any)

			// length := fileDict["length"]

			pl := fileDict["path"].([]any)
			pathList := make([]string, len(pl))
			for i, v := range pl {
				pathList[i] = v.(string)
			}
			// fmt.Println(strings.Join(pathList, "/"), length, "bytes")
		}
	}
}
