package main

import (
	"bufio"
	"fmt"
	"os"
)

type bt_token int

const (
    bt_digit bt_token = iota
    bt_colon
    bt_num
    bt_str 
    bt_dict
    bt_list
)

func die(e error) {
    if e != nil {
        panic(e)
    }
}

func main() {
    if len(os.Args) != 2 {
        fmt.Println()
        die(fmt.Errorf("USAGE: bittorrent-client [.torrent FILENAME]"))
    }

    fh, err := os.Open(os.Args[1])
    die(err)
    defer fh.Close()

    fscan := bufio.NewScanner(fh)

    var fileLines []string

    for fscan.Scan() {
        fileLines = append(fileLines, fscan.Text())
    }

    fmt.Println(fileLines)
}
