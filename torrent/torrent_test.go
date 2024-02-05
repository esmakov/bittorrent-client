package torrent

import (
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"

	"github.com/esmakov/bittorrent-client/parser"
)

// Creates random size files and makes the metainfo file from them,
// thereby returning a completed Torrent.
// NOTE: Requires having github.com/pobrn/mktorrent/ in your PATH
func createTorrentWithTestData(numFiles, maxFileSize int) (*Torrent, error) {
	testDir, err := os.MkdirTemp(".", "torrent_test_")
	if err != nil {
		return new(Torrent), err
	}

	for i := 0; i < numFiles; i++ {
		file, err := os.CreateTemp(testDir, "test_file_")
		if err != nil {
			return new(Torrent), err
		}
		b := make([]byte, maxFileSize)
		// So it's distinguishable from an empty file,
		// e.g. we don't get correct=true when checking a piece that hasn't been completed
		for i := 0; i < len(b); i++ {
			b[i] = 1
		}
		_, err = file.Write(b)
		if err != nil {
			return new(Torrent), err
		}
	}

	mktorrent := exec.Command("mktorrent", testDir)
	if err := mktorrent.Run(); err != nil {
		return new(Torrent), err
	}

	_, f := filepath.Split(testDir)
	metaInfoFileName := f + ".torrent"

	fileBytes, err := os.ReadFile(metaInfoFileName)
	if err != nil {
		return new(Torrent), err
	}

	p := parser.New(false)

	topLevelMap, infoHash, err := p.ParseMetaInfoFile(fileBytes)
	if err != nil {
		return new(Torrent), err
	}

	torr, err := New(metaInfoFileName, topLevelMap, infoHash)
	if err != nil {
		return new(Torrent), err
	}

	return torr, nil
}

func TestCheckAllPieces(t *testing.T) {
	torr, err := createTorrentWithTestData(
		rand.Intn(10),
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

	if !torr.IsComplete() {
		t.Fatalf("Expected all, but only %v/%v pieces were verified: %b", torr.piecesDownloaded, torr.numPieces, torr.bitfield)
	}

	if err := os.RemoveAll(torr.dir); err != nil {
		t.Fatal(err)
	}

	if err := os.Remove(torr.metaInfoFileName); err != nil {
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
	// pieceNum := 0

	currPieceSize := torr.pieceSize
	if pieceNum == torr.numPieces-1 {
		currPieceSize = torr.totalSize - pieceNum*torr.pieceSize
	}
	p := NewPieceData(currPieceSize)
	p.num = pieceNum

	for i := 0; i < len(p.data); i++ {
		p.data[i] = 1
	}

	filesToCheck, err := torr.OpenOrCreateFiles()
	if err != nil {
		t.Fatal(err)
	}

	if err := torr.savePieceToDisk(p); err != nil {
		t.Fatal(err)
	}

	existingPieces, err := torr.CheckAllPieces(filesToCheck)
	if err != nil {
		t.Fatal(err)
	}

	if !slices.Contains(existingPieces, p.num) {
		t.Fatalf("Expected piece %v to be saved", p.num)
	}

	if err := os.RemoveAll(torr.dir); err != nil {
		t.Fatal(err)
	}

	if err := os.Remove(torr.metaInfoFileName); err != nil {
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
	p := NewPieceData(currPieceSize)
	p.num = pieceNum
	err = torr.getPieceFromDisk(p)
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

	if err := os.Remove(torr.metaInfoFileName); err != nil {
		t.Fatal(err)
	}
}

func TestNextAvailablePieceIdx(t *testing.T) {}
