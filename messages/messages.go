package messages

import (
	"encoding/binary"
	"errors"
	"fmt"
	"slices"
)

var (
	ErrUnsupportedProtocol = errors.New("Peer protocol unsupported")
	ErrBadInfoHash         = errors.New("Peer handshake contained invalid info hash")
	ErrMessageTimedOut     = errors.New("Message timeout expired")
)

type messageKinds int

const (
	Choke messageKinds = iota
	Unchoke
	Interested
	Uninterested
	Have
	Bitfield
	Request
	Piece
	Cancel
	Port
	Keepalive
	Handshake
	// Not part of the BT protocol; used here to mean an incomplete "piece" message split into blocks
	Fragment
)

func (m messageKinds) String() string {
	switch m {
	case Choke:
		return "choke"
	case Unchoke:
		return "unchoke"
	case Interested:
		return "interested"
	case Uninterested:
		return "uninterested"
	case Have:
		return "have"
	case Bitfield:
		return "bitfield"
	case Request:
		return "request"
	case Piece:
		return "piece"
	case Cancel:
		return "cancel"
	case Port:
		return "port"
	case Keepalive:
		return "keepalive"
	case Handshake:
		return "handshake"
	default:
		return "unknown or fragment"
	}
}

// TODO: Move away from single type for all messages
type PeerMessage struct {
	Kind        messageKinds
	TotalSize   int
	PeerId      []byte
	PieceNum    int
	Bitfield    []byte
	BlockOffset int
	BlockSize   int
	BlockData   []byte
}

// parseMultiMessage will parse a chunk of bytes for multiple sequential messages until empty
func ParseMultiMessage(buf, infoHash []byte) ([]PeerMessage, error) {
	var msgList []PeerMessage
	for i := 0; i < len(buf)-1; {
		msg, err := parseMessage(buf[i:], infoHash)
		if err != nil {
			return nil, err
		}
		msgList = append(msgList, msg)
		i += msg.TotalSize
	}
	return msgList, nil
}

func parseMessage(buf, infoHash []byte) (msg PeerMessage, e error) {
	handshakeStart := string(rune(0x13)) + "BitTorrent protocol" // Only version 1.0 supported
	if len(buf) >= len(handshakeStart) {
		bufStart := buf[:len(handshakeStart)]
		if string(bufStart) == handshakeStart {
			msg, e = parseAndVerifyHandshake(buf, infoHash)
			return
		}
	}

	statedLen := int(binary.BigEndian.Uint32(buf[:4]))

	if len(buf) == 4 && statedLen == 0 {
		msg.Kind = Keepalive
		msg.TotalSize = 4
		return
	}

	if statedLen > len(buf) || len(buf) < 4 {
		// Message needs to be reassembled from this packet and the next
		msg.Kind = Fragment
		msg.TotalSize = len(buf)
		return
	}

	msg.Kind = messageKinds(int(buf[4]))

	if msg.Kind == Have || msg.Kind == Request || msg.Kind == Piece || msg.Kind == Cancel {
		msg.PieceNum = int(binary.BigEndian.Uint32(buf[5:9]))
	}

	msg.TotalSize = statedLen + 4 // Include length prefix

	if msg.TotalSize > len(buf) {
		panic(fmt.Sprintf("Out of bounds of msg buffer: %v / %v", msg.TotalSize, len(buf)))
	}

	switch msg.Kind {
	case Bitfield:
		msg.Bitfield = buf[5:msg.TotalSize]
	case Request:
		msg.BlockOffset = int(binary.BigEndian.Uint32(buf[9:13]))
		msg.BlockSize = int(binary.BigEndian.Uint32(buf[13:msg.TotalSize]))
	case Piece:
		msg.BlockOffset = int(binary.BigEndian.Uint32(buf[9:13]))
		msg.BlockData = buf[13:msg.TotalSize]
	}

	return
}

func parseAndVerifyHandshake(buf []byte, expectedInfoHash []byte) (PeerMessage, error) {
	protocolLen := int(buf[0])
	if protocolLen != 19 {
		return PeerMessage{}, ErrUnsupportedProtocol
	}

	reservedBytes := buf[protocolLen+1 : protocolLen+9]
	_ = reservedBytes

	theirInfoHash := buf[protocolLen+9 : protocolLen+29]
	if !slices.Equal(theirInfoHash, expectedInfoHash) {
		return PeerMessage{}, ErrBadInfoHash
	}

	return PeerMessage{
		Kind:      Handshake,
		TotalSize: protocolLen + 49,
		PeerId:    buf[protocolLen+29 : protocolLen+49],
	}, nil
}

/*
Unless specified otherwise, all integers in the peer wire protocol are encoded as four byte big-endian values. This includes the length prefix on all messages that come after the handshake.
*/

// <pstrlen><pstr><reserved><info_hash><peer_id>
func CreateHandshake(infoHash []byte, peerId []byte) []byte {
	protocol := "BitTorrent protocol"
	protocolBytes := make([]byte, len(protocol))
	copy(protocolBytes, protocol)

	length := []byte{byte(len(protocol))}
	reserved := []byte{0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}

	return ConcatMultipleSlices([][]byte{length, protocolBytes, reserved, infoHash, peerId})
}

func CreateChoke() []byte {
	return []byte{0x00, 0x00, 0x00, 0x01, 0x00}
}

func CreateUnchoke() []byte {
	return []byte{0x00, 0x00, 0x00, 0x01, 0x01}
}

// <len=0001><id=2>
func CreateInterested() []byte {
	lenBytes := make([]byte, 4)
	binary.BigEndian.PutUint32(lenBytes, uint32(1))
	return ConcatMultipleSlices([][]byte{lenBytes, {0x02}})
}

/*
	<len=0001+X><id=5><bitfield>

"The bitfield message may only be sent immediately after the handshaking sequence is completed, and before any other messages are sent. It is optional, and need not be sent if a client has no pieces.

The bitfield message is variable length, where X is the length of the bitfield. The payload is a bitfield representing the pieces that have been successfully downloaded. The high bit in the first byte corresponds to piece index 0. Bits that are cleared indicated a missing piece, and set bits indicate a valid and available piece. Spare bits at the end are set to zero."
*/
func CreateBitfield(bitfield []byte) []byte {
	lenBytes := make([]byte, 4)
	length := 1 + len(bitfield)
	binary.BigEndian.PutUint32(lenBytes, uint32(length))
	return ConcatMultipleSlices([][]byte{lenBytes, {0x05}, bitfield})
}

// <len=0013><id=6><index><begin><length>
func CreateRequest(pieceNum, offset, length uint32) []byte {
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
func CreatePiece(pieceNum, offset uint32, block []byte) []byte {
	lengthBytes := make([]byte, 4)
	length := uint32(9 + len(block))
	binary.BigEndian.PutUint32(lengthBytes, length)

	pieceNumBytes := make([]byte, 4)
	binary.BigEndian.PutUint32(pieceNumBytes, pieceNum)

	offsetBytes := make([]byte, 4)
	binary.BigEndian.PutUint32(offsetBytes, offset)
	return ConcatMultipleSlices([][]byte{lengthBytes, {0x07}, pieceNumBytes, offsetBytes, block})
}

// have: <len=0005><id=4><piece index>
// "The have message is fixed length. The payload is the zero-based index of a piece that has just been successfully downloaded and verified via the hash."
func CreateHave(pieceNum uint32) []byte {
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
func ConcatMultipleSlices[T any](slices [][]T) []T {
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
