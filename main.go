package main

import (
	"bufio"
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

	scan := bufio.NewScanner(fh)
    // scan := bufio.NewScanner(strings.NewReader("d3:keyli1e3:stre10:anotherkeyd3:keyli3ei4ee3:key3:valeeld3:keyli1eed3:key3:val3:keyi2eeee4:test"))
	// scan := bufio.NewScanner(strings.NewReader("ld3:keyi1234eed3:key3:valee"))

	parser.Main(*scan)
}
