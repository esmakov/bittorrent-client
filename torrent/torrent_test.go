package torrent

import (
	"crypto/rand"
	"fmt"
	"log"
	mrand "math/rand"
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
		_, err = rand.Read(b)
		if err != nil {
			return new(Torrent), err
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

	_, err = torr.CreateFiles()
	if err != nil {
		return nil, err
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
		mrand.Intn(32*1024))
	if err != nil {
		t.Fatal(err)
	}

	_, err = torr.CheckAllPieces()
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

func TestWritePieceToDisk(t *testing.T) {
	torr, err := createTorrentWithTestData(
		5,
		mrand.Intn(32*1024))
	if err != nil {
		t.Fatal(err)
	}

	pieceNum := mrand.Intn(torr.numPieces)
	p := newPieceData(ActualSize(torr, pieceNum))
	p.num = pieceNum
	// Needs to have the same contents the files were generated with
	err = torr.readPieceFromDisk(p)

	if err := os.RemoveAll(torr.dir); err != nil {
		t.Fatal(err)
	}

	_, err = torr.CreateFiles()
	if err != nil {
		t.Fatal(err)
	}

	if err := torr.writePieceToDisk(p); err != nil {
		t.Fatal(err)
	}

	existingPieces, err := torr.CheckAllPieces()
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

func TestReadPieceFromDisk(t *testing.T) {
	torr, err := createTorrentWithTestData(
		3,
		mrand.Intn(32*1024))
	if err != nil {
		t.Fatal(err)
	}

	pieceNum := mrand.Intn(torr.numPieces)
	p := newPieceData(ActualSize(torr, pieceNum))
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
		mrand.Intn(32*1024))
	if err != nil {
		t.Fatal(err)
	}

	pieceNum := mrand.Intn(torr.numPieces)
	p := newPieceData(ActualSize(torr, pieceNum))
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
		mrand.Intn(32*1024))
	if err != nil {
		t.Fatal(err)
	}

	torr.SetWantedBitfield()

	p := newPieceData(torr.pieceSize)
	p.num = mrand.Intn(torr.numPieces)

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

// Pieces must be downloaded if even a single byte belongs to a file the user wants,
// even if the rest of the piece is in an unwanted file.
func TestGetWantedPieceNumsNoBoundaryCrossed(t *testing.T) {
	torr, err := createTorrentWithTestData(
		3,
		SMALLEST_TYPICAL_PIECE_SIZE)
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
	for i, result := range actual {
		if expected[i] != result {
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
		3,
		SMALLEST_TYPICAL_PIECE_SIZE/3)
	if err != nil {
		t.Fatal(err)
	}

	torr.Files[0].Wanted = false
	expected := []bool{true} // Only one piece so we definitely want it

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
