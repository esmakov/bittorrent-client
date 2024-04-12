package torrent

import (
	"encoding/binary"
	"errors"
	"fmt"
	"slices"
)

type messageKinds int

const (
	choke messageKinds = iota
	unchoke
	interested
	uninterested
	have
	bitfield
	request
	piece
	cancel
	port
	keepalive
	handshake
	fragment
)

func (m messageKinds) String() string {
	switch m {
	case choke:
		return "choke"
	case unchoke:
		return "unchoke"
	case interested:
		return "interested"
	case uninterested:
		return "uninterested"
	case have:
		return "have"
	case bitfield:
		return "bitfield"
	case request:
		return "request"
	case piece:
		return "piece"
	case cancel:
		return "cancel"
	case port:
		return "port"
	case keepalive:
		return "keepalive"
	case handshake:
		return "handshake"
	default:
		return "unknown or fragment"
	}
}

// TODO: Move away from single type for all messages
type peerMessage struct {
	kind        messageKinds
	totalSize   int
	peerId      []byte
	pieceNum    int
	bitfield    []byte
	blockOffset int
	blockSize   int
	blockData   []byte
}

// Sometimes multiple messages will be read from the stream at once
func parseMultiMessage(buf, infoHash []byte) ([]peerMessage, error) {
	var msgList []peerMessage
	for i := 0; i < len(buf)-1; {
		// Chop off one message at a time
		msg, err := parseMessage(buf[i:], infoHash)
		if err != nil {
			return nil, err
		}
		msgList = append(msgList, msg)
		i += msg.totalSize
	}
	return msgList, nil
}

func parseMessage(buf, infoHash []byte) (msg peerMessage, e error) {
	handshakeStart := []byte{0x13, 0x42, 0x69, 0x74, 0x54}
	bufStart := buf[:len(handshakeStart)]
	if slices.Equal(bufStart, handshakeStart) {
		msg, e = parseAndVerifyHandshake(buf, infoHash)
		return
	}

	if len(buf) == 4 {
		msg.kind = keepalive
		msg.totalSize = 4
		return
	}

	lenBytes := binary.BigEndian.Uint32(buf[:4])
	lenVal := int(lenBytes)

	if lenVal > len(buf) || len(buf) < 4 {
		// Message needs to be reassembled from this packet and the next
		// fmt.Printf("Message is %v bytes long but we only have %v so far\n", lenVal, len(buf))
		msg.kind = fragment
		msg.totalSize = len(buf)
		return
	}

	messageKind := int(buf[4])
	msg.kind = messageKinds(messageKind)

	if msg.kind == have || msg.kind == request || msg.kind == piece || msg.kind == cancel {
		msg.pieceNum = int(binary.BigEndian.Uint32(buf[5:9]))
	}

	msg.totalSize = lenVal + 4 // Include length prefix

	if msg.totalSize > len(buf) {
		panic(fmt.Sprintf("Out of bounds of msg buffer: %v / %v", msg.totalSize, len(buf)))
	}

	switch msg.kind {
	case bitfield:
		msg.bitfield = buf[5:msg.totalSize]
	case request:
		msg.blockOffset = int(binary.BigEndian.Uint32(buf[9:13]))
		msg.blockSize = int(binary.BigEndian.Uint32(buf[13:msg.totalSize]))
	case piece:
		msg.blockOffset = int(binary.BigEndian.Uint32(buf[9:13]))
		msg.blockData = buf[13:msg.totalSize]
	}

	return
}

func parseAndVerifyHandshake(buf []byte, expectedInfoHash []byte) (peerMessage, error) {
	msg := peerMessage{}
	protocolLen := int(buf[0]) // Should be 19 or 0x13

	// protocolBytes := buf[1 : protocolLen+1]
	// reservedBytes := buf[protocolLen+1 : protocolLen+9]

	theirInfoHash := buf[protocolLen+9 : protocolLen+29]
	if !slices.Equal(theirInfoHash, expectedInfoHash) {
		return msg, errors.New("Peer did not respond with correct info hash")
	}

	peerId := buf[protocolLen+29 : protocolLen+49]

	msg.kind = handshake
	msg.totalSize = protocolLen + 49
	msg.peerId = peerId
	return msg, nil
}

// <pstrlen><pstr><reserved><info_hash><peer_id>
func createHandshakeMsg(infoHash []byte, peerId []byte) []byte {
	protocol := "BitTorrent protocol"
	protocolBytes := make([]byte, len(protocol))
	copy(protocolBytes, protocol)

	length := []byte{byte(len(protocol))}
	reserved := []byte{0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}

	return concatMultipleSlices([][]byte{length, protocolBytes, reserved, infoHash, peerId})
}

func createChokeMsg() []byte {
	return []byte{0x00, 0x00, 0x00, 0x01, 0x00}
}

func createUnchokeMsg() []byte {
	return []byte{0x00, 0x00, 0x00, 0x01, 0x01}
}

// <len=0001><id=2>
func createInterestedMsg() []byte {
	lenBytes := make([]byte, 4)
	binary.BigEndian.PutUint32(lenBytes, uint32(1))
	return concatMultipleSlices([][]byte{lenBytes, {0x02}})
}

// <len=0001+X><id=5><bitfield>
func createBitfieldMsg(bitfield []byte) []byte {
	lenBytes := make([]byte, 4)
	length := 1 + len(bitfield)
	binary.BigEndian.PutUint32(lenBytes, uint32(length))
	return concatMultipleSlices([][]byte{lenBytes, {0x05}, bitfield})
}

// <len=0013><id=6><index><begin><length>
func createRequestMsg(pieceNum, offset, length int) []byte {
	lengthAndID := []byte{
		0x00,
		0x00,
		0x00,
		0x0D, // length without prefix = 13
		0x06, // message ID
	}
	bytes := make([]byte, 4+13)
	copy(bytes, lengthAndID)

	payload := []uint32{uint32(pieceNum), uint32(offset), uint32(length)}
	for i, v := range payload {
		index := 5 + i*4
		binary.BigEndian.PutUint32(bytes[index:index+4], v)
	}

	return bytes
}

// <len=0009+X><id=7><index><begin><block>
func createPieceMsg(pieceNum, offset int, block []byte) []byte {
	lengthBytes := make([]byte, 4)
	length := 9 + len(block)
	binary.BigEndian.PutUint32(lengthBytes, uint32(length))

	pieceNumBytes := make([]byte, 4)
	binary.BigEndian.PutUint32(pieceNumBytes, uint32(pieceNum))

	offsetBytes := make([]byte, 4)
	binary.BigEndian.PutUint32(offsetBytes, uint32(offset))
	return concatMultipleSlices([][]byte{lengthBytes, {0x07}, pieceNumBytes, offsetBytes, block})
}

// Credit: https://freshman.tech/snippets/go/concatenate-slices/#concatenating-multiple-slices-at-once
func concatMultipleSlices[T any](slices [][]T) []T {
	var totalLen int

	for _, s := range slices {
		totalLen += len(s)
	}

	result := make([]T, totalLen)

	var i int

	for _, s := range slices {
		i += copy(result[i:], s)
	}

	return result
}

func getPeerId() string {
	return "edededededededededed"
}

// TODO: Do we need more than one?
func getNextFreePort() string {
	return "6881"
}
