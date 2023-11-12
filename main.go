package main

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
)

type btTokenType string

const (
    btNum btTokenType = "btNum"
    btStr = "btStr"
    btListStart = "btListStart"
    btListEnd = "btListEnd"
    btDictStart = "btDictStart"
    btDictKey = "btDictKey"
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

// Credit: Michael Green
// https://stackoverflow.com/questions/28541609/looking-for-reasonable-stack-implementation-in-golang
type stack[T any] struct {
    Push func(T)
    Pop func() T
    Peek func() T
    Length func() int
}

func Stack[T any]() stack[T] {
    slice := make([]T, 0)
    return stack[T]{
        Push: func(i T) {
            slice = append(slice, i)
        },
        Pop: func() T {
            if len(slice) == 0 {
                return *new(T)
            }
            res := slice[len(slice)-1]
            slice = slice[:len(slice)-1]
            return res
        },
        Peek: func() T {
            if len(slice) == 0 {
                return *new(T)
            }

            res := slice[len(slice)-1]
            return res
            
        },
        Length: func() int {
            return len(slice)
        },
    }
}

func main() {
    if len(os.Args) != 2 {
        die(fmt.Errorf("USAGE: bittorrent-client [*.torrent]"))
    }
    fh, err := os.Open(os.Args[1])
    die(err)
    defer fh.Close()

    // scan := bufio.NewScanner(fh)
    scan := bufio.NewScanner(strings.NewReader("d3:keyli1e3:stre10:anotherkeyd3:keyli3ei4ee3:key3:valeeld3:keyli1eed3:key3:val3:keyi2eeee"))
    // scan := bufio.NewScanner(strings.NewReader("ld3:keyi1234eed3:key3:valee"))

    var tokenList = []btToken{}
    line := 0
    indentLevel := 0
    shouldExpectDictKey := false
    compoundTypesEncountered := Stack[btToken]()

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
                t := btToken{btListStart, "l", "", line, indentLevel}
                compoundTypesEncountered.Push(t)
                tokenList = append(tokenList, t)
                indentLevel++
                return 1, data[:i], nil
            case 'd':
                t := btToken{btDictStart, "d", "", line, indentLevel}
                compoundTypesEncountered.Push(t)
                tokenList = append(tokenList, t)
                indentLevel++
                shouldExpectDictKey = true
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

                if compoundTypesEncountered.Peek().tokenType == "btDictStart" {
                    // This number was a value in a dict
                    shouldExpectDictKey = true
                }

                t := btToken{btNum, lexeme, numVal, line, indentLevel}
                tokenList = append(tokenList, t)
                return curr + 1, token, nil
            case 'e':
                indentLevel--

                switch compoundTypesEncountered.Pop().tokenType {
                case "btListStart":
                    if compoundTypesEncountered.Peek().tokenType == "btDictStart" {
                        // This list was a value in a dict
                        shouldExpectDictKey = true
                    }

                    t := btToken{btListEnd, "e", "", line, indentLevel}
                    tokenList = append(tokenList, t)
                    return 1, data[:i], nil
                 case "btDictStart": 
                        if compoundTypesEncountered.Peek().tokenType == "btDictStart" {
                            shouldExpectDictKey = true
                        }

                        t := btToken{btDictEnd, "", "", line, indentLevel}
                        tokenList = append(tokenList, t)
                        return 1, data[:i], nil
                default:
                    die(fmt.Errorf("Couldn't match type: %v\n", compoundTypesEncountered.Peek()))
                }
            default:
                if c >= '0' && c <= '9' {
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
                    var t btToken

                    if compoundTypesEncountered.Peek().tokenType == "btDictStart" {
                        if shouldExpectDictKey {
                            t = btToken{btDictKey, lexeme, lexeme, line, indentLevel}
                            shouldExpectDictKey = false
                        } else {
                            t = btToken{btStr, lexeme, lexeme, line, indentLevel}
                            shouldExpectDictKey = true
                        }
                    } else {
                        t = btToken{btStr, lexeme, lexeme, line, indentLevel}
                    }

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

    fmt.Println("--------------------------------")

    for scan.Scan() {
    }

    for _, v := range tokenList{
        for i := v.indentLevel; i > 0; i-- {
            fmt.Print("\t")
        }
        fmt.Println(v)
    }
}
