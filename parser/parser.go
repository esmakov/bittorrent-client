package parser

import (
	"bufio"
	"errors"
	"io"
	"log"
	"os"
	"strconv"

	"github.com/esmakov/bittorrent-client/hash"
	"github.com/esmakov/bittorrent-client/stack"
)

func die(e error) {
	if e != nil {
		log.Fatalln(e)
	}
}

type btTokenKinds int

const (
	btZeroVal btTokenKinds = iota
	btNum
	btStr
	btListStart
	btListEnd
	btDictStart
	btDictKey
	btDictEnd
)

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
	literal   any
}

type parser struct {
	tokenList             []btToken
	fileIdx               int
	infoDictStartIdx      int
	infoDictEndIdx        int
	indentLevel           int
	shouldExpectDictKey   bool
	unclosedCompoundTypes stack.Stack[btToken]
	splitFun              bufio.SplitFunc
}

func New() parser {
	stack := stack.NewStack[btToken]()
	return parser{
		unclosedCompoundTypes: stack,
	}
}

/*
Assumes that the provided file is a correctly-formatted metainfo file.
*/
func (p *parser) ParseMetaInfoFile(file string) (map[string]any, []byte) {
	fd, err := os.Open(file)
	die(err)
	defer fd.Close()

	p.bdecode(fd)

	// Extract info hash verbatim
	fileBytes, _ := os.ReadFile(file)
	if p.infoDictEndIdx == 0 {
		log.Fatalln("Didn't find info dict ending")
	}
	infoDictBytes := fileBytes[p.infoDictStartIdx : p.infoDictEndIdx+1]
	infoHash := hash.HashSHA1(infoDictBytes)

	// Ignore start of dict
	if _, err := p.consumeToken(); err != nil {
		log.Fatalln(err)
	}

	// Recursively parse the tokens in the list
	return p.parseDict(), infoHash
}

// While scanning, builds up the token list and populates indices around "info" dictionary
func (p *parser) bdecode(r io.Reader) {
	scan := bufio.NewScanner(r)
	// TODO: Use smaller buffer
	const START_BUFFER_SIZE = 512 * 1024
	const MAX_BUFFER_SIZE = 1024 * 1024

	scan.Buffer(make([]byte, START_BUFFER_SIZE), MAX_BUFFER_SIZE)
	scan.Split(p.splitFunc)

	for scan.Scan() {
	}
}

func (p *parser) ParseResponse(r io.Reader) map[string]any {
	p.bdecode(r)

	// Ignore start of dict
	if _, err := p.consumeToken(); err != nil {
		log.Fatalln(err)
	}
	return p.parseDict()
}

func (p *parser) parse(t btToken) any {
	switch t.tokenKind {
	case btNum:
		fallthrough
	case btStr:
		return t.literal
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
		k, v, err := p.parseEntry()
		if err != nil {
			break
		}
		m[k] = v
	}
	return m
}

func (p *parser) parseEntry() (k string, v any, e error) {
	for i := 0; i <= 1; i++ {
		t, err := p.consumeToken()
		if err != nil {
			e = err
			return
		}
		if t.tokenKind == btDictEnd {
			e = errors.New("End of dict")
			return
		}

		if t.tokenKind == btDictKey {
			k = t.lexeme
		} else {
			// Value can be of any bencoded type
			v = p.parse(t)
		}
	}
	return
}

func (p parser) prettyPrint(t btToken) {
	// TODO: Enable with command line arg
	// for i := p.indentLevel; i > 0; i-- {
	// 	fmt.Print("\t")
	// }
	// fmt.Println(t.tokenKind, t.lexeme)
}

func (p *parser) splitFunc(data []byte, atEOF bool) (bytesToAdvance int, token []byte, err error) {
	for i := 0; i < len(data); i++ {
		switch c := data[i]; c {
		case 'l':
			t := btToken{btListStart, "l", nil}

			p.tokenList = append(p.tokenList, t)
			p.unclosedCompoundTypes.Push(t)
			p.prettyPrint(t)
			p.indentLevel++

			bytesToAdvance = 1
			p.fileIdx += bytesToAdvance
			token = data[:bytesToAdvance]
			return
		case 'd':
			p.shouldExpectDictKey = true

			t := btToken{btDictStart, "d", nil}

			if p.fileIdx == p.infoDictStartIdx && p.fileIdx != 0 {
				t.lexeme = "INFODICT"
			}

			p.tokenList = append(p.tokenList, t)
			p.unclosedCompoundTypes.Push(t)
			p.prettyPrint(t)
			p.indentLevel++

			bytesToAdvance = 1
			p.fileIdx += bytesToAdvance
			token = data[:bytesToAdvance]
			return
		case 'e':
			switch next := p.unclosedCompoundTypes.Pop(); next.tokenKind {
			case btListStart:
				if p.unclosedCompoundTypes.Peek().tokenKind == btDictStart {
					// This list was a value in a dict
					p.shouldExpectDictKey = true
				}

				t := btToken{btListEnd, "e", nil}
				p.tokenList = append(p.tokenList, t)
				p.indentLevel--
				p.prettyPrint(t)

				bytesToAdvance = 1
				p.fileIdx += bytesToAdvance
				token = data[:bytesToAdvance]
				return
			case btDictStart:
				if p.unclosedCompoundTypes.Peek().tokenKind == btDictStart {
					// This dict was a value in an outer dict
					p.shouldExpectDictKey = true
				}

				if next.lexeme == "INFODICT" {
					p.infoDictEndIdx = p.fileIdx
				}

				t := btToken{btDictEnd, "e", nil}
				p.tokenList = append(p.tokenList, t)
				p.indentLevel--
				p.prettyPrint(t)

				bytesToAdvance = 1
				p.fileIdx += bytesToAdvance
				token = data[:bytesToAdvance]
				return
			default:
				err = errors.New("Unrecognized token type")
				return
			}
		case 'i':
			bytesToAdvance = 1 // Consume i
			tokenStartIdx := bytesToAdvance

			for data[bytesToAdvance] != 'e' {
				bytesToAdvance++
			}

			token = data[tokenStartIdx:bytesToAdvance]
			lexeme := string(token)

			numVal, e := strconv.Atoi(lexeme)
			if e != nil {
				err = e
				return
			}

			if p.unclosedCompoundTypes.Peek().tokenKind == btDictStart {
				// This number was a value in a dict
				p.shouldExpectDictKey = true
			}

			t := btToken{btNum, lexeme, numVal}
			p.tokenList = append(p.tokenList, t)
			p.prettyPrint(t)

			bytesToAdvance++ // Consume e
			p.fileIdx += bytesToAdvance
			return
		default:
			// Bencoded string
			if c >= '0' && c <= '9' {
				for data[bytesToAdvance] != ':' {
					bytesToAdvance++
				}
				strLen, e := strconv.Atoi(string(data[:bytesToAdvance]))
				if e != nil {
					err = e
					return
				}

				tokenStartIdx := bytesToAdvance + 1
				bytesToAdvance += strLen + 1
				p.fileIdx += bytesToAdvance

				token = data[tokenStartIdx:bytesToAdvance]
				lexeme := string(token)

				// Assume not in a dict
				t := btToken{btStr, lexeme, lexeme}

				if p.unclosedCompoundTypes.Peek().tokenKind == btDictStart {
					if p.shouldExpectDictKey {
						t.tokenKind = btDictKey
						p.shouldExpectDictKey = false

						if t.lexeme == "info" {
							p.infoDictStartIdx = p.fileIdx
						}
					} else {
						// This string is a dict value
						p.shouldExpectDictKey = true
					}
				}

				p.tokenList = append(p.tokenList, t)
				p.prettyPrint(t)

				return
			} else {
				err = errors.New("Unrecognized token type")
				return
			}
		}
	}

	if !atEOF {
		// Signal to scanner to try again and process more input
		return 0, nil, nil
	}

	return 0, data, bufio.ErrFinalToken
}
