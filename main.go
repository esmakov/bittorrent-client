package main

import (
	"bufio"
	"fmt"
	"os"
	"reflect"
	"strconv"
	"strings"
)

type btTokenType string

const (
    btNum btTokenType = "btNum"
    btStr = "btStr"
    btListStart = "btListStart"
    btListEnd = "btListEnd"
    btDictStart = "btListEnd"
    btDictEnd = "btDictEnd"
)

type btToken struct {
    tokenType  btTokenType
    lexeme string
    literal any
    line int
    indentLevel int
}

func die(e error) {
    if e != nil {
        panic(e)
    }
}

func main() {
    if len(os.Args) != 2 {
        die(fmt.Errorf("USAGE: bittorrent-client [.torrent FILENAME]"))
    }
    fh, err := os.Open(os.Args[1])
    die(err)
    defer fh.Close()

    // rdr := bufio.NewReader(fh)
    // scan := bufio.NewScanner(fh)
    scan := bufio.NewScanner(strings.NewReader("l4:spam4:eggsl5:bagell3:loxeee"))
    var tokenList = []btToken{}
    line := 0
    indentLevel := 0
    var lastOpenedType btTokenType

    tokenizer := func(data []byte, atEOF bool) (advance int, token []byte, err error) {
        for i := 0; i < len(data); i++ {
            switch c := data[i]; c {
            case '\n':
                line++
                fallthrough
            case ' ':
                fallthrough
            case '\t':
                return 1, data[:i], nil
            case 'l':
                lastOpenedType = btListStart
                t := btToken{btListStart, "", "", line, indentLevel}
                tokenList = append(tokenList, t)
                indentLevel++
                return 1, data[:i], nil
            case 'd':
                indentLevel++
                lastOpenedType = btDictStart
                t := btToken{btDictStart, "", "", line, indentLevel}
                tokenList = append(tokenList, t)
                return 1, data[:i], nil
            case 'i':
                curr := i + 1 // Consume i
                tokenStartIdx := curr
                for data[curr] != 'e' {
                    curr++
                }
                token := data[tokenStartIdx:curr]
                lexeme := string(token)

                numVal, err := strconv.Atoi(lexeme)
                die(err)

                t := btToken{btNum, lexeme, numVal, line, indentLevel}
                tokenList = append(tokenList, t)
                return curr + 1, token, nil
            case 'e':
                // fmt.Println("saw an E with", string(data), "remaining")
                indentLevel--;
                switch lastOpenedType {
                case "btListStart":
                    t := btToken{btListEnd, "", "", line, indentLevel}
                    tokenList = append(tokenList, t)
                    return 1, data[:i], nil
                 case "btDictStart": 
                    t := btToken{btDictEnd, "", "", line, indentLevel}
                    tokenList = append(tokenList, t)
                    return 1, data[:i], nil
                default:
                    fmt.Println("couldn't match type", reflect.TypeOf(lastOpenedType).String())
                }
            default:
                if c >= '0' && c <= '9' {
                    // fmt.Println("saw a string")
                    curr := i
                    for data[curr] != ':' {
                        curr++
                    }
                    strLen, err := strconv.Atoi(string(data[:curr]))
                    die(err)

                    tokenStartIdx := curr + 1
                    tokenEndIdx := tokenStartIdx + strLen

                    token := data[tokenStartIdx:tokenEndIdx]
                    lexeme := string(token)
                    
                    t := btToken{btStr, lexeme, lexeme, line, indentLevel}
                    tokenList = append(tokenList, t)
                    return tokenEndIdx - i, token, nil
                } else {
                    fmt.Println("Unrecognized token type")
                    return i + 1, data[:i], nil
                }
            }
        }

        if !atEOF {
            return 0, nil, nil
        }

        return 0, data, bufio.ErrFinalToken
    }
    scan.Split(tokenizer)
    for scan.Scan() {
    }

    for _, v := range tokenList{
        for i := v.indentLevel; i > 0; i-- {
            fmt.Print("\t")
        }
        fmt.Printf("%v\n", v)
    }
}
