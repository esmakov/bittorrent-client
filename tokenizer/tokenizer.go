package tokenizer

import (
	"bufio"
	"fmt"
	"strconv"
)

type btTokenType string

const (
	btNum       btTokenType = "btNum"
	btStr                   = "btStr"
	btListStart             = "btListStart"
	btListEnd               = "btListEnd"
	btDictStart             = "btDictStart"
	btDictKey               = "btDictKey"
	btDictEnd               = "btDictEnd"
)

type btToken struct {
	tokenType   btTokenType
	lexeme      string
	literal     any
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

var (
	indentLevel              = 0
	shouldExpectDictKey      = false
	compoundTypesEncountered = newStack[btToken]()
	tokenList                = []btToken{}
)

/*
Assumes that the provided scanner is for a correctly-formatted .torrent metainfo file.
*/
func Parse(scan bufio.Scanner) {
	scan.Split(splitFunc)

	fmt.Println("--------------------------------")

	for scan.Scan() {
	}

	for _, v := range tokenList {
		for i := v.indentLevel; i > 0; i-- {
			fmt.Print("\t")
		}
		fmt.Println(v.tokenType, v.lexeme)
	}
}

func splitFunc(data []byte, atEOF bool) (advance int, token []byte, err error) {
	for i := 0; i < len(data); i++ {
		switch c := data[i]; c {
		case 'l':
			t := btToken{btListStart, "l", nil, indentLevel}
			compoundTypesEncountered.Push(t)
			tokenList = append(tokenList, t)
			indentLevel++
			return 1, data[:i], nil
		case 'd':
			t := btToken{btDictStart, "d", nil, indentLevel}
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

			t := btToken{btNum, lexeme, numVal, indentLevel}
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

				t := btToken{btListEnd, "e", nil, indentLevel}
				tokenList = append(tokenList, t)
				return 1, data[:i], nil
			case "btDictStart":
				if compoundTypesEncountered.Peek().tokenType == "btDictStart" {
					shouldExpectDictKey = true
				}

				t := btToken{btDictEnd, "e", nil, indentLevel}
				tokenList = append(tokenList, t)
				return 1, data[:i], nil
			default:
				die(fmt.Errorf("Unrecognized token type\n"))
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
						t = btToken{btDictKey, lexeme, lexeme, indentLevel}
						shouldExpectDictKey = false
					} else {
						// This string is a dict value
						t = btToken{btStr, lexeme, lexeme, indentLevel}
						shouldExpectDictKey = true
					}
				} else {
					// Not in a dict
					t = btToken{btStr, lexeme, lexeme, indentLevel}
				}

				tokenList = append(tokenList, t)
				return tokenEndIdx - i, token, nil
			} else {
				die(fmt.Errorf("Unrecognized token type\n"))
				return i + 1, data[:i], nil
			}
		}
	}

	if !atEOF {
		return 0, nil, nil
	}

	return 0, data, bufio.ErrFinalToken
}
