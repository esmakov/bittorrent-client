package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

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
		fmt.Println("USAGE: <command> <.torrent>\nwhere commands are any one of 'add' or 'tree'")
		os.Exit(1)
	}
	metaInfoFileName := os.Args[2]

	if filepath.Ext(metaInfoFileName) != ".torrent" {
		fmt.Println(metaInfoFileName, "is not of type .torrent")
		os.Exit(1)
	}

	switch os.Args[1] {
	case "tree":
		if err := treeCmd.Parse(os.Args[3:]); err != nil {
			fmt.Println(err)
			os.Exit(1)
		}

		_, err := torrent.New(metaInfoFileName, true)
		if err != nil {
			fmt.Println(err)
			os.Exit(1)
		}
	case "add":
		if err := addCmd.Parse(os.Args[3:]); err != nil {
			fmt.Println(err)
			os.Exit(1)
		}

		t, err := openAndCheckTorrent(metaInfoFileName, false)
		if err != nil {
			fmt.Println(err)
			os.Exit(1)
		}

		if t.IsComplete() {
			fmt.Println(metaInfoFileName, "is complete, starting to seed...")
			// TODO: Start seeding
			return
		}

		fmt.Println(t)

		if err := t.Start(); err != nil {
			fmt.Printf("Torrent %v failed in Start: %v\n", metaInfoFileName, err)
			os.Exit(1)
		}
	default:
		fmt.Println("USAGE: No such subcommand")
		os.Exit(1)
	}
	return
}

func openAndCheckTorrent(metaInfoFileName string, shouldPrettyPrint bool) (*torrent.Torrent, error) {
	t, err := torrent.New(metaInfoFileName, shouldPrettyPrint)
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
