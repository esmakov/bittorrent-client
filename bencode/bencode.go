package bencode

import (
	"errors"
	"fmt"
)

func Bencode(data any) (string, error) {
	s := ""
	switch t := data.(type) {
	case string:
		s += bencodeString(t)
	case int:
		s += bencodeInt(t)
	case map[string]any:
		res, err := bencodeDict(t)
		if err != nil {
			return "", err
		}
		s += res
	case []any:
		res, err := bencodeList(t)
		if err != nil {
			return "", err
		}
		s += res
	default:
		return "", errors.New("Could not extract type out of data to be bencoded")
	}
	return s, nil
}

func bencodeString(s string) string {
	return fmt.Sprintf("%d", len(s)) + ":" + s
}

func bencodeInt(i int) string {
	return "i" + fmt.Sprintf("%d", i) + "e"
}

func bencodeDict(d map[string]any) (string, error) {
	s := "d"
	for k, v := range d {
		s += bencodeString(k)
		res, err := Bencode(v)
		if err != nil {
			return "", err
		}
		s += res
	}
	s += "e"
	return s, nil
}

func bencodeList(l []any) (string, error) {
	s := "l"
	for _, v := range l {
		res, err := Bencode(v)
		if err != nil {
			return "", err
		}
		s += res
	}
	s += "e"
	return s, nil
}
