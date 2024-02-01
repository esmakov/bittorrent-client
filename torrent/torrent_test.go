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

func TestCheckExistingPieces(t *testing.T) {
    torr, err := createTorrentWithTestData(
        rand.Intn(100),
        rand.Intn(128 * 1024))
	if err != nil {
		t.Error(err)
	}

    filesToCheck, err := torr.OpenOrCreateFiles()
	if err != nil {
		t.Error(err)
	}

	_, err = torr.CheckExistingPieces(filesToCheck)
	if err != nil {
        t.Error(err)
	}

    if !torr.IsComplete() {
        t.Fatalf("Expected all, but only %v/%v pieces were verified: %b", torr.piecesDownloaded, torr.numPieces, torr.bitfield)
    }

	if err := os.RemoveAll(torr.dir); err != nil {
		t.Error(err)
	}

	if err := os.Remove(torr.metaInfoFileName); err != nil {
		t.Error(err)
	}
}

func TestSavePiece(t *testing.T) {
    torr, err := createTorrentWithTestData(
        3,
        rand.Intn(128 * 1024))
	if err := os.RemoveAll(torr.dir); err != nil {
		t.Error(err)
	}

	p := NewPieceData(torr.pieceSize)
    p.num = rand.Intn(torr.numPieces)

    lastPieceSize := torr.totalSize - (torr.numPieces -1 ) * torr.pieceSize
    currPieceSize := torr.pieceSize
    if p.num == torr.numPieces - 1 {
        currPieceSize = lastPieceSize
    }
    for i:=0;i<len(p.data);i++ {
        p.data[i] = 1
    }

    filesToCheck, err := torr.OpenOrCreateFiles()
	if err != nil {
		t.Error(err)
	}

    if err := torr.savePiece(p, currPieceSize); err != nil {
        t.Error(err)
    }

	existingPieces, err := torr.CheckExistingPieces(filesToCheck)
	if err != nil {
        t.Error(err)
	}

    if slices.Contains(existingPieces, p.num) {
        t.Fatalf("Expected piece %v to be saved", p.num)
    }

	if err := os.RemoveAll(torr.dir); err != nil {
		t.Error(err)
	}

	if err := os.Remove(torr.metaInfoFileName); err != nil {
		t.Error(err)
	}
}

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

func TestNextAvailablePieceIdx(t *testing.T) {}
