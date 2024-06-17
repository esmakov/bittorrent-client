package torrent

import (
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
)

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

	mktorrent := exec.Command("mktorrent", testDir)
	if err := mktorrent.Run(); err != nil {
		return new(Torrent), err
	}

	metaInfoFileName := filepath.Base(testDir) + ".torrent"

	torr, err := New(metaInfoFileName, false)
	if err != nil {
		return new(Torrent), err
	}

	for _, file := range torr.Files() {
		file.Wanted = true
	}

	// Has to be called again because we needed to programmatically
	// set the files as wanted above _after_ calling New
	torr.SetWantedBitfield()

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

	if err := os.Remove(torr.metaInfoFileName); err != nil {
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

	if err := os.Remove(torr.metaInfoFileName); err != nil {
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

	// We should want all the pieces now
	if err := os.RemoveAll(torr.dir); err != nil {
		t.Fatal(err)
	}

	p := newPieceData(torr.pieceSize)
	p.num = rand.Intn(torr.numPieces)

	expectedPieceNum := p.num + 1
	if p.num == torr.numPieces-1 {
		expectedPieceNum = 0
	}

	fmt.Printf("torr.numPieces: %v\n", torr.numPieces)
	fmt.Printf("p.num: %v\n", p.num)
	fmt.Printf("expectedPieceNum: %v\n", expectedPieceNum)

	peerBitfield := make([]byte, len(torr.bitfield))
	for i := range len(peerBitfield) {
		// Our pretend peer has all the pieces
		peerBitfield[i] |= 0xFF
		torr.bitfield[i] &= 0x00
	}
	fmt.Printf("peerBitfield: %b\n", peerBitfield)
	fmt.Printf("torr.bitfield: %b\n", torr.bitfield)
	fmt.Printf("torr.wantedBitfield: %b\n", torr.wantedBitfield)

	num, err := torr.selectNextPiece(p.num, peerBitfield)
	if err != nil {
		t.Fatal(err)
	}

	if err := os.Remove(torr.metaInfoFileName); err != nil {
		t.Fatal(err)
	}

	if num != expectedPieceNum {
		t.Fatalf("Expected %v, got %v\n", expectedPieceNum, num)
	}
}

func TestGetWantedPieceNumsNoBoundaryCrossed(t *testing.T) {
	torr, err := createTorrentWithTestData(
		3,
		2^18) // Each file contains exactly 1 piece
	if err != nil {
		t.Fatal(err)
	}

	_, err = torr.OpenOrCreateFiles()
	if err != nil {
		t.Fatal(err)
	}

	torr.Files()[1].Wanted = false
	expectedWantedPieces := []bool{true, false, true}

	actualWantedPieceNums := torr.getWantedPieceNums()
	for i, bool := range actualWantedPieceNums {
		if expectedWantedPieces[i] != bool {
			t.Fatalf("Expected to want pieces %v, actually wanted pieces %v\n", expectedWantedPieces, actualWantedPieceNums)
		}
	}
}

// func TestSendAndReceive(t *testing.T) {
// 	sender, err := createTorrentWithTestData(
// 		3,
// 		rand.Intn(32*1024))
// 	if err != nil {
// 		t.Fatal(err)
// 	}

// 	_, err = sender.OpenOrCreateFiles()
// 	if err != nil {
// 		t.Fatal(err)
// 	}

// 	// pieceNum := rand.Intn(sender.numPieces)

// 	receiver, err := New(sender.metaInfoFileName, false)
// 	// Has to be in different directory from sender so it won't think it has the files already
// 	receiver.dir = "torrent_test_receive"
// 	receiver.OpenOrCreateFiles()

// 	// sender.Listen()
// 	signalErrors := make(chan error, 1)
// 	signalDone := make(chan struct{}, 1)
// 	receiverIP := "127.0.0.1:420"
// 	senderIP := "127.0.0.1:421"
// 	go sender.chooseResponse([]byte{0x01}, receiverIP, signalErrors, signalDone)
// 	go receiver.chooseResponse([]byte{0x02}, senderIP, signalErrors, signalDone)

// 	for {
// 		select {
// 		case e := <-signalErrors:
// 			t.Fatal("Error caught in start:", e)
// 		case <-signalDone:
// 			fmt.Println("Completed", sender.metaInfoFileName)
// 			break
// 		}
// 	}

// 	if err := os.RemoveAll(receiver.dir); err != nil {
// 		t.Fatal(err)
// 	}

// 	if err := os.RemoveAll(sender.dir); err != nil {
// 		t.Fatal(err)
// 	}

// 	if err := os.Remove(sender.metaInfoFileName); err != nil {
// 		t.Fatal(err)
// 	}
// }
