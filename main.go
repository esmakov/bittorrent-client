package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/esmakov/bittorrent-client/parser"
	"github.com/esmakov/bittorrent-client/torrent"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Println("USAGE: bittorrent-client add [*.torrent]")
		os.Exit(1)
	}

	addCmd := flag.NewFlagSet("add", flag.ExitOnError)
	shouldPrettyPrint := addCmd.Bool("print", false, "Pretty print the metainfo file parse tree")

	switch os.Args[1] {
	case "add":
		if len(os.Args) < 3 {
			fmt.Println("USAGE: add [.torrent]")
			return
		}
		metaInfoFileName := os.Args[2]
		if err := addCmd.Parse(os.Args[3:]); err != nil {
			fmt.Println(err)
			return
		}

		t, err := addTorrent(metaInfoFileName, *shouldPrettyPrint)
		if err != nil {
			fmt.Println(err)
			return
		}
		fmt.Println(t)

		if err := t.Start(); err != nil {
			fmt.Println(err)
			return
		}
		// Return to background
	default:
		fmt.Println("USAGE: No such subcommand")
		return
	}
	return
}

func addTorrent(metaInfoFileName string, shouldPrettyPrint bool) (*torrent.Torrent, error) {
	fd, err := os.Open(metaInfoFileName)
	if err != nil {
		return new(torrent.Torrent), err
	}
	defer fd.Close()

	p := parser.New(shouldPrettyPrint)
	topLevelMap, infoHash, err := p.ParseMetaInfoFile(fd)
	if err != nil {
		return new(torrent.Torrent), err
	}

	infoMap := topLevelMap["info"].(map[string]any)
	pieceLength := infoMap["piece length"].(int)
	_ = pieceLength
	piecesStr := infoMap["pieces"].(string)

	pieceHashes, err := p.MapPieceIndicesToHashes(piecesStr)
	if err != nil {
		return new(torrent.Torrent), err
	}

	t := torrent.New(metaInfoFileName, topLevelMap, infoHash, pieceHashes)
	return t, nil
}
