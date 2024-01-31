package torrent

import (
    "fmt"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/esmakov/bittorrent-client/parser"
)

// NOTE: Requires having github.com/pobrn/mktorrent/ in your PATH
func TestCheckExistingPieces(t *testing.T) {
    numFiles := rand.Intn(12)
    maxFileSize := 128 * 1024

	testDir, err := os.MkdirTemp(".", "torrent_test_0_")
	if err != nil {
		t.Error(err)
	}

	for i := 0; i < numFiles; i++ {
		file, err := os.CreateTemp(testDir, "test_file_")
		if err != nil {
			t.Error(err)
		}
		b := make([]byte, rand.Intn(maxFileSize))
        // So we don't get correct=true when checking a piece that hasn't been completed
        for i := 0; i < len(b); i++ {
            b[i] = 1
        }
		_, err = file.Write(b)
		if err != nil {
			t.Error(err)
		}
	}

	mktorrent := exec.Command("mktorrent", testDir)
	if err := mktorrent.Run(); err != nil {
		t.Error(err)
	}

	_, f := filepath.Split(testDir)
	metaInfoFileName := f + ".torrent"

	fileBytes, err := os.ReadFile(metaInfoFileName)
	if err != nil {
		t.Error(err)
	}

	p := parser.New(false)

	topLevelMap, infoHash, err := p.ParseMetaInfoFile(fileBytes)
	if err != nil {
		t.Error(err)
	}

	torr, err := New(metaInfoFileName, topLevelMap, infoHash)
	if err != nil {
		t.Error(err)
	}

	// Expect all pieces to pass hash check
	err = torr.CreateAndCheckFiles()
	if err != nil {
		t.Error(err)
	}

    if !torr.IsComplete() {
        t.Error(fmt.Sprintf("Only %v/%v pieces were checked:%b", torr.piecesDownloaded, torr.numPieces, torr.bitfield))
    }

	if err := os.RemoveAll(torr.dir); err != nil {
		t.Error(err)
	}

	if err := os.Remove(metaInfoFileName); err != nil {
		t.Error(err)
	}
}

func TestNextAvailablePieceIdx(t *testing.T) {}
