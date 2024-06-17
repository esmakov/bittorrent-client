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
	"github.com/esmakov/bittorrent-client/messages"
	"github.com/esmakov/bittorrent-client/parser"
)

type Torrent struct {
	metaInfoFileName string
	trackerHostname  string
	comment          string
	infoHash         []byte
	isPrivate        bool
	piecesStr        string
	// Typical piece size for this torrent.
	// The final piece will almost certainly be smaller, however.
	pieceSize              int
	numPieces              int
	totalSize              int
	piecesDownloaded       int
	piecesUploaded         int
	bitfield               []byte
	wantedBitfield         []byte
	files                  []*TorrentFile
	lastChecked            time.Time
	dir                    string
	portForTrackerResponse string
	// Optionally sent by server
	trackerId string
	seeders   int
	leechers  int
	// TODO: state field (paused, seeding, etc), maybe related to tracker "event"

	// for connection limit on a per-torrent basis
	// numInterested int
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
	bFiles, ok := bInfo["files"]
	if ok {
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
		metaInfoFileName:       metaInfoFileName,
		trackerHostname:        announce,
		infoHash:               infoHash,
		comment:                comment,
		isPrivate:              isPrivate == 1,
		piecesStr:              bPieces,
		pieceSize:              bPieceLength,
		numPieces:              numPieces,
		totalSize:              totalSize,
		bitfield:               make([]byte, bitfieldLen),
		wantedBitfield:         make([]byte, bitfieldLen),
		files:                  files,
		dir:                    dir,
		portForTrackerResponse: getNextFreePort(),
	}

	t.SetWantedBitfield()

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

// Returns a slice where the index corresponds to the piece number, and the value is true if the piece is wanted.
func (t *Torrent) getWantedPieceNums() []bool {
	// pieces are 0-indexed
	wantedPieces := make([]bool, t.numPieces+1)

	filePtr := int64(0)
	for _, file := range t.Files() {
		filePtr += file.finalSize
		if !file.Wanted {
			continue
		}
		/*
			                     filePtr=85            filePtr = 140
			NNNNNNNNNNNNNNNNNNNNNN|YYYYYYYYYYYYYYYYYYYY|NNNNNNNNNNNNNNNNNN
			unwanted piece  | piece 3 | piece 4 | piece 5 |
			                75          100         125
		*/

		firstWantedPieceIdx := (filePtr / int64(t.pieceSize)) * int64(t.pieceSize)
		firstWantedPieceNum := firstWantedPieceIdx / int64(t.pieceSize)

		fileEndPtr := filePtr + file.finalSize
		lastWantedPieceIdx := (fileEndPtr / int64(t.pieceSize)) * int64(t.pieceSize)
		lastWantedPieceNum := lastWantedPieceIdx / int64(t.pieceSize)

		for num := firstWantedPieceNum; num <= lastWantedPieceNum; num++ {
			wantedPieces[num] = true
		}
	}
	return wantedPieces
}

// Populates a bitfield representing the pieces the user wants.
func (t *Torrent) SetWantedBitfield() {
	for i, bool := range t.getWantedPieceNums() {
		if bool {
			updateBitfield(t.wantedBitfield, i)
		}
	}
}

/*
Opens the TorrentFiles for writing and returns the ones that have been modified since the torrent was last checked.

FIXME: Since a new torrent instance is created and destroyed each run, the
lastChecked field doesn't persist and we aren't able to save time by not
checking files. As a result, all the TorrentFiles are returned.

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

const (
	CLIENT_BLOCK_SIZE = 16 * 1024

	/*
	   "Implementer's Note: Even 30 peers is plenty, the official client version 3
	   in fact only actively forms new connections if it has less than 30 peers and
	   will refuse connections if it has 55. This value is important to performance.
	   When a new piece has completed download, HAVE messages (see below) will need
	   to be sent to most active peers. As a result the cost of broadcast traffic
	   grows in direct proportion to the number of peers. Above 25, new peers are
	   highly unlikely to increase download speed. UI designers are strongly advised
	   to make this obscure and hard to change as it is very rare to be useful to do so." */
	EFFECTIVE_MAX_PEER_CONNS = 25

	// TODO: Expose to user
	USER_DESIRED_PEER_CONNS = 1

	DIAL_TIMEOUT    = 2 * time.Second
	MESSAGE_TIMEOUT = 5 * time.Second
	// Decides how long handleConn waits for incoming messages (from the peer)
	// before checking for outbound messages (from chooseResponse)
	// or whether MESSAGE_TIMEOUT has expired
	CONN_READ_INTERVAL = 100 * time.Millisecond

	CLIENT_PEER_ID = "edededededededededed"
)

func (t *Torrent) GetPeers() ([]string, error) {
	trackerResponse, err := t.sendTrackerMessage(startedEvent)
	if err != nil {
		return nil, err
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
	return extractCompactPeers(peersStr)
}

/*
	https://wiki.theory.org/BitTorrentSpecification#Tracker_HTTP.2FHTTPS_Protocol

"event: If specified, must be one of started, completed, stopped, (or empty which is the same as not being specified). If not specified, then this request is one performed at regular intervals.

started: The first request to the tracker must include the event key with this value.

stopped: Must be sent to the tracker if the client is shutting down gracefully.

completed: Must be sent to the tracker when the download completes. However, must not be sent if the download was already 100% complete when the client started. Presumably, this is to allow the tracker to increment the "completed downloads" metric based solely on this event."
*/
type trackerEventKinds string

const (
	startedEvent   trackerEventKinds = "started"
	stoppedEvent   trackerEventKinds = "stopped"
	completedEvent trackerEventKinds = "completed"
)

/*
StartConns sends the "started" message to the tracker and then runs for the lifetime of the program.

StartConns will attempt to net.Dial as many connections as desired by the user, as long as there are enough peers in the swarm. It also kicks off a separate goroutine that listens for incoming connections.
*/
func (t *Torrent) StartConns(peerList []string) error {
	if len(peerList) == 0 {
		// TODO: Keep polling tracker for peers in a separate goroutine
	}

	maxPeers := min(len(peerList), USER_DESIRED_PEER_CONNS, EFFECTIVE_MAX_PEER_CONNS)
	errs := make(chan error, maxPeers)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	numPeers := 0
	for numPeers < maxPeers {
		// TODO: Choose more intelligently
		conn, err := net.DialTimeout("tcp", getRandPeer(peerList), DIAL_TIMEOUT)
		if err != nil {
			log.Println(err)
			continue
		}

		go t.handleConn(ctx, cancel, conn, errs)
		numPeers++
	}

	numPeersChan := make(chan int, 2)
	go t.acceptConns(ctx, numPeers, maxPeers, numPeersChan, errs)

	for {
		select {
		// NOTE: Make sure errors are only sent when goroutines exit
		// so we don't go over EFFECTIVE_MAX_PEER_CONNS
		case n := <-numPeersChan:
			numPeers = n
		case e := <-errs:
			if errors.Is(e, messages.ErrBadInfoHash) || errors.Is(e, ErrBadPieceHash) || errors.Is(e, messages.ErrUnsupportedProtocol) {
				// TODO: Add to blacklist
			}
			numPeers--
			numPeersChan <- numPeers
			log.Println("Conn dropped:", e)

			conn, err := net.DialTimeout("tcp", getRandPeer(peerList), DIAL_TIMEOUT)
			if err != nil {
				log.Println(err)
				continue
			}
			go t.handleConn(ctx, cancel, conn, errs)
			numPeers++
			numPeersChan <- numPeers
		case <-ctx.Done():
			fmt.Println("COMPLETED", t.metaInfoFileName)

			// NOTE: Tracker only expects this once
			// Make sure this doesn't happen if the download started at 100% already
			t.sendTrackerMessage(completedEvent)
			return nil
		default:
			// Multiple successive errors occurred to get here
			if numPeers < maxPeers {
				conn, err := net.DialTimeout("tcp", getRandPeer(peerList), DIAL_TIMEOUT)
				if err != nil {
					log.Println(err)
					continue
				}
				go t.handleConn(ctx, cancel, conn, errs)
				numPeers++
				numPeersChan <- numPeers
			}
		}
	}
}

func (t *Torrent) acceptConns(ctx context.Context, numPeers, maxPeers int, numPeersChan chan int, errs chan error) {
	listenAddr, err := net.ResolveTCPAddr("tcp", fmt.Sprint(":", t.portForTrackerResponse))
	if err != nil {
		log.Println(err)
	}

	listener, err := net.ListenTCP("tcp", listenAddr)
	if err != nil {
		log.Println(err)
	}
	defer listener.Close()

	println("Listening...")

	for {
		select {
		case n := <-numPeersChan:
			numPeers = n
		case <-ctx.Done():
			log.Println("No longer accepting conns")
			return
		default:
		}
		if numPeers < maxPeers {
			conn, err := listener.AcceptTCP()
			if err != nil {
				log.Println("Accept:", err)
				continue
			}
			log.Printf("Incoming conn from %v\n", conn.RemoteAddr())
			newCtx, newCancel := context.WithCancel(ctx)
			go t.handleConn(newCtx, newCancel, conn, errs)
			numPeers++
			numPeersChan <- numPeers
		}
	}
}

func (t *Torrent) sendTrackerMessage(event trackerEventKinds) (map[string]any, error) {
	reqURL := url.URL{
		Opaque: t.trackerHostname,
	}
	queryParams := url.Values{}
	queryParams.Set("peer_id", CLIENT_PEER_ID)
	queryParams.Set("port", t.portForTrackerResponse)
	queryParams.Set("uploaded", strconv.Itoa(t.piecesUploaded))
	queryParams.Set("downloaded", strconv.Itoa(t.piecesDownloaded))
	queryParams.Set("left", strconv.Itoa(t.numPieces-t.piecesDownloaded))
	queryParams.Set("event", string(event))
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

var (
	ErrPieceNotOnDisk = errors.New("Piece not wanted or not downloaded yet")
	ErrBadPieceHash   = errors.New("Piece failed hash check")
	ErrNoUsefulPieces = errors.New("Peer has no pieces we want")
)

// Makes sure existing data on disk is verified when the user adds a torrent.
// TODO: Do in parallel?
func (t *Torrent) CheckAllPieces(files []*TorrentFile) ([]int, error) {
	p := newPieceData(t.pieceSize)
	var existingPieces []int

	for p.num = range t.numPieces {
		err := t.readPieceFromDisk(p)
		if err == ErrPieceNotOnDisk {
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
				return existingPieces, ErrBadPieceHash
			}
			// Piece hasn't been downloaded but some pieces ahead have
			continue
		}

		// NOTE: Piece could be empty and still correct at this point, if intentionally so
		t.updateBitfield(p.num)
		t.piecesDownloaded++
		existingPieces = append(existingPieces, p.num)
		clear(p.data)
	}

	t.lastChecked = time.Now()
	return existingPieces, nil
}

type FileIOOperation func(f *os.File, b []byte, off int64) (n int, err error)

var (
	writeOp FileIOOperation = (*os.File).WriteAt
	readOp  FileIOOperation = (*os.File).ReadAt
)

func (t *Torrent) readOrWritePiece(op FileIOOperation, p *pieceData) error {
	currPieceSize := t.pieceSize
	if p.num == t.numPieces-1 {
		currPieceSize = t.totalSize - p.num*t.pieceSize
	}

	// All indices are relative to the "stream" of pieces and may cross file boundaries
	pieceStartIdx := int64(p.num * t.pieceSize)
	pieceEndIdx := pieceStartIdx + int64(currPieceSize)

	if len(t.files) == 1 {
		fileInfo, err := os.Stat(t.files[0].Path)
		if err != nil {
			return err
		}

		if &op == &readOp {
			if pieceStartIdx >= fileInfo.Size() {
				return ErrPieceNotOnDisk
			}
		}
		_, err = op(t.files[0].fd, p.data[:currPieceSize], pieceStartIdx)
		return err
	}

	var (
		fileStartIdx       = int64(0)
		remainingPieceSize = currPieceSize
		startOpAt          = 0
	)

	for _, currFile := range t.files {
		if &op == &readOp {
			if !currFile.Wanted {
				continue
			}
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
					return ErrPieceNotOnDisk
				}
			} else {
				if pieceOffsetIntoFile >= currFile.finalSize {
					panic("Unreachable")
				}
			}

			boundedByFile := pieceStartIdx >= fileStartIdx && pieceEndIdx <= fileEndIdx
			crossesFileBoundary := pieceStartIdx >= fileStartIdx && pieceEndIdx > fileEndIdx
			if boundedByFile || remainingPieceSize < currPieceSize {
				if remainingPieceSize < currPieceSize {
					opSize := min(int(currFile.finalSize), remainingPieceSize)
					if _, err := op(currFile.fd, p.data[startOpAt:startOpAt+opSize], 0); err != nil {
						return err
					}
					remainingPieceSize -= opSize
					startOpAt += opSize
					if remainingPieceSize < 0 {
						panic("Unreachable: remainingPieceSize < 0")
					}

					if remainingPieceSize > 0 {
						// Move on to yet another (at least a third) file to finish this piece
						fileStartIdx += currFile.finalSize
						break
					}

					if startOpAt != currPieceSize {
						panic("Unreachable: Piece fragments do not add up to a whole piece")
					}

				} else {
					if _, err := op(currFile.fd, p.data[:currPieceSize], pieceOffsetIntoFile); err != nil {
						return err
					}
				}

				return nil
			} else if crossesFileBoundary {
				if pieceStartIdx < fileStartIdx {
					panic("bytesFromEnd will be calculated too high")
				}

				// TODO: Shouldn't have to subslice as long as the caller initializes the pieceData with correct size
				if _, err := op(currFile.fd, p.data[:bytesFromEOF], pieceOffsetIntoFile); err != nil {
					return err
				}

				remainingPieceSize -= int(bytesFromEOF)
				startOpAt += int(bytesFromEOF)
				fileStartIdx += currFile.finalSize
				break
			} else {
				panic("Unreachable")
			}
		}
	}

	return nil
}

// Writes retrieved data into the provided pieceData, assuming the piece number is initialized and in a file we want
func (t *Torrent) readPieceFromDisk(p *pieceData) error {
	currPieceSize := t.pieceSize
	if p.num == t.numPieces-1 {
		currPieceSize = t.totalSize - p.num*t.pieceSize
	}

	// All indices are relative to the "stream" of pieces and may cross file boundaries
	pieceStartIdx := int64(p.num * t.pieceSize)
	pieceEndIdx := pieceStartIdx + int64(currPieceSize)

	if len(t.files) == 1 {
		fileInfo, err := os.Stat(t.files[0].Path)
		if err != nil {
			return err
		}

		if pieceStartIdx >= fileInfo.Size() {
			return ErrPieceNotOnDisk
		}
		_, err = t.files[0].fd.ReadAt(p.data[:currPieceSize], pieceStartIdx)
		return err
	}

	var (
		fileStartIdx       = int64(0)
		remainingPieceSize = currPieceSize
		startReadAt        = 0
	)

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
					return ErrPieceNotOnDisk
				}
			} else {
				if pieceOffsetIntoFile >= currFile.finalSize {
					panic("Unreachable")
				}
			}

			boundedByFile := pieceStartIdx >= fileStartIdx && pieceEndIdx <= fileEndIdx
			crossesFileBoundary := pieceStartIdx >= fileStartIdx && pieceEndIdx > fileEndIdx

			if boundedByFile || remainingPieceSize < currPieceSize {
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
			} else if crossesFileBoundary {
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

// Reads data from the provided pieceData and writes it, assuming the piece number is set correctly
func (t *Torrent) writePieceToDisk(p *pieceData) error {
	thisPieceSize := t.pieceSize
	if p.num == t.numPieces-1 {
		thisPieceSize = t.totalSize - p.num*t.pieceSize
	}

	// All piece indices are relative to the "stream" of pieces and may cross file boundaries
	pieceStartIdx := int64(p.num * t.pieceSize)
	pieceEndIdx := pieceStartIdx + int64(thisPieceSize)

	if len(t.files) == 1 {
		_, err := t.files[0].fd.WriteAt(p.data[:thisPieceSize], pieceStartIdx)
		return err
	}

	var (
		fileStartIdx       = int64(0)
		remainingPieceSize = thisPieceSize
		startWriteAt       = 0
	)

	for _, currFile := range t.files {
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

			boundedByFile := pieceStartIdx >= fileStartIdx && pieceEndIdx <= fileEndIdx
			crossesFileBoundary := pieceStartIdx >= fileStartIdx && pieceEndIdx > fileEndIdx

			if boundedByFile || remainingPieceSize < thisPieceSize {
				if remainingPieceSize < thisPieceSize {
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

					if startWriteAt != thisPieceSize {
						panic("Piece fragments do not add up to a whole piece")
					}

				} else {
					if _, err := currFile.fd.WriteAt(p.data[:thisPieceSize], pieceOffsetIntoFile); err != nil {
						return err
					}
				}

				return nil
			} else if crossesFileBoundary {
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

// NOTE: Assumes there is at least one peer returned from the tracker.
func getRandPeer(peerList []string) string {
	return peerList[rand.Intn(len(peerList))]
}

type pieceData struct {
	num  int
	data []byte
}

func newPieceData(pieceSize int) *pieceData {
	data := make([]byte, pieceSize)
	return &pieceData{data: data}
}

func (p *pieceData) storeBlockIntoPiece(msg messages.PeerMessage, blockSize int) {
	block := p.data[msg.BlockOffset : msg.BlockOffset+blockSize]
	// TODO: Don't copy each block twice
	copy(block, msg.BlockData)
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

/*
chooseResponse runs for the lifetime of a connection, receiving parsed messages from the handleConn goroutine for a particular peer connection. It sends back a byte response on outboundMsgs to be written.

It also verifies the piece hash and calls writePieceToDisk if correct.
*/
func (t *Torrent) chooseResponse(peerAddr string, outboundMsgs chan<- []byte, parsedMsgs <-chan messages.PeerMessage, errs chan<- error, cancel context.CancelFunc) {
	defer close(outboundMsgs)

	var (
		p             = newPieceData(t.pieceSize)
		connState     = newConnState()
		peerBitfield  = make([]byte, int(math.Ceil(float64(t.numPieces)/8.0)))
		currPieceSize = t.pieceSize
		currBlockSize = CLIENT_BLOCK_SIZE
		blockOffset   = 0
	)

	for {
		msg, open := <-parsedMsgs
		if !open {
			// log.Println("handleConn signaled to drop connection to", peerAddr)
			return
		}

		switch msg.Kind {
		case messages.Handshake:
			// fmt.Printf("%v has peer id %v\n", peerAddr, string(msg.peerId))
			outboundMsgs <- messages.CreateBitfieldMsg(t.bitfield)
			fmt.Printf("INFO: Sending bitfield to %v\n", peerAddr)

			// TODO: Determine if we want to talk to this peer
			outboundMsgs <- messages.CreateUnchokeMsg()
		case messages.Request:
			err := t.handlePieceRequest(msg.PieceNum, msg.BlockSize, outboundMsgs)
			if err != nil {
				// Drop connection?
				log.Println("Error retrieving piece:", err)
			}
		case messages.Keepalive:
		case messages.Choke:
			connState.peer_choking = true
		case messages.Unchoke:
			connState.peer_choking = false
		case messages.Interested:
			connState.peer_interested = true
		case messages.Uninterested:
			connState.peer_interested = false
		case messages.Bitfield:
			// if connState.peer_choking {
			// 	continue
			// }
			peerBitfield = msg.Bitfield

			outboundMsgs <- messages.CreateInterestedMsg()
			fmt.Printf("Sending interested, request to %v\n", peerAddr)

			// Start wherever for now
			var err error
			p.num, err = t.selectNextPiece(rand.Intn(t.numPieces), peerBitfield)
			if err != nil {
				errs <- err
				return
			}

			reqMsg := messages.CreateRequestMsg(uint32(p.num), uint32(blockOffset), CLIENT_BLOCK_SIZE)
			outboundMsgs <- reqMsg
			fmt.Printf("Requesting piece %v from %v\n", p.num, peerAddr)
		case messages.Piece:
			p.storeBlockIntoPiece(msg, currBlockSize)
			blockOffset += CLIENT_BLOCK_SIZE

			if p.num == t.numPieces-1 {
				currPieceSize = t.totalSize - p.num*t.pieceSize
			}

			if blockOffset == currPieceSize {
				fmt.Printf("Completed piece %v, checking...\n", p.num)
				correct, err := t.checkPieceHash(p)
				if err != nil {
					errs <- err
					return
				}

				if !correct {
					errs <- ErrBadPieceHash
					return
				}

				// TODO: Send all connected peers a 'have' msg when a piece is downloaded
				if err = t.writePieceToDisk(p); err != nil {
					errs <- err
					return
				}

				t.storeDownloaded(t.piecesDownloaded + 1)
				t.updateBitfield(msg.pieceNum)
				fmt.Printf("%b\n", t.bitfield)

				if t.IsComplete() {
					cancel()
					return
				}

				// Reset for next piece
				currPieceSize = t.pieceSize
				currBlockSize = CLIENT_BLOCK_SIZE
				blockOffset = 0

				p.num, err = t.selectNextPiece(p.num, peerBitfield)
				if err != nil {
					errs <- err
					return
				}
			} else {
				// Incomplete piece
				remaining := currPieceSize - blockOffset
				if remaining < CLIENT_BLOCK_SIZE {
					currBlockSize = remaining
				}
				// TODO: Make a progress bar
				fmt.Println(blockOffset, "/", currPieceSize, "bytes")
			}

			reqMsg := messages.CreateRequestMsg(uint32(p.num), uint32(blockOffset), uint32(currBlockSize))
			outboundMsgs <- reqMsg
		case messages.Have:
			setBitfield(peerBitfield, msg.PieceNum)
		}
	}
}

// Retrieves and sends piece data block by block
func (t *Torrent) handlePieceRequest(pieceNum int, blockSize int, outboundMsgs chan<- []byte) error {
	havePiece := bitfieldContains(t.bitfield, pieceNum)
	if !havePiece {
		outboundMsgs <- messages.CreateChokeMsg()
		return ErrPieceNotOnDisk
	}

	currPieceSize := t.pieceSize
	if pieceNum == t.numPieces-1 {
		currPieceSize = t.totalSize - pieceNum*t.pieceSize
	}

	p := newPieceData(currPieceSize)
	t.readPieceFromDisk(p)
	blocks := p.splitIntoBlocks(t, blockSize)

	offset := 0
	for _, block := range blocks {
		outboundMsgs <- messages.CreatePieceMsg(uint32(pieceNum), uint32(offset), block)
		offset += blockSize
	}

	t.storeUploaded(t.piecesUploaded + 1)

	return nil
}

/*
handleConn reads from the connection into a temporary buffer. It then uses a memory arena to piece together fragments of peer messages.

After parsing, structured messages are sent to another goroutine, which constructs a response to be returned to handleConn for writing.
*/
func (t *Torrent) handleConn(ctx context.Context, cancel context.CancelFunc, conn net.Conn, errs chan<- error) {
	defer conn.Close()

	parsedMessages := make(chan messages.PeerMessage, 10)
	defer close(parsedMessages)
	outboundMsgs := make(chan []byte, 10)

	peer := conn.RemoteAddr().String()
	go t.chooseResponse(peer, outboundMsgs, parsedMessages, errs, cancel)

	log.Println("Connected to peer", peer)

	handshakeMsg := messages.CreateHandshakeMsg(t.infoHash, []byte(CLIENT_PEER_ID))
	if _, err := conn.Write(handshakeMsg); err != nil {
		errs <- err
		return
	}

	msgBuf := make([]byte, 0, 32*1024)

	// Account for 'piece' message header
	const MAX_RESPONSE_SIZE = CLIENT_BLOCK_SIZE + 13
	tempBuf := make([]byte, MAX_RESPONSE_SIZE)

	// Generally 2 minutes: https://wiki.theory.org/BitTorrentSpecification#keep-alive:_.3Clen.3D0000.3E
	timer := time.NewTimer(MESSAGE_TIMEOUT)

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
			errs <- errors.New("Message timeout expired")
			return
		case <-ctx.Done():
			return
		default:
			// fmt.Println("Nothing to write")
		}

		conn.SetReadDeadline(time.Now().Add(CONN_READ_INTERVAL))

		numRead, err := conn.Read(tempBuf)
		if errors.Is(err, os.ErrDeadlineExceeded) {
			// Nothing to read, loop back to see if an outbound message is pending
			continue
		} else if err != nil {
			errs <- err
			return
		}

		if !timer.Stop() {
			<-timer.C
		}
		timer.Reset(MESSAGE_TIMEOUT)

		msgBuf = msgBuf[:len(msgBuf)+numRead]
		copy(msgBuf[len(msgBuf)-numRead:], tempBuf)

		// Passing a slice of msgBuf to the parser would create msg structs that get cleared too
		copyToParse := slices.Clone(msgBuf)
		msgList, err := messages.ParseMultiMessage(copyToParse, t.infoHash)
		if err != nil {
			errs <- err
			return
		}

		firstFragmentIdx := 0
		for _, msg := range msgList {
			parsedMessages <- msg
			if msg.Kind != messages.Fragment {
				fmt.Printf("INFO: %v sent a %v\n", conn.RemoteAddr(), msg.Kind)
			}

			if msg.Kind != messages.Fragment {
				firstFragmentIdx += msg.TotalSize
			}
		}

		if firstFragmentIdx == len(msgBuf) {
			// No fragments found
			msgBuf = msgBuf[:0]
		} else if firstFragmentIdx != 0 {
			// "Shift left"
			copy(msgBuf, msgBuf[firstFragmentIdx:])
			msgBuf = msgBuf[:len(msgBuf)-firstFragmentIdx]
		}
	}
}

// Fulfills BEP 23: Tracker Returns Compact Peer Lists
// http://bittorrent.org/beps/bep_0023.html
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

func (t *Torrent) updateBitfield(pieceNum int) {
	t.Lock()
	defer t.Unlock()
	updateBitfield(t.bitfield, pieceNum)
}

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

// Sequentially chooses the next piece to download, based on which ones
// the peer has, the one we have, and the files the user wants
func (t *Torrent) selectNextPiece(currPieceNum int, peerBitfield []byte) (int, error) {
	nextPieceNum := currPieceNum + 1

	for range t.numPieces {
		if currPieceNum == t.numPieces {
			nextPieceNum = 0
		}

		if bitfieldContains(peerBitfield, nextPieceNum) &&
			bitfieldContains(t.wantedBitfield, nextPieceNum) &&
			!bitfieldContains(t.bitfield, nextPieceNum) {
			return nextPieceNum, nil
		}
		nextPieceNum++
	}

	return 0, ErrNoUsefulPieces
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

// TODO: Do we need more than one?
func getNextFreePort() string {
	return "6881"
}
