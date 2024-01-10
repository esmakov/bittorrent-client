package main

import (
	"flag"
	"fmt"
	"io"
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
	printParseTree := addCmd.Bool("print", false, "Pretty print the metainfo file parse tree")

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

		t, err := addTorrent(metaInfoFileName, *printParseTree)
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
		return nil, err
	}
	defer fd.Close()

	p := parser.New(shouldPrettyPrint)

	fileBytes, err := io.ReadAll(fd)
	if err != nil {
		return nil, err
	}

	topLevelMap, infoHash, err := p.ParseMetaInfoFile(fileBytes)
	if err != nil {
		return nil, err
	}

	t := torrent.New(metaInfoFileName, topLevelMap, infoHash)

	return t, nil
}
