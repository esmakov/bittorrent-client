package torrent

import (
	"fmt"
	"log"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"

	"github.com/esmakov/bittorrent-client/messages"
)

const SMALLEST_TYPICAL_PIECE_SIZE = 32 * 1024

/*
Creates numFiles of fileSize bytes (all set to Wanted=true) in a new directory
and generates a metainfo file from them, thereby returning a completed Torrent.
The file descriptors are uninitialized until calling the OpenOrCreateFiles method.

The file contents are initialized to 1 at the byte level to distinguish them from empty files.
(i.e. we don't get correct=true when checking a piece that hasn't been completed)

Deleting the test directory and corresponding metainfo file is the caller's responsibility.

NOTE: Requires having github.com/pobrn/mktorrent/ in your PATH
*/
func createTorrentWithTestData(numFiles, fileSize int) (*Torrent, error) {
	testDir, err := os.MkdirTemp(".", "torrent_test_")
	if err != nil {
		return new(Torrent), err
	}

	for range numFiles {
		file, err := os.CreateTemp(testDir, "test_file_")
		if err != nil {
			return new(Torrent), err
		}

		b := make([]byte, fileSize)

		for i := range len(b) {
			b[i] = 1
		}

		_, err = file.Write(b)
		if err != nil {
			return new(Torrent), err
		}
	}

	path, err := exec.LookPath("mktorrent")
	if err != nil {
		log.Fatal("mktorrent dependency not found in PATH")
	}

	mktorrent := exec.Command(path, testDir)
	if err := mktorrent.Run(); err != nil {
		return new(Torrent), err
	}

	metaInfoFileName := filepath.Base(testDir) + ".torrent"

	torr, err := New(metaInfoFileName, false)
	if err != nil {
		return new(Torrent), err
	}

	for _, file := range torr.Files {
		file.Wanted = true
	}

	if torr.numPieces <= 0 {
		return createTorrentWithTestData(1, 64*1024)
	}
	return torr, nil
}

func TestSetBitfield(t *testing.T) {
	cases := []struct {
		expected byte
		arg      int
	}{
		{expected: 0b00010000, arg: 3},
		{expected: 0b00000010, arg: 6},
	}

	b := make([]byte, 1)

	for _, test := range cases {
		setBitfield(b, test.arg)
		if b[0] != byte(test.expected) {
			t.Fatalf("Expected: %b, got: %v\n", test.expected, b[0])
		}
		clear(b)
	}
}

func TestClearBitfield(t *testing.T) {
	b := make([]byte, 1)
	b[0] = 0xFF
	clearBitfield(b, 3)
	expected := 0b11101111
	if b[0] != byte(expected) {
		t.Fatalf("Expected: %b, got: %v\n", expected, b[0])
	}
}

func TestPopCount(t *testing.T) {
	cases := []struct {
		expected int
		arg      byte
	}{
		{expected: 4, arg: 0b0010_0111},
		{expected: 2, arg: 0b1000_0001},
		{expected: 1, arg: 0b0001_0000},
	}

	for _, v := range cases {
		res := PopCount(v.arg)
		if res != v.expected {
			t.Fatalf("Expected: %v, got: %v\n", v.expected, res)
		}
	}
}

func TestCheckAllPieces(t *testing.T) {
	torr, err := createTorrentWithTestData(
		10,
		rand.Intn(32*1024))
	if err != nil {
		t.Fatal(err)
	}

	filesToCheck, err := torr.OpenOrCreateFiles()
	if err != nil {
		t.Fatal(err)
	}

	_, err = torr.CheckAllPieces(filesToCheck)
	if err != nil {
		t.Fatal(err)
	}

	n := 0
	for _, b := range torr.Bitfield {
		n += PopCount(b)
	}

	if !torr.IsComplete() {
		t.Fatalf("Expected all, but only %v/%v pieces were verified: %b", n, torr.numPieces, torr.Bitfield)
	}

	if err := os.RemoveAll(torr.dir); err != nil {
		t.Fatal(err)
	}

	if err := os.Remove(torr.MetaInfoFileName); err != nil {
		t.Fatal(err)
	}
}

func TestSavePieceToDisk(t *testing.T) {
	torr, err := createTorrentWithTestData(
		5,
		rand.Intn(32*1024))
	if err != nil {
		t.Fatal(err)
	}

	if err := os.RemoveAll(torr.dir); err != nil {
		t.Fatal(err)
	}

	pieceNum := rand.Intn(torr.numPieces)
	currPieceSize := torr.pieceSize
	if pieceNum == torr.numPieces-1 {
		currPieceSize = torr.totalSize - pieceNum*torr.pieceSize
	}
	p := newPieceData(currPieceSize)
	p.num = pieceNum

	// Needs to have the same contents the files were generated with
	for i := range len(p.data) {
		p.data[i] = 1
	}

	filesToCheck, err := torr.OpenOrCreateFiles()
	if err != nil {
		t.Fatal(err)
	}

	if err := torr.writePieceToDisk(p); err != nil {
		t.Fatal(err)
	}

	existingPieces, err := torr.CheckAllPieces(filesToCheck)
	if err != nil {
		t.Fatal(err)
	}

	if !slices.Contains(existingPieces, p.num) {
		t.Fatalf("Expected piece %v to be on disk", p.num)
	}

	if err := os.RemoveAll(torr.dir); err != nil {
		t.Fatal(err)
	}

	if err := os.Remove(torr.MetaInfoFileName); err != nil {
		t.Fatal(err)
	}
}

func TestGetPieceFromDisk(t *testing.T) {
	torr, err := createTorrentWithTestData(
		3,
		rand.Intn(32*1024))
	if err != nil {
		t.Fatal(err)
	}

	_, err = torr.OpenOrCreateFiles()
	if err != nil {
		t.Fatal(err)
	}

	pieceNum := rand.Intn(torr.numPieces)
	currPieceSize := torr.pieceSize
	if pieceNum == torr.numPieces-1 {
		currPieceSize = torr.totalSize - pieceNum*torr.pieceSize
	}
	p := newPieceData(currPieceSize)
	p.num = pieceNum

	err = torr.readPieceFromDisk(p)
	if err != nil {
		t.Fatal(err)
	}

	correct, err := torr.checkPieceHash(p)
	if err != nil {
		t.Fatal(err)
	}

	if !correct {
		t.Fatalf("Piece %v failed hash check", p.num)
	}

	if err := os.RemoveAll(torr.dir); err != nil {
		t.Fatal(err)
	}

	if err := os.Remove(torr.MetaInfoFileName); err != nil {
		t.Fatal(err)
	}
}

func TestSplitIntoBlocks(t *testing.T) {
	torr, err := createTorrentWithTestData(
		3,
		rand.Intn(32*1024))
	if err != nil {
		t.Fatal(err)
	}

	_, err = torr.OpenOrCreateFiles()
	if err != nil {
		t.Fatal(err)
	}

	pieceNum := rand.Intn(torr.numPieces)
	currPieceSize := torr.pieceSize
	if pieceNum == torr.numPieces-1 {
		currPieceSize = torr.totalSize - pieceNum*torr.pieceSize
	}
	p := newPieceData(currPieceSize)
	p.num = pieceNum

	err = torr.readPieceFromDisk(p)
	if err != nil {
		t.Fatal(err)
	}

	blocks := p.splitIntoBlocks(torr, CLIENT_BLOCK_SIZE)
	p.data = messages.ConcatMultipleSlices(blocks)
	correct, err := torr.checkPieceHash(p)
	if err != nil {
		t.Fatal(err)
	}

	if !correct {
		t.Fatalf("Piece %v failed hash check", p.num)
	}

	if err := os.RemoveAll(torr.dir); err != nil {
		t.Fatal(err)
	}

	if err := os.Remove(torr.MetaInfoFileName); err != nil {
		t.Fatal(err)
	}
}

func TestSelectNextPiece(t *testing.T) {
	torr, err := createTorrentWithTestData(
		3,
		rand.Intn(32*1024))
	if err != nil {
		t.Fatal(err)
	}

	torr.SetWantedBitfield()

	p := newPieceData(torr.pieceSize)
	p.num = rand.Intn(torr.numPieces)

	expected := p.num + 1
	if p.num == torr.numPieces-1 {
		expected = 0
	}

	peerBitfield := make([]byte, len(torr.Bitfield))
	for i := range len(peerBitfield) {
		// Our pretend peer has all the pieces
		peerBitfield[i] |= 0xFF
		torr.Bitfield[i] &= 0x00
	}

	actual, err := torr.selectNextPieceSeq(p.num, peerBitfield)
	if err != nil {
		t.Fatal(err)
	}

	if actual != expected {
		t.Fatalf("Expected %v, got %v\n", expected, actual)
	}

	if err := os.RemoveAll(torr.dir); err != nil {
		t.Fatal(err)
	}

	if err := os.Remove(torr.MetaInfoFileName); err != nil {
		t.Fatal(err)
	}
}

// Pieces must be downloaded if even a single byte belongs to a file the user wants, even if the rest of the piece is in an unwanted file.
func TestGetWantedPieceNumsNoBoundaryCrossed(t *testing.T) {
	torr, err := createTorrentWithTestData(
		3,
		SMALLEST_TYPICAL_PIECE_SIZE)
	if err != nil {
		t.Fatal(err)
	}

	_, err = torr.OpenOrCreateFiles()
	if err != nil {
		t.Fatal(err)
	}

	if torr.numPieces != 3 {
		fmt.Printf("torr.pieceSize: %v\n", torr.pieceSize)
		t.Fatalf("Expected %v pieces, actual: %v\n", 3, torr.numPieces)
	}

	torr.Files[1].Wanted = false
	expected := []bool{true, false, true}

	actual := torr.getWantedPieces()
	for i, bool := range actual {
		if expected[i] != bool {
			t.Fatalf("Expected to want pieces %v, actually wanted pieces %v\n", expected, actual)
		}
	}

	if err := os.RemoveAll(torr.dir); err != nil {
		t.Fatal(err)
	}

	if err := os.Remove(torr.MetaInfoFileName); err != nil {
		t.Fatal(err)
	}
}

func TestGetWantedPieceNumsBoundaryCrossed(t *testing.T) {
	torr, err := createTorrentWithTestData(
		2,
		SMALLEST_TYPICAL_PIECE_SIZE/2)
	if err != nil {
		t.Fatal(err)
	}

	if torr.numPieces != 1 {
		t.Fatalf("Expected there to be 1 piece, actual: %v\n", torr.numPieces)
	}

	_, err = torr.OpenOrCreateFiles()
	if err != nil {
		t.Fatal(err)
	}

	torr.Files[1].Wanted = false
	expected := []bool{true}

	actual := torr.getWantedPieces()
	for i, bool := range actual {
		if expected[i] != bool {
			t.Fatalf("Expected to want pieces %v, actually wanted pieces %v\n", expected, actual)
		}
	}

	if err := os.RemoveAll(torr.dir); err != nil {
		t.Fatal(err)
	}

	if err := os.Remove(torr.MetaInfoFileName); err != nil {
		t.Fatal(err)
	}
}
