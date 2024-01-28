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

func TestCheckExistingPieces(t *testing.T) {
	const numFiles = 5

	tempDir, err := os.MkdirTemp("", "torrent_test_0_")
	if err != nil {
		t.Error(err)
	}

	fmt.Println("Created", tempDir)

	for i := 0; i < numFiles; i++ {
		file, err := os.CreateTemp(tempDir, "test_file_")
		if err != nil {
			t.Error(err)
		}
		b := make([]byte, rand.Intn(500*1024))
		_, err = file.Write(b)
		if err != nil {
			t.Error(err)
		}
	}

	mktorrent := exec.Command("mktorrent", tempDir)
	if err := mktorrent.Run(); err != nil {
		t.Error(err)
	}

	_, f := filepath.Split(tempDir)
	metaInfoFileName := f + ".torrent"

	fileBytes, err := os.ReadFile(metaInfoFileName)
	if err != nil {
		t.Error(err)
	}

	p := parser.New(true)

	topLevelMap, infoHash, err := p.ParseMetaInfoFile(fileBytes)
	if err != nil {
		t.Error(err)
	}

	torr, err := New(metaInfoFileName, topLevelMap, infoHash)
	if err != nil {
		t.Error(err)
	}

	torr.dir = filepath.Join(os.TempDir(), torr.dir)

	// Expect all pieces to pass hash check
	err = torr.CreateAndCheckFiles()
	if err != nil {
		t.Error(err)
	}

	if err := os.RemoveAll(torr.dir); err != nil {
		t.Error(err)
	}
}

func TestCreateMetainfoFile(t *testing.T) {}
