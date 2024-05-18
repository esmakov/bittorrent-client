package torrent

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"math"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/esmakov/bittorrent-client/hash"
	"github.com/esmakov/bittorrent-client/parser"
)

type Torrent struct {
	metaInfoFileName string
	trackerHostname  string
	comment          string
	infoHash         []byte
	isPrivate        bool
	piecesStr        string
	pieceSize        int
	numPieces        int
	totalSize        int
	piecesDownloaded int
	piecesUploaded   int
	bitfield         []byte
	files            []*TorrentFile
	lastChecked      time.Time
	dir              string
	// Optionally sent by server
	trackerId string
	seeders   int
	leechers  int
	// TODO: state field (paused, seeding, etc), maybe related to tracker "event"
	// numInterested // for connection limit
	sync.Mutex
}

type TorrentFile struct {
	fd        *os.File
	Path      string
	finalSize int64
	Wanted    bool
}

// Initializes the Torrent state with nil file descriptors and no notion of which files are wanted.
func New(metaInfoFileName string, shouldPrettyPrint bool) (*Torrent, error) {
	fileBytes, err := os.ReadFile(metaInfoFileName)
	if err != nil {
		return nil, err
	}

	p := parser.New(shouldPrettyPrint)

	metaInfoMap, infoHash, err := p.ParseMetaInfoFile(fileBytes)
	if err != nil {
		return nil, err
	}

	// Fields common to single-file and multi-file torrents
	var announce string
	if bAnnounce, ok := metaInfoMap["announce"]; ok {
		announce = bAnnounce.(string)
	}
	bInfo := metaInfoMap["info"].(map[string]any)

	bPieceLength := bInfo["piece length"].(int)
	bPieces := bInfo["pieces"].(string)
	numPieces := len(bPieces) / 20

	bitfieldLen := int(math.Ceil(float64(numPieces) / 8))
	bitfield := make([]byte, bitfieldLen)

	// Optional common fields
	var (
		comment   string
		isPrivate int
	)

	if bComment, ok := metaInfoMap["comment"]; ok {
		comment = bComment.(string)
	}

	if bPrivate, ok := bInfo["private"]; ok {
		isPrivate = bPrivate.(int)
	}

	hasMultipleFiles := false
	bFiles := bInfo["files"]
	if bFiles != nil {
		hasMultipleFiles = true
	}

	var (
		totalSize int
		files     []*TorrentFile
		dir       string
	)

	if hasMultipleFiles {
		dir = bInfo["name"].(string)

		for _, f := range bFiles.([]any) {
			bFile := f.(map[string]any)

			bFileLength := bFile["length"].(int)
			totalSize += bFileLength

			bPathList := bFile["path"].([]any)

			var pathSegments []string
			for _, f := range bPathList {
				pathSegments = append(pathSegments, f.(string))
			}

			if len(pathSegments) == 0 {
				return nil, errors.New("Invalid file path provided in .torrent file")
			}

			files = append(files, &TorrentFile{
				Path:      filepath.Join(pathSegments...),
				finalSize: int64(bFileLength),
			})
		}
	} else {
		f := bInfo["name"].(string)
		totalSize = bInfo["length"].(int)
		files = append(files, &TorrentFile{
			Path:      f,
			finalSize: int64(totalSize),
		})
	}

	t := &Torrent{
		metaInfoFileName: metaInfoFileName,
		trackerHostname:  announce,
		infoHash:         infoHash,
		comment:          comment,
		isPrivate:        isPrivate == 1,
		piecesStr:        bPieces,
		pieceSize:        bPieceLength,
		numPieces:        numPieces,
		totalSize:        totalSize,
		bitfield:         bitfield,
		files:            files,
		dir:              dir,
	}

	return t, nil
}

func (t *Torrent) storeSeeders(n int) {
	t.Lock()
	defer t.Unlock()
	t.seeders = n
}

func (t *Torrent) storeLeechers(n int) {
	t.Lock()
	defer t.Unlock()
	t.leechers = n
}

func (t *Torrent) storeUploaded(n int) {
	t.Lock()
	defer t.Unlock()
	t.piecesUploaded = n
}

func (t *Torrent) storeDownloaded(n int) {
	t.Lock()
	defer t.Unlock()
	t.piecesDownloaded = n
}

func (t *Torrent) IsComplete() bool {
	return t.piecesDownloaded == t.numPieces
}

func (t *Torrent) String() string {
	sb := strings.Builder{}
	if t.isPrivate {
		sb.WriteString(fmt.Sprint("PRIVATE TORRENT"))
	}

	numWanted := 0
	for _, file := range t.files {
		if file.Wanted {
			numWanted++
		}
	}

	sb.WriteString(fmt.Sprint(
		fmt.Sprintln("--------------Torrent Info--------------"),
		fmt.Sprintln("Torrent file:", t.metaInfoFileName),
		fmt.Sprintln("Tracker:", t.trackerHostname),
		fmt.Sprintln("Total size:", t.totalSize),
		fmt.Sprintf("Selected %v of %v total file(s):\n", numWanted, len(t.files)),
	))

	for _, file := range t.files {
		marker := "N"
		if file.Wanted {
			marker = "Y"
		}
		sb.WriteString(fmt.Sprintf("  [%v] %v\n", marker, filepath.Base(file.Path)))
	}

	if t.comment != "" {
		sb.WriteString(fmt.Sprintln("Comment:", t.comment))
	}
	sb.WriteString("----------------------------------------")
	return sb.String()
}

// Returns the TorrentFiles by pointer so their Wanted field can be updated with user input in main.main()
func (t *Torrent) Files() []*TorrentFile {
	return t.files
}

/*
Opens the TorrentFiles for writing and returns the ones that have been modified since the torrent was last checked.

TODO: Since a new torrent instance is created and destroyed each run,
the lastChecked field doesn't persist and we aren't able to save time by not
checking files.
NOTE: Prior to calling this, torrentFiles have a nil file descriptior.
*/
func (t *Torrent) OpenOrCreateFiles() ([]*TorrentFile, error) {
	var filesToCheck []*TorrentFile
	if t.dir != "" {
		os.Mkdir(t.dir, 0o766)
	}
	for _, file := range t.files {
		if !file.Wanted {
			continue
		}

		if t.dir != "" {
			file.Path = filepath.Join(t.dir, file.Path)
		}

		fd, err := os.OpenFile(file.Path, os.O_RDWR, 0)
		if errors.Is(err, fs.ErrNotExist) {
			fd, err = os.Create(file.Path)
			if err != nil {
				return nil, err
			}
			file.fd = fd
		} else if err != nil {
			return nil, err
		}

		file.fd = fd
		info, err := os.Stat(file.fd.Name())
		if err != nil {
			return nil, err
		}

		if info.ModTime().After(t.lastChecked) {
			filesToCheck = append(filesToCheck, file)
		}
	}

	return filesToCheck, nil
}

const MAX_PEER_CONNS = 1

func (t *Torrent) Start() error {
	myPeerId := getPeerId()
	portForTrackerResponse := getNextFreePort()

	trackerResponse, err := t.sendTrackerMessage(myPeerId, portForTrackerResponse, "started")
	if err != nil {
		return err
	}

	if warning, ok := trackerResponse["warning message"]; ok {
		log.Println("TRACKER WARNING:", warning.(string))
	}

	waitInterval := trackerResponse["interval"].(int)
	_ = waitInterval

	// Optional tracker fields
	if trackerId, ok := trackerResponse["tracker id"]; ok {
		t.trackerId = trackerId.(string)
	}

	if numSeeders, ok := trackerResponse["complete"]; ok {
		t.storeSeeders(numSeeders.(int))
	}

	if numLeechers, ok := trackerResponse["incomplete"]; ok {
		t.storeLeechers(numLeechers.(int))
	}

	peersStr := trackerResponse["peers"].(string)
	peerList, err := extractCompactPeers(peersStr)
	if err != nil {
		return err
	}

	if len(peerList) == 0 {
		// TODO: Keep polling tracker for peers in a separate goroutine
	}

	errs := make(chan error, MAX_PEER_CONNS)

	// TODO: Send all peers a have msg when a piece is downloaded

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	numPeers := 0
	for numPeers < MAX_PEER_CONNS {
		// TODO: Choose more intelligently
		peer := getRandPeer(peerList)
		conn, err := net.DialTimeout("tcp", peer, 1*time.Second)
		if err != nil {
			log.Println("Dial:", err)
			continue
		}

		go t.handleConn(conn, []byte(myPeerId), errs, ctx, cancel)
		numPeers++
	}

	listenAddr, err := net.ResolveTCPAddr("tcp", fmt.Sprint("localhost:", portForTrackerResponse))
	if err != nil {
		return err
	}

	l, err := net.ListenTCP("tcp", listenAddr)
	if err != nil {
		return err
	}
	defer l.Close()

	for {
		if numPeers < MAX_PEER_CONNS {
			conn, err := l.AcceptTCP()
			if err != nil {
				log.Println("Accept:", err)
				continue
			}
			log.Printf("Incoming connection from %v\n", conn.RemoteAddr())
			go t.handleConn(conn, []byte(myPeerId), errs, ctx, cancel)
			numPeers++
		}

		select {
		// NOTE: Make sure errors are only sent when goroutines exit
		// so we don't go over MAX_PEER_CONNS
		case e := <-errs:
			numPeers--
			log.Println("Conn dropped:", e)

			peer := getRandPeer(peerList)
			conn, err := net.DialTimeout("tcp", peer, 1*time.Second)
			if err != nil {
				log.Println("Dial:", err)
				continue
			}
			go t.handleConn(conn, []byte(myPeerId), errs, ctx, cancel)
			numPeers++
		case <-ctx.Done():
			fmt.Println("COMPLETED", t.metaInfoFileName)

			// NOTE: Tracker only expects this once
			// Make sure this doesn't happen if the download started at 100% already
			t.sendTrackerMessage(myPeerId, portForTrackerResponse, "completed")
			return nil
		default:
		}
	}
}

/*
	https://wiki.theory.org/BitTorrentSpecification#Tracker_HTTP.2FHTTPS_Protocol

event: If specified, must be one of started, completed, stopped, (or empty which is the same as not being specified). If not specified, then this request is one performed at regular intervals.

started: The first request to the tracker must include the event key with this value.

stopped: Must be sent to the tracker if the client is shutting down gracefully.

completed: Must be sent to the tracker when the download completes. However, must not be sent if the download was already 100% complete when the client started. Presumably, this is to allow the tracker to increment the "completed downloads" metric based solely on this event.
*/
func (t *Torrent) sendTrackerMessage(peerId, portForTrackerResponse, event string) (map[string]any, error) {
	reqURL := url.URL{
		Opaque: t.trackerHostname,
	}
	queryParams := url.Values{}
	queryParams.Set("peer_id", peerId)
	queryParams.Set("port", portForTrackerResponse)
	queryParams.Set("uploaded", strconv.Itoa(t.piecesUploaded))
	queryParams.Set("downloaded", strconv.Itoa(t.piecesDownloaded))
	queryParams.Set("left", strconv.Itoa(t.numPieces-t.piecesDownloaded))
	queryParams.Set("event", event)
	queryParams.Set("compact", "1")
	if t.trackerId != "" {
		queryParams.Set("trackerid", t.trackerId)
	}
	reqURL.RawQuery = "info_hash=" + hash.CustomURLEscape(t.infoHash) + "&" + queryParams.Encode()

	req, err := http.NewRequest("GET", reqURL.String(), nil)
	if err != nil {
		return nil, err
	}

	req.Close = true
	req.Header.Set("Connection", "close")

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	p := parser.New(false)
	response, err := p.ParseTrackerResponse(body)
	if err != nil {
		return nil, err
	}

	if failureReason, ok := response["failure reason"]; ok {
		return nil, errors.New("TRACKER FAILURE REASON: " + failureReason.(string))
	}

	return response, nil
}

func (t *Torrent) checkPieceHash(p *pieceData) (bool, error) {
	currPieceSize := t.pieceSize
	if p.num == t.numPieces-1 {
		currPieceSize = t.totalSize - p.num*t.pieceSize
	}
	givenHash, err := hash.HashSHA1(p.data[:currPieceSize])
	if err != nil {
		return false, err
	}
	return string(givenHash) == t.piecesStr[p.num*20:p.num*20+20], nil
}

var PieceNotOnDiskErr error = errors.New("Piece not wanted or not downloaded yet")

// Makes sure existing data on disk is verified when the user adds a torrent.
// TODO: Do in parallel?
func (t *Torrent) CheckAllPieces(files []*TorrentFile) ([]int, error) {
	p := newPieceData(t.pieceSize)
	var existingPieces []int

	for p.num = range t.numPieces {
		err := t.getPieceFromDisk(p)
		if err == PieceNotOnDiskErr {
			continue
		} else if err != nil {
			return existingPieces, err
		}

		empty := slices.Max(p.data) == 0

		correct, err := t.checkPieceHash(p)
		if err != nil {
			return existingPieces, err
		}

		if !correct {
			if !empty {
				return existingPieces, errors.New(fmt.Sprintf("Piece %v failed hash check\n", p.num))
			}
			// Piece hasn't been downloaded but some pieces ahead have
			continue
		}

		// NOTE: Piece could be empty and still correct at this point, if intentionally so
		updateBitfield(t.bitfield, p.num)
		t.piecesDownloaded++
		existingPieces = append(existingPieces, p.num)
		clear(p.data)
	}

	t.lastChecked = time.Now()
	return existingPieces, nil
}

// Writes into the provided pieceData, assuming the piece number is initialized and in a file we want
func (t *Torrent) getPieceFromDisk(p *pieceData) error {
	currPieceSize := t.pieceSize
	if p.num == t.numPieces-1 {
		currPieceSize = t.totalSize - p.num*t.pieceSize
	}
	pieceStartIdx := int64(p.num * t.pieceSize)
	pieceEndIdx := pieceStartIdx + int64(currPieceSize)

	if len(t.files) == 1 {
		fileInfo, err := os.Stat(t.files[0].Path)
		if err != nil {
			return err
		}

		if pieceStartIdx >= fileInfo.Size() {
			return PieceNotOnDiskErr
		}
		_, err = t.files[0].fd.ReadAt(p.data[:currPieceSize], pieceStartIdx)
		return err
	}

	// All indices are relative to the "stream" of pieces and may cross file boundaries
	fileStartIdx := int64(0)
	remainingPieceSize := currPieceSize
	startReadAt := 0

	for _, currFile := range t.files {
		if !currFile.Wanted {
			continue
		}
		fileEndIdx := fileStartIdx + currFile.finalSize

		if pieceStartIdx > fileEndIdx {
			fileStartIdx += currFile.finalSize
			continue
		}

		fileInfo, err := os.Stat(currFile.Path)
		if err != nil {
			return err
		}

		for {
			pieceOffsetIntoFile := pieceStartIdx - fileStartIdx
			bytesFromEOF := fileEndIdx - pieceStartIdx

			if fileInfo.Size() < currFile.finalSize {
				if pieceOffsetIntoFile >= fileInfo.Size() {
					return PieceNotOnDiskErr
				}
			} else {
				if pieceOffsetIntoFile >= currFile.finalSize {
					panic("Unreachable")
				}
			}

			if (pieceStartIdx >= fileStartIdx && pieceEndIdx <= fileEndIdx) || remainingPieceSize < currPieceSize {
				if remainingPieceSize < currPieceSize {
					readSize := min(int(currFile.finalSize), remainingPieceSize)
					if _, err := currFile.fd.ReadAt(p.data[startReadAt:startReadAt+readSize], 0); err != nil {
						return err
					}
					remainingPieceSize -= readSize
					startReadAt += readSize
					if remainingPieceSize < 0 {
						panic("Unreachable: remainingPieceSize < 0")
					}

					if remainingPieceSize > 0 {
						// Move on to yet another (at least a third) file to finish this piece
						fileStartIdx += currFile.finalSize
						break
					}

					if startReadAt != currPieceSize {
						panic("Unreachable: Piece fragments do not add up to a whole piece")
					}

				} else {
					if _, err := currFile.fd.ReadAt(p.data[:currPieceSize], pieceOffsetIntoFile); err != nil {
						return err
					}
				}

				return nil
			} else if pieceStartIdx >= fileStartIdx && pieceEndIdx > fileEndIdx {
				// Piece crosses file boundary

				if pieceStartIdx < fileStartIdx {
					panic("bytesFromEnd will be calculated too high")
				}

				// TODO: Shouldn't have to subslice as long as the caller initializes the pieceData with correct size
				if _, err := currFile.fd.ReadAt(p.data[:bytesFromEOF], pieceOffsetIntoFile); err != nil {
					return err
				}

				remainingPieceSize -= int(bytesFromEOF)
				startReadAt += int(bytesFromEOF)
				fileStartIdx += currFile.finalSize
				break
			} else {
				panic("Unreachable")
			}
		}
	}

	return nil
}

// Reads data from the provided pieceData and saves it, assuming the piece number is initialized
func (t *Torrent) savePieceToDisk(p *pieceData) error {
	currPieceSize := t.pieceSize
	if p.num == t.numPieces-1 {
		currPieceSize = t.totalSize - p.num*t.pieceSize
	}
	pieceStartIdx := int64(p.num * t.pieceSize)
	pieceEndIdx := pieceStartIdx + int64(currPieceSize)

	if len(t.files) == 1 {
		_, err := t.files[0].fd.WriteAt(p.data[:currPieceSize], pieceStartIdx)
		return err
	}

	// All indices are relative to the "stream" of pieces and may cross file boundaries
	fileStartIdx := int64(0)
	remainingPieceSize := currPieceSize
	startWriteAt := 0

	for i := 0; i < len(t.files); i++ {
		currFile := t.files[i]
		fileEndIdx := fileStartIdx + currFile.finalSize

		if pieceStartIdx > fileEndIdx {
			fileStartIdx += currFile.finalSize
			continue
		}

		fileInfo, err := os.Stat(currFile.Path)
		if err != nil {
			return err
		}

		for {
			pieceOffsetIntoFile := pieceStartIdx - fileStartIdx
			bytesFromEOF := fileEndIdx - pieceStartIdx

			if pieceOffsetIntoFile < 0 {
				pieceOffsetIntoFile = 0
			}

			if fileInfo.Size() >= currFile.finalSize {
				if pieceOffsetIntoFile >= currFile.finalSize {
					panic("Unreachable")
				}
			}

			if (pieceStartIdx >= fileStartIdx && pieceEndIdx <= fileEndIdx) || remainingPieceSize < currPieceSize {
				if remainingPieceSize < currPieceSize {
					writeSize := min(int(currFile.finalSize), remainingPieceSize)
					if pieceOffsetIntoFile != 0 {
						panic("Interesting if this happens, maybe we skipped a piece belonging to a file we didn't want?")
					}
					if _, err := currFile.fd.WriteAt(p.data[startWriteAt:startWriteAt+writeSize], 0); err != nil {
						return err
					}
					remainingPieceSize -= writeSize
					startWriteAt += writeSize
					if remainingPieceSize < 0 {
						panic("remainingPieceSize < 0")
					}

					if remainingPieceSize > 0 {
						// Move on to yet another (at least a third) file to finish this piece
						fileStartIdx += currFile.finalSize
						break
					}

					if startWriteAt != currPieceSize {
						panic("Piece fragments do not add up to a whole piece")
					}

				} else {
					if _, err := currFile.fd.WriteAt(p.data[:currPieceSize], pieceOffsetIntoFile); err != nil {
						return err
					}
				}

				return nil
			} else if pieceStartIdx >= fileStartIdx && pieceEndIdx > fileEndIdx {
				// Piece crosses file boundary

				if pieceStartIdx < fileStartIdx {
					panic("bytesFromEnd will be calculated too high")
				}

				// TODO: Shouldn't have to subslice as long as the caller calls NewPieceData with correct size
				if _, err := currFile.fd.WriteAt(p.data[:bytesFromEOF], pieceOffsetIntoFile); err != nil {
					return err
				}

				remainingPieceSize -= int(bytesFromEOF)
				startWriteAt += int(bytesFromEOF)
				fileStartIdx += currFile.finalSize
				break
			} else {
				panic("Unreachable")
			}
		}
	}

	return nil
}

func getRandPeer(peerList []string) string {
	return peerList[rand.Intn(len(peerList))]
}

const BLOCK_SIZE = 16 * 1024

type pieceData struct {
	num  int
	data []byte
}

func newPieceData(pieceLen int) *pieceData {
	data := make([]byte, pieceLen)
	return &pieceData{data: data}
}

func (p *pieceData) storeBlockIntoPiece(msg peerMessage, blockSize int) {
	block := p.data[msg.blockOffset : msg.blockOffset+blockSize]
	// TODO: Don't copy each block twice
	copy(block, msg.blockData)
}

func (p *pieceData) splitIntoBlocks(t *Torrent, blockSize int) [][]byte {
	currPieceSize := t.pieceSize
	if p.num == t.numPieces-1 {
		currPieceSize = t.totalSize - p.num*t.pieceSize
	}
	numBlocks := int(math.Ceil(float64(currPieceSize) / float64(blockSize)))
	blockOffset := 0
	// lastBlockOffset := (numBlocks - 1) * BLOCK_SIZE

	blocks := make([][]byte, numBlocks)
	for i := 0; i < numBlocks; i++ {
		remainingPieceSize := currPieceSize - i*blockSize
		readSize := min(blockSize, remainingPieceSize)
		blocks[i] = p.data[blockOffset : blockOffset+readSize]
		blockOffset += blockSize
	}
	return blocks
}

func (t *Torrent) chooseResponse(peerAddr string, outboundMsgs chan<- []byte, parsedMsgs <-chan peerMessage, errs chan<- error, cancel context.CancelFunc) {
	defer close(outboundMsgs)

	connState := newConnState()

	bfLen := int(math.Ceil(float64(t.numPieces) / 8.0))
	peerBitfield := make([]byte, bfLen)

	blockOffset := 0
	numBlocks := t.pieceSize / BLOCK_SIZE
	lastBlockOffset := (numBlocks - 1) * BLOCK_SIZE
	currBlockSize := BLOCK_SIZE
	currPieceSize := t.pieceSize
	p := newPieceData(t.pieceSize)

	for {
		msg, open := <-parsedMsgs
		if !open {
			// log.Println("handleConn signaled to drop connection to", peerAddr)
			return
		}

		if msg.kind != fragment {
			fmt.Printf("%v sent a %v\n", peerAddr, msg.kind)
		}

		switch msg.kind {
		case handshake:
			// fmt.Printf("%v has peer id %v\n", peerAddr, string(msg.peerId))
			outboundMsgs <- createBitfieldMsg(t.bitfield)
			fmt.Printf("Sending bitfield to %v\n", peerAddr)

			// TODO: Determine if we want to talk to this peer
			outboundMsgs <- createUnchokeMsg()
		case request:
			err := t.handlePieceRequest(msg.pieceNum, msg.blockSize, outboundMsgs)
			if err != nil {
				// Drop connection?
				log.Println("Error retrieving piece:", err)
			}
		case keepalive:
		case choke:
			connState.peer_choking = true
		case unchoke:
			connState.peer_choking = false
		case interested:
			connState.peer_interested = true
		case uninterested:
			connState.peer_interested = false
		case bitfield:
			// if connState.peer_choking {
			// 	continue
			// }
			peerBitfield = msg.bitfield

			outboundMsgs <- createInterestedMsg()
			fmt.Printf("Sending interested, request to %v\n", peerAddr)

			// Start wherever for now
			var err error
			p.num, err = nextAvailablePieceIdx(rand.Intn(t.numPieces), t.numPieces, peerBitfield, t.bitfield)
			if err != nil {
				errs <- err
				return
			}

			reqMsg := createRequestMsg(p.num, blockOffset, BLOCK_SIZE)
			outboundMsgs <- reqMsg
			fmt.Printf("Requesting piece %v from %v\n", p.num, peerAddr)
		case piece:
			p.storeBlockIntoPiece(msg, currBlockSize)

			if p.num == t.numPieces-1 {
				currPieceSize = t.totalSize - p.num*t.pieceSize
				numBlocks = int(math.Ceil(float64(currPieceSize) / BLOCK_SIZE))
				lastBlockOffset = (numBlocks - 1) * BLOCK_SIZE
			}

			// Incomplete piece
			if blockOffset < lastBlockOffset {
				// Account for block just downloaded
				blockOffset += BLOCK_SIZE
				remaining := currPieceSize - blockOffset
				if remaining < BLOCK_SIZE {
					currBlockSize = remaining
				}
				fmt.Println(blockOffset, "/", currPieceSize, "bytes")
			} else {
				fmt.Printf("Completed piece %v, checking...\n", p.num)
				correct, err := t.checkPieceHash(p)
				if err != nil {
					errs <- err
					return
				}

				// TODO: Add peer to blacklist
				if !correct {
					errs <- fmt.Errorf("Piece %v from %v failed hash check", p.num, peerAddr)
					return
				}

				if err = t.savePieceToDisk(p); err != nil {
					errs <- err
					return
				}

				t.storeDownloaded(t.piecesDownloaded + 1)
				updateBitfield(t.bitfield, msg.pieceNum)
				fmt.Printf("%b\n", t.bitfield)

				if t.IsComplete() {
					cancel()
					// TODO: Anything else? Do peers have to be notified?
					return
				}

				// Reset
				currPieceSize = t.pieceSize
				currBlockSize = BLOCK_SIZE
				numBlocks = t.pieceSize / BLOCK_SIZE
				blockOffset = 0
				lastBlockOffset = (numBlocks - 1) * BLOCK_SIZE
				clear(p.data) // In case we pause in the middle of a piece

				p.num, err = nextAvailablePieceIdx(p.num, t.numPieces, peerBitfield, t.bitfield)
				if err != nil {
					errs <- err
					return
				}
			}

			reqMsg := createRequestMsg(p.num, blockOffset, currBlockSize)
			outboundMsgs <- reqMsg
			fmt.Printf("Requesting %v bytes for piece %v from %v\n", currBlockSize, p.num, peerAddr)
		case have:
			updateBitfield(peerBitfield, msg.pieceNum)
		}
	}
}

// Retrieves and sends piece data block by block
func (t *Torrent) handlePieceRequest(pieceNum int, blockSize int, outboundMsgs chan<- []byte) error {
	havePiece := bitfieldContains(t.bitfield, pieceNum)
	if !havePiece {
		outboundMsgs <- createChokeMsg()
		return PieceNotOnDiskErr
	}

	currPieceSize := t.pieceSize
	if pieceNum == t.numPieces-1 {
		currPieceSize = t.totalSize - pieceNum*t.pieceSize
	}

	p := newPieceData(currPieceSize)
	t.getPieceFromDisk(p)
	blocks := p.splitIntoBlocks(t, blockSize)

	offset := 0
	for _, block := range blocks {
		outboundMsgs <- createPieceMsg(pieceNum, offset, block)
		offset += blockSize
	}

	return nil
}

func (t *Torrent) handleConn(conn net.Conn, myPeerId []byte, errs chan<- error, ctx context.Context, cancel context.CancelFunc) {
	defer conn.Close()

	parsedMessages := make(chan peerMessage, 10)
	defer close(parsedMessages)
	outboundMsgs := make(chan []byte, 10)

	peer := conn.RemoteAddr().String()
	go t.chooseResponse(peer, outboundMsgs, parsedMessages, errs, cancel)

	log.Println("Connected to peer", peer)

	handshakeMsg := createHandshakeMsg(t.infoHash, myPeerId)
	if _, err := conn.Write(handshakeMsg); err != nil {
		errs <- err
		return
	}

	msgBuf := make([]byte, 32*1024)
	msgBufCursor := 0

	// Include piece message header
	const MAX_RESPONSE_SIZE = BLOCK_SIZE + 13
	tempBuf := make([]byte, MAX_RESPONSE_SIZE)

	// Generally 2 minutes: https://wiki.theory.org/BitTorrentSpecification#keep-alive:_.3Clen.3D0000.3E
	timeout := 5 * time.Second
	timer := time.NewTimer(timeout)

	for {
		select {
		case msg, open := <-outboundMsgs:
			if !open {
				// log.Println("chooseResponse signaled to drop connection to", peer)
				return
			}

			if _, err := conn.Write(msg); err != nil {
				errs <- err
				return
			}
		case <-timer.C:
			errs <- errors.New("Waited too long")
			return
		case <-ctx.Done():
			return
		default:
			// fmt.Println("Nothing to write")
		}

		conn.SetReadDeadline(time.Now().Add(time.Millisecond * 500))
		numRead, err := conn.Read(tempBuf)
		if errors.Is(err, os.ErrDeadlineExceeded) {
			// fmt.Println("Nothing to read")
			continue
		} else if err != nil {
			errs <- err
			return
		}

		if !timer.Stop() {
			<-timer.C
		}
		timer.Reset(timeout)

		// TODO: Use len instead
		if slices.Max(msgBuf) == 0 {
			// fmt.Println("Didn't already contain fragment(s)")
			copy(msgBuf, tempBuf)
		} else {
			// fmt.Println("Appending to earlier fragment")
			newSection := msgBuf[msgBufCursor : msgBufCursor+numRead]
			copy(newSection, tempBuf)
		}

		msgBufCursor += numRead

		// Passing a slice of msgBuf to the parser would create msg structs that get cleared too
		copyToParse := slices.Clone(msgBuf[:msgBufCursor])
		msgList, err := parseMultiMessage(copyToParse, t.infoHash)
		if err != nil {
			errs <- err
			return
		}

		firstFragmentIdx := 0
		for _, msg := range msgList {
			parsedMessages <- msg
			// fmt.Println("Parsed a", msg.kind)

			if msg.kind != fragment {
				firstFragmentIdx += msg.totalSize
			}
		}

		clear(msgBuf[:firstFragmentIdx])

		if firstFragmentIdx == msgBufCursor {
			// No fragments found
			msgBufCursor = 0
		} else if firstFragmentIdx != 0 {
			// Copy over leading 0s
			copy(msgBuf, msgBuf[firstFragmentIdx:])
			msgBufCursor -= firstFragmentIdx
		}
	}
}

func extractCompactPeers(s string) ([]string, error) {
	var list []string

	if len(s)%6 != 0 {
		return nil, errors.New("Peer information must be a multiple of 6 bytes")
	}

	for i := 0; i < len(s)-2; {
		ip := ""
		for j := 0; j < 4; j++ {
			ip += fmt.Sprintf("%d", s[i])
			if j != 3 {
				ip += "."
			}
			i++
		}
		ip += ":"

		var portVal uint16
		portBytes := []byte(s[i : i+2])
		portVal = binary.BigEndian.Uint16(portBytes)
		ip += strconv.Itoa(int(portVal))

		// Skip 2 bytes for the port
		i += 2

		list = append(list, ip)
	}

	return list, nil
}

// TODO: Implement a version that operates on a Torrent so it can be locked for thread safety
func updateBitfield(bitfield []byte, pieceNum int) {
	b := &bitfield[pieceNum/8]
	bitsFromRight := 7 - (pieceNum % 8)
	mask := uint8(0x01) << bitsFromRight
	*b |= mask
}

func bitfieldContains(bitfield []byte, pieceNum int) bool {
	b := bitfield[pieceNum/8]
	bitsFromRight := 7 - (pieceNum % 8)
	mask := uint8(0x01) << bitsFromRight
	return b&mask != 0
}

// TODO: Don't select pieces from unwanted files
// Sequentially chooses the next piece to download, assuming we want all of them
func nextAvailablePieceIdx(currPieceNum, numPieces int, peerBitfield, ourBitfield []byte) (int, error) {
	nextPieceNum := currPieceNum + 1

	for attempts := 0; attempts < numPieces; attempts++ {
		if currPieceNum == numPieces {
			nextPieceNum = 0
		}

		if bitfieldContains(peerBitfield, nextPieceNum) &&
			!bitfieldContains(ourBitfield, nextPieceNum) {
			return nextPieceNum, nil
		}
		nextPieceNum++
	}

	return 0, errors.New("Peer has no pieces we want")
}

type connState struct {
	am_choking      bool
	am_interested   bool
	peer_choking    bool
	peer_interested bool
}

func newConnState() *connState {
	return &connState{
		true,
		false,
		true,
		false,
	}
}
