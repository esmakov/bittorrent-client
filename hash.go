package main

import (
	"crypto/sha1"
	"io"
	"log"
    "fmt"
)

func Hash(r io.Reader) string {
	h := sha1.New()
	if _, err := io.Copy(h, r); err != nil {
		log.Fatal(err)
	}

	hash := h.Sum(nil)

    hashString := ""
    for _, v := range hash {
        hashString += fmt.Sprintf("%x", v)
    }
    return hashString
}
