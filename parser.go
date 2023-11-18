package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"log"
	"net/url"
	"os"
	"strconv"
	"strings"
)

type btTokenType int

const (
	btNum btTokenType = iota
	btStr
	btListStart
	btListEnd
	btDictStart
	btDictKey
	btDictEnd
)

func (t btTokenType) String() string {
	switch t {
	case btNum:
		return "btNum"
	case btStr:
		return "btStr"
	case btListStart:
		return "btListStart"
	case btListEnd:
		return "btListEnd"
	case btDictStart:
		return "btDictStart"
	case btDictKey:
		return "btDictKey"
	case btDictEnd:
		return "btDictEnd"
	}
	return "unknown"
}

type btToken struct {
	tokenType btTokenType
	lexeme    string
	literal   any
}

var tokenList = []btToken{}

/*
Assumes that the provided file is a correctly-formatted metainfo file.
*/
func ParseMetaInfoFile(file string) map[string]any {
	fd, err := os.Open(file)
	die(err)
	defer fd.Close()

	// Build up the token list and populate indices around "info" dictionary
	bdecode(fd)

    fileBytes, _ := os.ReadFile(file)
    infoDictBytes := fileBytes[infoDictStartIdx:infoDictEndIdx + 1]
    infoHash := Hash(bufio.NewReader(strings.NewReader(string(infoDictBytes))))
    escapedInfoHash := url.PathEscape(infoHash)
    fmt.Println("info_hash:", escapedInfoHash)

	// Shrink it down again
	if _, err := consume(); err != nil {
        log.Fatalln(err)
    }

	return parseDict()
}

func bdecode(r io.Reader) {
	fmt.Println("--------------------------------")
	scan := bufio.NewScanner(r)
	scan.Split(splitFunc)

	for scan.Scan() {}
}

func parse(t btToken) any {
	switch v := t.literal.(type) {
	case string:
		return v
	case int:
		return v
	default:
		// fmt.Println("no match:", t.lexeme)
	}

	switch t.tokenType {
	// case "btNum":
	//     fallthrough
	// case "btStr":
	//     return t.literal
	case btDictStart:
		return parseDict()
	case btListStart:
		return parseList()
	default:
		return nil
	}
}

func consume() (btToken, error) {
	if len(tokenList) == 0 {
		return *new(btToken), errors.New("End of list")
	}
	t := tokenList[0]
	tokenList = tokenList[1:]
	return t, nil
}

func parseList() []any {
	s := make([]any, 0)
	for {
		t, err := consume()
		if err != nil || t.tokenType == btListEnd {
			break
		}
		s = append(s, parse(t))
	}
	return s
}

func parseDict() map[string]any {
	m := make(map[string]any)
	for {
		k, v, err := parseEntry()
		if err != nil {
			break
		}
		m[k] = v
	}
	return m
}

func parseEntry() (k string, v any, e error) {
	for i := 0; i <= 1; i++ {
		t, err := consume()
		if err != nil {
			e = err
			return
		}
		if t.tokenType == btDictEnd {
			e = errors.New("End of dict")
			return
		}

		if t.tokenType == btDictKey {
			k = t.lexeme
			if k == "info" {

			}
		} else {
			// Value can be of any bencoded type
			v = parse(t)
		}
	}
	return
}

var (
	idx                   = 0
	infoDictStartIdx      = 0
	infoDictEndIdx        = 0
	indentLevel           = 0
	shouldExpectDictKey   = false
	unclosedCompoundTypes = newStack[btToken]()
)

func prettyPrint(t btToken) {
	// for i := indentLevel; i > 0; i-- {
	// 	fmt.Print("\t")
	// }
	// fmt.Println(t.tokenType, t.lexeme)
}

func splitFunc(data []byte, atEOF bool) (advance int, token []byte, err error) {
	for i := 0; i < len(data); i++ {
		switch c := data[i]; c {
		case 'l':
			t := btToken{btListStart, "l", nil}

			tokenList = append(tokenList, t)
			unclosedCompoundTypes.Push(t)
			prettyPrint(t)
			indentLevel++

			advance = 1
			idx += advance
			token = data[:advance]
			return
		case 'd':
			shouldExpectDictKey = true

			t := btToken{btDictStart, "d", nil}

			if idx == infoDictStartIdx && idx != 0 {
				t.lexeme = "INFODICT"
			}

			tokenList = append(tokenList, t)
			unclosedCompoundTypes.Push(t)
			prettyPrint(t)
			indentLevel++

			advance = 1
			idx += advance
			token = data[:advance]
			return
		case 'e':
			switch next := unclosedCompoundTypes.Pop(); next.tokenType {
			case btListStart:
				if unclosedCompoundTypes.Peek().tokenType == btDictStart {
					// This list was a value in a dict
					shouldExpectDictKey = true
				}

				t := btToken{btListEnd, "e", nil}
				tokenList = append(tokenList, t)
				indentLevel--
				prettyPrint(t)

				advance = 1
				idx += advance
				token = data[:advance]
				return
			case btDictStart:
				if unclosedCompoundTypes.Peek().tokenType == btDictStart {
					// This dict was a value in an outer dict
					shouldExpectDictKey = true
				}

				if next.lexeme == "INFODICT" {
					infoDictEndIdx = idx
				}

				t := btToken{btDictEnd, "e", nil}
				tokenList = append(tokenList, t)
				indentLevel--
				prettyPrint(t)

				advance = 1
				idx += advance
				token = data[:advance]
				return
			default:
				err = errors.New("Unrecognized token type")
				return
			}
		case 'i':
			advance = 1 // Consume i
			tokenStartIdx := advance

			for data[advance] != 'e' {
				advance++
			}

			token = data[tokenStartIdx:advance]
			lexeme := string(token)

			numVal, e := strconv.Atoi(lexeme)
			if e != nil {
				err = e
				return
			}

			if unclosedCompoundTypes.Peek().tokenType == btDictStart {
				// This number was a value in a dict
				shouldExpectDictKey = true
			}

			t := btToken{btNum, lexeme, numVal}
			tokenList = append(tokenList, t)
			prettyPrint(t)

			advance++ // Consume e
			idx += advance
			return
		default:
			if c >= '0' && c <= '9' {
				for data[advance] != ':' {
					advance++
				}
				strLen, e := strconv.Atoi(string(data[:advance]))
				if e != nil {
					err = e
					return
				}

				tokenStartIdx := advance + 1
				advance += strLen + 1
				idx += advance

				token = data[tokenStartIdx:advance]
				lexeme := string(token)

				// Assume not in a dict
				t := btToken{btStr, lexeme, lexeme}

				if unclosedCompoundTypes.Peek().tokenType == btDictStart {
					if shouldExpectDictKey {
						t.tokenType = btDictKey
						shouldExpectDictKey = false
						if t.lexeme == "info" {
							infoDictStartIdx = idx
						}
					} else {
						// This string is a dict value
						shouldExpectDictKey = true
					}
				}

				tokenList = append(tokenList, t)
				prettyPrint(t)

				return
			} else {
				err = errors.New("Unrecognized token type")
				return
			}
		}
	}

	if !atEOF {
		return 0, nil, nil
	}

	return 0, data, bufio.ErrFinalToken
}
