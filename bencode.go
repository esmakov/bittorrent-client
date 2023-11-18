package main

import (
    "fmt"
    "log"
)

func Bencode(data any) (s string) {
        switch t := data.(type) {
        case string:
            s += bencodeString(t)
        case int:
            s += bencodeInt(t)
        case map[string]any:
            s += bencodeDict(t)
        case []any:
            s += bencodeList(t)
        default:
            log.Fatalln("Could not assert type")
        }
	return
}

func bencodeString(s string) string {
	return fmt.Sprintf("%d", len(s)) + ":" + s
}

func bencodeInt(i int) string {
	return "i" + fmt.Sprintf("%d", i) + "e"
}

func bencodeDict(d map[string]any) (s string) {
    s += "d"
    for k, v := range d {
        s += bencodeString(k)
        s += Bencode(v)
    }
    s += "e"
    return
}

func bencodeList(l []any) (s string) {
    s += "l"
    for _, v := range l {
        s += Bencode(v)
    }
    s += "e"
    return
}
