package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

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
	if filepath.Ext(metaInfoFileName) != ".torrent" {
		fmt.Println("File is not of type .torrent")
		return
	}

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
			fmt.Printf("%v is complete, starting to seed...\n", metaInfoFileName)
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
	fileBytes, err := os.ReadFile(metaInfoFileName)
	if err != nil {
		return nil, err
	}

	p := parser.New(shouldPrettyPrint)

	topLevelMap, infoHash, err := p.ParseMetaInfoFile(fileBytes)
	if err != nil {
		return nil, err
	}

	t, err := torrent.New(metaInfoFileName, topLevelMap, infoHash)
	if err != nil {
		return nil, err
	}

	filesToCheck, err := t.OpenOrCreateFiles()
	if err != nil {
		return nil, err
	}

	fmt.Println("Checking...")

	_, err = t.CheckAllPieces(filesToCheck)
	if err != nil {
		return nil, err
	}

	return t, nil
}
