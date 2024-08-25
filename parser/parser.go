// Contains functionality related to parsing tokens and taking substrings from the metainfo file.
package parser

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/esmakov/bittorrent-client/hash"
	"github.com/esmakov/bittorrent-client/stack"
)

type btTokenKinds int

const (
	btZeroVal btTokenKinds = iota
	// https://wiki.theory.org/BitTorrentSpecification#Bencoding
	// NOTE: The maximum number of bit of this integer is unspecified, but to handle it as a signed 64bit integer is mandatory to handle "large files" aka .torrent for more that 4Gbyte.
	btNum
	btStr
	btListStart
	btListEnd
	btDictStart
	btDictKey
	btDictEnd
)

var ErrInvalidToken = errors.New("Invalid token type")

func (t btTokenKinds) String() string {
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
	return "btZeroVal"
}

type btToken struct {
	tokenKind btTokenKinds
	lexeme    string
	value     any
}

type parser struct {
	cursor                     int
	infoDictStartIdx           int
	infoDictEndIdx             int
	piecesStartIdx             int
	indentLevel                int
	expectNextTokenToBeDictKey bool
	shouldPrettyPrint          bool
	splitFun                   bufio.SplitFunc
	unclosedCompoundTypes      stack.Stack[btToken]
	tokenList                  []btToken
}

func New(shouldPrettyPrint bool) parser {
	return parser{
		unclosedCompoundTypes: stack.NewStack[btToken](),
		shouldPrettyPrint:     shouldPrettyPrint,
	}
}

// NOTE: Assumes that the provided file is correctly encoded
func (p *parser) ParseMetaInfoFile(fileBytes []byte) (topLevelMap map[string]any, infoHash []byte, e error) {
	if e = p.bDecode(fileBytes); e != nil {
		return
	}

	// Extract "info dict" substring
	if p.infoDictStartIdx == 0 || p.infoDictEndIdx == 0 {
		e = errors.New("Didn't find info dict start or end")
		return
	}
	if p.infoDictEndIdx > len(fileBytes) {
		e = errors.New("Tried to take a substring out of range of the file")
		return
	}

	infoDictBytes := fileBytes[p.infoDictStartIdx : p.infoDictEndIdx+1]
	infoHash, e = hash.HashSHA1(infoDictBytes)
	if e != nil {
		return
	}

	// Skip start of dict
	if _, e = p.consumeToken(); e != nil {
		return
	}

	topLevelMap = p.parseDict()
	return
}

func (p *parser) ParseTrackerResponse(bytes []byte) (map[string]any, error) {
	if err := p.bDecode(bytes); err != nil {
		return nil, err
	}

	// Ignore start of dict
	if _, err := p.consumeToken(); err != nil {
		return nil, err
	}

	return p.parseDict(), nil
}

func (p *parser) parse(t btToken) any {
	switch t.tokenKind {
	case btNum:
		fallthrough
	case btStr:
		return t.value
	case btDictStart:
		return p.parseDict()
	case btListStart:
		return p.parseList()
	default:
		return nil
	}
}

func (p *parser) consumeToken() (btToken, error) {
	if len(p.tokenList) == 0 {
		return *new(btToken), errors.New("End of list")
	}

	t := p.tokenList[0]
	p.tokenList = p.tokenList[1:]
	return t, nil
}

func (p *parser) parseList() []any {
	s := make([]any, 0)
	for {
		t, err := p.consumeToken()
		if err != nil || t.tokenKind == btListEnd {
			break
		}
		s = append(s, p.parse(t))
	}
	return s
}

func (p *parser) parseDict() map[string]any {
	m := make(map[string]any)
	for {
		k, v, err := p.parseDictEntry()
		if err != nil {
			break
		}
		m[k] = v
	}
	return m
}

func (p *parser) parseDictEntry() (k string, v any, e error) {
	for range 2 {
		t, err := p.consumeToken()
		if err != nil {
			e = err
			return
		}
		switch t.tokenKind {
		case btDictEnd:
			e = errors.New("End of dict")
			return
		case btDictKey:
			k = t.lexeme
		default:
			// Value can be of any bencoded type
			v = p.parse(t)
		}
	}
	return
}

func (p parser) prettyPrint(t btToken) {
	if !p.shouldPrettyPrint {
		return
	}

	if len(t.lexeme) > 200 {
		fmt.Printf("%v %v %q\n", strings.Repeat("\t", p.indentLevel), t.tokenKind, t.lexeme[:200]+"...")
	} else {
		fmt.Printf("%v %v %q\n", strings.Repeat("\t", p.indentLevel), t.tokenKind, t.lexeme)
	}
}

// Parses a bencoded sequence of bytes.
// As a side effect, bDecode also builds up the token list and populates indices around  the "info" dictionary, so it can be extracted from the file in a later pass.
func (p *parser) bDecode(data []byte) error {
	for len(data) > 0 {
		switch c := data[0]; c {
		case 'l':
			t := btToken{btListStart, "l", nil}
			p.tokenList = append(p.tokenList, t)
			p.unclosedCompoundTypes.Push(t)

			p.prettyPrint(t)
			p.indentLevel++

			p.cursor++
			data = data[1:]
		case 'd':
			p.expectNextTokenToBeDictKey = true

			t := btToken{btDictStart, "d", nil}

			if p.cursor == p.infoDictStartIdx && p.cursor != 0 {
				t.lexeme = "INFODICT"
			}

			p.tokenList = append(p.tokenList, t)
			p.unclosedCompoundTypes.Push(t)

			p.prettyPrint(t)
			p.indentLevel++

			p.cursor++
			data = data[1:]
		case 'e':
			switch next := p.unclosedCompoundTypes.Pop(); next.tokenKind {
			case btListStart:
				if p.unclosedCompoundTypes.Peek().tokenKind == btDictStart {
					// This list was a value in a dict
					p.expectNextTokenToBeDictKey = true
				}

				t := btToken{btListEnd, "e", nil}
				p.tokenList = append(p.tokenList, t)

				p.indentLevel--
				p.prettyPrint(t)

				p.cursor++
				data = data[1:]
			case btDictStart:
				if p.unclosedCompoundTypes.Peek().tokenKind == btDictStart {
					// This dict was a value in an outer dict
					p.expectNextTokenToBeDictKey = true
				}

				if next.lexeme == "INFODICT" {
					p.infoDictEndIdx = p.cursor
				}

				t := btToken{btDictEnd, "e", nil}
				p.tokenList = append(p.tokenList, t)

				p.indentLevel--
				p.prettyPrint(t)

				p.cursor++
				data = data[1:]
			default:
				return errors.New("Unrecognized token type")
			}
		case 'i':
			idxOfE := bytes.IndexByte(data, 'e')

			lexeme := string(data[1:idxOfE]) // Consume leading 'i'

			numVal, e := strconv.Atoi(lexeme)
			if e != nil {
				return e
			}

			if p.unclosedCompoundTypes.Peek().tokenKind == btDictStart {
				// This number was a value in a dict
				p.expectNextTokenToBeDictKey = true
			}

			t := btToken{btNum, lexeme, numVal}
			p.tokenList = append(p.tokenList, t)

			p.prettyPrint(t)

			p.cursor += idxOfE + 1
			data = data[idxOfE+1:] // Consume trailing 'e'
		default:
			// Bencoded string of the form '3:cat'
			if !(c >= '0' && c <= '9') {
				return ErrInvalidToken
			}

			idxOfColon := bytes.IndexByte(data, ':')
			strLen, err := strconv.Atoi(string(data[:idxOfColon]))
			if err != nil {
				return err
			}

			tokenStartIdx := idxOfColon + 1
			tokenEndIdx := tokenStartIdx + strLen

			p.cursor += tokenEndIdx

			lexeme := string(data[tokenStartIdx:tokenEndIdx])

			if lexeme == "pieces" {
				p.piecesStartIdx = tokenStartIdx
			}

			// Assume not in a dict
			t := btToken{btStr, lexeme, lexeme}
			if p.unclosedCompoundTypes.Peek().tokenKind == btDictStart {
				if p.expectNextTokenToBeDictKey {
					t.tokenKind = btDictKey
					p.expectNextTokenToBeDictKey = false

					if t.lexeme == "info" {
						p.infoDictStartIdx = p.cursor
					}
				} else {
					// This string is a dict value
					p.expectNextTokenToBeDictKey = true
				}
			}
			p.tokenList = append(p.tokenList, t)

			p.prettyPrint(t)

			data = data[tokenEndIdx:]
		}
	}

	if len(data) > 0 {
		return errors.New("Failed to parse entire file")
	} else {
		return nil
	}
}
