package parser

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strconv"
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
	tokenType   btTokenType
	lexeme      string
	literal     any
}

func die(e error) {
	if e != nil {
		panic(e)
	}
}

// Credit: Michael Green
// https://stackoverflow.com/questions/28541609/looking-for-reasonable-stack-implementation-in-golang
type stack[T any] struct {
	Push   func(T)
	Pop    func() T
	Peek   func() T
	Length func() int
}

func newStack[T any]() stack[T] {
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

var tokenList = []btToken{}

func bencode() string {

    return ""
}

/*
Assumes that the provided scanner is for a correctly-formatted .torrent metainfo file.
*/
func ParseMetaInfo(fh *os.File) map[string]any {
	fmt.Println("--------------------------------")
	scan := bufio.NewScanner(fh)
	scan.Split(splitFunc)

	// Build up the list
	for scan.Scan() {
        // fmt.Println(scan.Text())
	}


	// Shrink it down again
	_, err := consume()
	die(err)

	return parseDict()
}

func parse(t btToken) any {
	switch v := t.literal.(type) {
	case string:
		// fmt.Println("string", v)
		return v
	case int:
		// fmt.Println("int", v)
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
		} else {
			// Value can be of any bencoded type
			v = parse(t)
		}
	}
	return
}

var (
	indentLevel           = 0
	shouldExpectDictKey   = false
	unclosedCompoundTypes = newStack[btToken]()
)
func prettyPrint(t btToken) {
		for i := indentLevel; i > 0; i-- {
		    fmt.Print("\t")
		}
		fmt.Println(t.tokenType, t.lexeme)
}

func splitFunc(data []byte, atEOF bool) (advance int, token []byte, err error) {
	for i := 0; i < len(data); i++ {
		switch c := data[i]; c {
		case 'l':
			t := btToken{btListStart, "l", nil}
			unclosedCompoundTypes.Push(t)
			tokenList = append(tokenList, t)
			indentLevel++
            prettyPrint(t)
			return 1, data[:i], nil
		case 'd':
			t := btToken{btDictStart, "d", nil}
			unclosedCompoundTypes.Push(t)
			tokenList = append(tokenList, t)
			indentLevel++
			shouldExpectDictKey = true
            prettyPrint(t)
			return 1, data[:i], nil
        case 'e':
            indentLevel--

            switch unclosedCompoundTypes.Pop().tokenType {
            case btListStart:
                if unclosedCompoundTypes.Peek().tokenType == btDictStart {
                    // This list was a value in a dict
                    shouldExpectDictKey = true
                }

                t := btToken{btListEnd, "e", nil}
                tokenList = append(tokenList, t)
                prettyPrint(t)
                return 1, data[:i], nil
            case btDictStart:
                if unclosedCompoundTypes.Peek().tokenType == btDictStart {
                    shouldExpectDictKey = true
                }

                t := btToken{btDictEnd, "e", nil}
                tokenList = append(tokenList, t)
                prettyPrint(t)
                return 1, data[:i], nil
            default:
                die(fmt.Errorf("Unrecognized token type\n"))
            }
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

			if unclosedCompoundTypes.Peek().tokenType == btDictStart {
				// This number was a value in a dict
				shouldExpectDictKey = true
			}

			t := btToken{btNum, lexeme, numVal}
			tokenList = append(tokenList, t)
            prettyPrint(t)
			return curr + 1, token, nil
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

				// Assume not in a dict
                t := btToken{btStr, lexeme, lexeme}

				if unclosedCompoundTypes.Peek().tokenType == btDictStart {
					if shouldExpectDictKey {
                        t.tokenType = btDictKey
						shouldExpectDictKey = false
					} else {
						// This string is a dict value
						shouldExpectDictKey = true
					}
				}

				tokenList = append(tokenList, t)
                prettyPrint(t)
				return tokenEndIdx - i, token, nil
			} else {
				die(fmt.Errorf("Unrecognized token type\n"))
			}
		}
	}

	if !atEOF {
		return 0, nil, nil
	}

	return 0, data, bufio.ErrFinalToken
}
