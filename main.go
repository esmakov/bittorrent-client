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
		fmt.Println("USAGE: bittorrent-client [command] [*.torrent]")
		os.Exit(1)
	}

	addCmd := flag.NewFlagSet("add", flag.ExitOnError)
	treeCmd := flag.NewFlagSet("tree", flag.ExitOnError)

	if len(os.Args) < 3 {
		fmt.Println("USAGE: [command] [.torrent]")
		return
	}
	metaInfoFileName := os.Args[2]

	switch os.Args[1] {
	case "tree":
		if err := treeCmd.Parse(os.Args[3:]); err != nil {
			fmt.Println(err)
			return
		}

		_, err := addTorrent(metaInfoFileName, true)
		if err != nil {
			fmt.Println(err)
			return
		}
	case "add":
		if err := addCmd.Parse(os.Args[3:]); err != nil {
			fmt.Println(err)
			return
		}

		t, err := addTorrent(metaInfoFileName, false)
		if err != nil {
			fmt.Println(err)
			return
		}

		if t.IsComplete() {
			// TODO: Start seeding
			return
		}

		fmt.Println(t)

		if err := t.Start(); err != nil {
			fmt.Println(err)
			return
		}
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

	t, err := torrent.New(metaInfoFileName, topLevelMap, infoHash)
	if err != nil {
		return nil, err
	}

	return t, nil
}
