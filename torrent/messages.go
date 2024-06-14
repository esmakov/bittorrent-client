package torrent

import (
	"encoding/binary"
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

// parseMultiMessage will parse a chunk of bytes for multiple sequential messages until empty
func parseMultiMessage(buf, infoHash []byte) ([]peerMessage, error) {
	var msgList []peerMessage
	for i := 0; i < len(buf)-1; {
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
	handshakeStart := string(rune(0x13)) + "BitTorrent protocol" // Only version 1.0 supported
	if len(buf) >= len(handshakeStart) {
		bufStart := buf[:len(handshakeStart)]
		if string(bufStart) == handshakeStart {
			msg, e = parseAndVerifyHandshake(buf, infoHash)
			return
		}
	}

	if len(buf) == 4 {
		msg.kind = keepalive
		msg.totalSize = 4
		return
	}

	statedLen := int(binary.BigEndian.Uint32(buf[:4]))

	if statedLen > len(buf) || len(buf) < 4 {
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

	msg.totalSize = statedLen + 4 // Include length prefix

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
	protocolLen := int(buf[0])
	if protocolLen != 19 {
		return peerMessage{}, ErrUnsupportedProtocol
	}

	reservedBytes := buf[protocolLen+1 : protocolLen+9]
	_ = reservedBytes

	theirInfoHash := buf[protocolLen+9 : protocolLen+29]
	if !slices.Equal(theirInfoHash, expectedInfoHash) {
		return peerMessage{}, ErrBadInfoHash
	}

	return peerMessage{
		kind:      handshake,
		totalSize: protocolLen + 49,
		peerId:    buf[protocolLen+29 : protocolLen+49],
	}, nil
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
func createRequestMsg(pieceNum, offset, length uint32) []byte {
	lengthAndID := []byte{
		0x00,
		0x00,
		0x00,
		0x0D, // length without prefix = 13
		0x06, // message ID
	}
	bytes := make([]byte, 4+13)
	copy(bytes, lengthAndID)

	payload := []uint32{pieceNum, offset, length}
	for i, v := range payload {
		idx := 5 + i*4
		binary.BigEndian.PutUint32(bytes[idx:idx+4], v)
	}

	return bytes
}

// <len=0009+X><id=7><index><begin><block>
func createPieceMsg(pieceNum, offset uint32, block []byte) []byte {
	lengthBytes := make([]byte, 4)
	length := uint32(9 + len(block))
	binary.BigEndian.PutUint32(lengthBytes, length)

	pieceNumBytes := make([]byte, 4)
	binary.BigEndian.PutUint32(pieceNumBytes, pieceNum)

	offsetBytes := make([]byte, 4)
	binary.BigEndian.PutUint32(offsetBytes, offset)
	return concatMultipleSlices([][]byte{lengthBytes, {0x07}, pieceNumBytes, offsetBytes, block})
}

// have: <len=0005><id=4><piece index>
// "The have message is fixed length. The payload is the zero-based index of a piece that has just been successfully downloaded and verified via the hash."
func createHaveMsg(pieceNum uint32) []byte {
	lengthAndID := []byte{
		0x00,
		0x00,
		0x00,
		0x05, // length without prefix = 5
		0x04, // message ID
	}
	bytes := make([]byte, 4+5)
	copy(bytes, lengthAndID)
	binary.BigEndian.AppendUint32(bytes, pieceNum)

	return bytes
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

// TODO: Do we need more than one?
func getNextFreePort() string {
	return "6881"
}
