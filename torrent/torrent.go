package torrent

import (
	"bytes"
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

type TorrentMetaInfo struct {
	MetaInfoFileName string
	trackerHostname  string
	comment          string
	infoHash         []byte
	isPrivate        bool
	piecesStr        string
	totalSize        int
	pieceSize        int // Size of all but the last piece
	numPieces        int
	dir              string
}

type TorrentOptions struct {
	UserDesiredConns int
}

type Torrent struct {
	TorrentMetaInfo
	TorrentOptions

	numUploadedBytes       int
	bitfield               []byte
	wantedBitfield         []byte
	Files                  []*TorrentFile
	lastChecked            time.Time
	portForTrackerResponse string
	activeConns            map[string]net.Conn

	// Optionally sent by server
	trackerId string
	seeders   int
	leechers  int

	// TODO: state field (paused, seeding, etc), maybe related to tracker "event"

	// for connection limit on a per-torrent basis
	// numInterested int

	sync.Mutex

	Logbuf bytes.Buffer
	Logger log.Logger
}

type TorrentFile struct {
	fd        *os.File
	Path      string
	finalSize int64
	Wanted    bool
}

// Initializes the Torrent state with nil file descriptors and no notion of which files are wanted (user input has not been read yet)
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
		TorrentMetaInfo: TorrentMetaInfo{
			MetaInfoFileName: metaInfoFileName,
			trackerHostname:  announce,
			infoHash:         infoHash,
			comment:          comment,
			isPrivate:        isPrivate == 1,
			piecesStr:        bPieces,
			pieceSize:        bPieceLength,
			numPieces:        numPieces,
			totalSize:        totalSize,
			dir:              dir,
		},
		bitfield:               make([]byte, bitfieldLen),
		wantedBitfield:         make([]byte, bitfieldLen),
		activeConns:            map[string]net.Conn{},
		Files:                  files,
		portForTrackerResponse: getNextFreePort(),
	}

	t.Logger = *log.New(&t.Logbuf, "DEBUG: ", log.LstdFlags)

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
	t.numUploadedBytes = n
}

func (t *Torrent) numBytesDownloaded() int {
	n := 0
	for _, b := range t.bitfield {
		n += PopCount(b)
	}

	if bitfieldContains(t.bitfield, t.numPieces-1) {
		sizeOfLast := t.totalSize - ((t.numPieces - 1) * t.pieceSize)
		return (n-1)*t.pieceSize + sizeOfLast
	}

	return n * t.numPieces
}

func (t *Torrent) safeSetBitfield(pieceNum int) {
	t.Lock()
	defer t.Unlock()
	setBitfield(t.bitfield, pieceNum)
}

// Modifies the given bitfield in place, whether it represents the pieces we have, the pieces we want, or the pieces a peer has.
// Use safeSetBitfield for thread safety when updating our Torrent.
func setBitfield(bitfield []byte, pieceNum int) {
	b := &bitfield[pieceNum/8]
	bitsFromRight := 7 - (pieceNum % 8)
	mask := uint8(0x01) << bitsFromRight
	*b |= mask
}

func clearBitfield(bitfield []byte, pieceNum int) {
	b := &bitfield[pieceNum/8]

	l := [8]int{
		0b01111111,
		0b10111111,
		0b11011111,
		0b11101111,
		0b11110111,
		0b11111011,
		0b11111101,
		0b11111110,
	}

	*b &= byte(l[pieceNum%8])
}

func bitfieldContains(bitfield []byte, pieceNum int) bool {
	b := bitfield[pieceNum/8]
	bitsFromRight := 7 - (pieceNum % 8)
	mask := uint8(0x01) << bitsFromRight
	return b&mask != 0
}

func (t *Torrent) IsComplete() bool {
	return t.numBytesDownloaded() == t.totalSize
}

func (t *Torrent) String() string {
	sb := strings.Builder{}
	if t.isPrivate {
		sb.WriteString(fmt.Sprint("PRIVATE TORRENT"))
	}

	numWanted := 0
	for _, file := range t.Files {
		if file.Wanted {
			numWanted++
		}
	}

	sb.WriteString(fmt.Sprint(
		fmt.Sprintln("--------------Torrent Info--------------"),
		fmt.Sprintln("Torrent file:", t.MetaInfoFileName),
		fmt.Sprintln("Tracker:", t.trackerHostname),
		fmt.Sprintln("Total size:", t.totalSize),
		fmt.Sprintf("Selected %v of %v file(s):\n", numWanted, len(t.Files)),
	))

	for _, file := range t.Files {
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

// Returns a bool slice of wanted pieces, based on which files the user wants. The slice index corresponds to the 0-indexed piece numbers.
func (t *Torrent) getWantedPieces() []bool {
	wantedPieces := make([]bool, t.numPieces)

	fileEndIdx := int64(0)
	for _, file := range t.Files {
		fileEndIdx += file.finalSize

		if !file.Wanted {
			continue
		}
		/*
			                     filePtr=85            filePtr = 140
			NNNNNNNNNNNNNNNNNNNNNN|YYYYYYYYYYYYYYYYYYYY|NNNNNNNNNNNNNNNNNN
			unwanted piece  | piece 3 | piece 4 | piece 5 |
			                75        100       125
		*/

		fileStartIdx := fileEndIdx - file.finalSize

		firstWantedPieceIdx := (fileStartIdx / int64(t.pieceSize)) * int64(t.pieceSize)
		firstWantedPieceNum := firstWantedPieceIdx / int64(t.pieceSize)

		lastWantedPieceIdx := ((fileEndIdx - 1) / int64(t.pieceSize)) * int64(t.pieceSize)
		lastWantedPieceNum := lastWantedPieceIdx / int64(t.pieceSize)
		// if lastWantedPieceNum == int64(t.numPieces) {
		// 	lastWantedPieceNum = int64(t.numPieces) - 1
		// }

		for num := firstWantedPieceNum; num <= lastWantedPieceNum; num++ {
			wantedPieces[num] = true
		}
	}
	return wantedPieces
}

// Populates a bitfield representing the pieces the user wants.
// Doesn't need to acquire a lock because it only runs on initialization (i.e. we don't support user changing desired pieces mid-download)
func (t *Torrent) SetWantedBitfield() {
	for i, bool := range t.getWantedPieces() {
		if bool {
			setBitfield(t.wantedBitfield, i)
		}
	}
}

func PopCount(b byte) int {
	count := 0
	for b > 0 {
		count += int(b & 1)
		b >>= 1
	}
	return count
}

/*
Opens the TorrentFiles' descriptors for writing and returns the ones that have been modified since the torrent was last checked.

FIXME: Since a new torrent instance is created and destroyed each run, the lastChecked field doesn't persist
and we aren't able to save time by not checking files. As a result, all the TorrentFiles are returned.

NOTE: Prior to calling this, torrentFiles have a nil file descriptior.
*/
func (t *Torrent) OpenOrCreateFiles() ([]*TorrentFile, error) {
	var filesToCheck []*TorrentFile

	if t.dir != "" {
		if err := os.Mkdir(t.dir, 0o766); err != nil {
			if !errors.Is(err, os.ErrExist) {
				return nil, err
			}
		}
	}

	for _, file := range t.Files {
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

	PIECE_MESSAGE_HEADER_SIZE = 13

	MAX_MESSAGE_SIZE = CLIENT_BLOCK_SIZE + PIECE_MESSAGE_HEADER_SIZE

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

	DIAL_TIMEOUT = 2 * time.Second

	// Generally 2 minutes: https://wiki.theory.org/BitTorrentSpecification#keep-alive:_.3Clen.3D0000.3E
	MESSAGE_TIMEOUT = 5 * time.Second

	// Decides how long handleConn waits for incoming messages (from the peer)
	// before checking for outbound messages (from chooseResponse)
	// or whether MESSAGE_TIMEOUT has expired
	// Note: directly affects throughput
	CONN_READ_INTERVAL = 100 * time.Millisecond

	CLIENT_PEER_ID = "edededededededededed"
)

func (t *Torrent) GetPeersFromTracker() ([]string, error) {
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

	peersStr, ok := trackerResponse["peers"].(string)
	if !ok {
		return nil, errors.New("No peers for this torrent")
	}
	return extractCompactPeers(peersStr)
}

/*
	https://wiki.theory.org/BitTorrentSpecification#Tracker_HTTP.2FHTTPS_Protocol

"event: If specified, must be one of started, completed, stopped, (or empty which is the same as not being specified). If not specified, then this request is one performed at regular intervals.
*/
type trackerEventKinds string

const (
	// started: The first request to the tracker must include the event key with this value.
	startedEvent trackerEventKinds = "started"

	// stopped: Must be sent to the tracker if the client is shutting down gracefully.
	stoppedEvent trackerEventKinds = "stopped"

	/* completed:
	   Must be sent to the tracker when the download completes.
	   However, must not be sent if the download was already 100% complete when the client started.
	   Presumably, this is to allow the tracker to increment the "completed downloads" metric based solely on this event."
	*/
	completedEvent trackerEventKinds = "completed"
)

/*
StartConns sends the "started" message to the tracker and then runs for the lifetime of the program.

Will attempt to start as many connections as desired by the user, as long as there are enough peers in the swarm.

It also kicks off a separate connection listener goroutine.
*/
func (t *Torrent) StartConns(peerList []string, userDesiredConns int) error {
	if len(peerList) == 0 {
		// TODO: Keep polling tracker for peers in a separate goroutine
		return errors.New("Tracker reports no peers")
	}

	maxPeers := min(len(peerList), userDesiredConns, EFFECTIVE_MAX_PEER_CONNS)
	errs := make(chan error, maxPeers)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go t.acceptConns(ctx, maxPeers, errs)

	for {
		select {
		case e := <-errs:
			if errors.Is(e, messages.ErrBadInfoHash) || errors.Is(e, ErrBadPieceHash) || errors.Is(e, messages.ErrUnsupportedProtocol) {
				// TODO: Add to blacklist
			}

			log.Println("Conn dropped:", e)
		case <-ctx.Done():
			fmt.Println("COMPLETED", t.MetaInfoFileName)

			// NOTE: Tracker only expects this once
			// Make sure this doesn't happen if the download started at 100% already
			t.sendTrackerMessage(completedEvent)
			return nil
		default:
			// Try to make more connections
			t.Lock()

			if len(t.activeConns) > maxPeers {
				panic("Over conn limit")
			}

			if len(t.activeConns) == maxPeers {
				t.Unlock()
				continue
			}

			conn, err := net.DialTimeout("tcp", getRandPeer(peerList), DIAL_TIMEOUT)
			if err != nil {
				log.Println(err)
				t.Unlock()
				continue
			}

			t.activeConns[conn.RemoteAddr().String()] = conn
			go t.handleConn(ctx, cancel, conn, errs)
			t.Unlock()
		}
	}
}

// While listening to incoming connections, acceptConns also uses numPeersChan to notify the
// StartConns goroutine when one has been accepted, so it can control the total number.

// Tries to acquire a lock on the Torrent to keep its peer count accurate.

func (t *Torrent) acceptConns(ctx context.Context, maxPeers int, errs chan error) {
	listenAddr, err := net.ResolveTCPAddr("tcp", fmt.Sprint(":", t.portForTrackerResponse))
	if err != nil {
		log.Println(err)
	}

	listener, err := net.ListenTCP("tcp", listenAddr)
	if err != nil {
		log.Println(err)
	}
	defer listener.Close()

	fmt.Println("Listening for incoming peer connections...")

	for {
		select {
		case <-ctx.Done():
			log.Println("INFO: Torrent", filepath.Base(t.MetaInfoFileName), "complete, no longer accepting conns")
			return
		default:
			t.Lock()

			if len(t.activeConns) > maxPeers {
				panic("Too many peers")
			}

			if len(t.activeConns) == maxPeers {
				t.Unlock()
				continue
			}

			if err := listener.SetDeadline(time.Now().Add(CONN_READ_INTERVAL)); err != nil {
				errs <- err
				t.Unlock()
				return
			}

			conn, err := listener.AcceptTCP()
			if errors.Is(err, os.ErrDeadlineExceeded) {
				t.Unlock()
				continue
			} else if err != nil {
				log.Println("Accept:", err)
				t.Unlock()
				continue
			}

			t.Logger.Println("Incoming conn from", conn.RemoteAddr())
			t.activeConns[conn.RemoteAddr().String()] = conn

			newCtx, newCancel := context.WithCancel(ctx)
			go t.handleConn(newCtx, newCancel, conn, errs)

			t.Unlock()
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
	queryParams.Set("uploaded", strconv.Itoa(t.numUploadedBytes))
	queryParams.Set("downloaded", strconv.Itoa(t.numBytesDownloaded()))
	queryParams.Set("left", strconv.Itoa(t.totalSize-t.numBytesDownloaded()))
	queryParams.Set("event", string(event))
	queryParams.Set("compact", "1")
	if t.trackerId != "" {
		queryParams.Set("trackerid", t.trackerId)
	}

	reqURL.RawQuery = "info_hash=" + hash.URLSanitize(t.infoHash) + "&" + queryParams.Encode()

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
		return nil, errors.New("TRACKER FAILURE: " + failureReason.(string))
	}

	return response, nil
}

func (t *Torrent) checkPieceHash(p *pieceData) (bool, error) {
	givenHash, err := hash.HashSHA1(p.data[:p.ActualSize(t)])
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

/*
Reads and verifies existing data on disk when the user adds a torrent.

Also checks the number of pieces downloaded so far, which is used to determine if the torrent is complete.

TODO: Benchmark a multithreaded version
*/
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
		t.safeSetBitfield(p.num)

		existingPieces = append(existingPieces, p.num)
		clear(p.data)
	}

	t.lastChecked = time.Now()
	return existingPieces, nil
}

// Note: Assumes the piece number is initialized and in a file we want
func (t *Torrent) readPieceFromDisk(p *pieceData) error {
	currPieceSize := t.pieceSize
	if p.num == t.numPieces-1 {
		currPieceSize = t.totalSize - p.num*t.pieceSize
	}

	// All indices are relative to the "stream" of pieces and may cross file boundaries
	pieceStartIdx := int64(p.num * t.pieceSize)
	pieceEndIdx := pieceStartIdx + int64(currPieceSize)

	if len(t.Files) == 1 {
		if t.Files[0].fd == nil {
			return errors.New("Nil file descriptor")
		}

		fileInfo, err := os.Stat(t.Files[0].Path)
		if err != nil {
			return err
		}

		if pieceStartIdx >= fileInfo.Size() {
			return ErrPieceNotOnDisk
		}
		_, err = t.Files[0].fd.ReadAt(p.data[:currPieceSize], pieceStartIdx)
		return err
	}

	var (
		fileStartIdx       = int64(0)
		remainingPieceSize = currPieceSize
		startReadAt        = 0
	)

	for _, currFile := range t.Files {
		fileEndIdx := fileStartIdx + currFile.finalSize

		if !currFile.Wanted || pieceStartIdx >= fileEndIdx {
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

// Note: Assumes the piece number is set correctly (e.g. not in an unwanted file)
func (t *Torrent) writePieceToDisk(p *pieceData) error {
	currPieceSize := t.pieceSize
	if p.num == t.numPieces-1 {
		currPieceSize = t.totalSize - p.num*t.pieceSize
	}

	// All piece indices are relative to the "stream" of pieces and may cross file boundaries
	pieceStartIdx := int64(p.num * t.pieceSize)
	pieceEndIdx := pieceStartIdx + int64(currPieceSize)

	if len(t.Files) == 1 {
		_, err := t.Files[0].fd.WriteAt(p.data[:currPieceSize], pieceStartIdx)
		return err
	}

	var (
		fileStartIdx       = int64(0)
		remainingPieceSize = currPieceSize
		startWriteAt       = 0
	)

	for _, currFile := range t.Files {
		fileEndIdx := fileStartIdx + currFile.finalSize

		if pieceStartIdx >= fileEndIdx {
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

			if boundedByFile || remainingPieceSize < currPieceSize {
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

func (p *pieceData) ActualSize(t *Torrent) int {
	if p.num == t.numPieces-1 {
		return t.totalSize - p.num*t.pieceSize
	}

	return t.pieceSize
}

func (p *pieceData) storeBlockIntoPiece(msg messages.PeerMessage, blockSize int) {
	block := p.data[msg.BlockOffset : msg.BlockOffset+blockSize]
	// TODO: Don't copy each block twice
	copy(block, msg.BlockData)
}

func (p *pieceData) splitIntoBlocks(t *Torrent, blockSize int) [][]byte {
	currPieceSize := p.ActualSize(t)
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
func (t *Torrent) chooseResponse(peerAddr string, outboundMsgs chan<- []byte, parsedMsgs <-chan messages.PeerMessage, errs chan<- error, ctx context.Context, cancel context.CancelFunc) {
	defer close(outboundMsgs)
	defer delete(t.activeConns, peerAddr)

	var (
		p             = newPieceData(t.pieceSize)
		connState     = newConnState()
		peerBitfield  = make([]byte, int(math.Ceil(float64(t.numPieces)/8.0)))
		currPieceSize = t.pieceSize
		currBlockSize = CLIENT_BLOCK_SIZE
		blockOffset   = 0
	)

	for {
		// fmt.Print(t.Logbuf.String())
		// t.Logbuf.Reset()

		select {
		case <-ctx.Done():
			return
		case msg, open := <-parsedMsgs:
			if !open {
				// log.Println("handleConn signaled to drop connection to", peerAddr)
				return
			}

			switch msg.Kind {
			case messages.Handshake:
				// fmt.Printf("%v has peer id %v\n", peerAddr, string(msg.peerId))
				outboundMsgs <- messages.CreateBitfield(t.bitfield)
				t.Logger.Println("Sending BITFIELD to", peerAddr)
			case messages.Request:
				if connState.am_choking {
					continue
				}

				err := t.handlePieceRequest(msg.PieceNum, msg.BlockSize, outboundMsgs)
				if err != nil {
					// TODO: Decide whether to drop conn
					t.Logger.Println("Error retrieving piece:", err)
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

				startingPiece := rand.Intn(t.numPieces)

				var err error
				p.num, err = t.selectNextPieceSeq(startingPiece, peerBitfield)
				if err != nil || errors.Is(err, ErrNoUsefulPieces) {
					// This peer is useless
					errs <- err
					return
				}

				connState.am_choking = false
				outboundMsgs <- messages.CreateUnchoke()

				outboundMsgs <- messages.CreateInterested()
				t.Logger.Println("Sending INTERESTED to", peerAddr)

				// Request the initial piece
				outboundMsgs <- messages.CreateRequest(uint32(p.num), uint32(blockOffset), CLIENT_BLOCK_SIZE)
				t.Logger.Println("Sending REQUEST for piece", p.num, "from", peerAddr)
			case messages.Piece:
				p.storeBlockIntoPiece(msg, currBlockSize)
				blockOffset += CLIENT_BLOCK_SIZE

				currPieceSize = p.ActualSize(t)

				if blockOffset == currPieceSize {
					t.Logger.Println("CHECKING piece", p.num)
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

					t.safeSetBitfield(msg.PieceNum)
					fmt.Printf("%b\n", t.bitfield)

					t.notifyPeers(uint32(p.num), peerAddr)

					if t.IsComplete() {
						cancel()
						return
					}

					// Reset for next piece
					currPieceSize = t.pieceSize
					currBlockSize = CLIENT_BLOCK_SIZE
					blockOffset = 0

					p.num, err = t.selectNextPieceSeq(p.num, peerBitfield)
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

					t.Logger.Println(blockOffset, "/", currPieceSize, "bytes")
				}

				// Check if we should request another piece
				if connState.peer_choking {
					continue
				}

				outboundMsgs <- messages.CreateRequest(uint32(p.num), uint32(blockOffset), uint32(currBlockSize))
			case messages.Have:
				setBitfield(peerBitfield, msg.PieceNum)
			}
		}
	}
}

func (t *Torrent) notifyPeers(pieceNum uint32, except string) {
	for _, conn := range t.activeConns {
		if conn.RemoteAddr().String() == except {
			continue
		}

		go func() {
			conn.Write(messages.CreateHaveMsg(pieceNum))
			t.Logger.Println("Notifying", conn.RemoteAddr(), "we HAVE piece", pieceNum)
		}()

		// fmt.Println(t.logbuf.String())
	}
}

// Retrieves and sends data for a particular piece block by block.
// If we are requested to provide a piece we don't have, choke the peer making the request.
func (t *Torrent) handlePieceRequest(pieceNum int, blockSize int, outboundMsgs chan<- []byte) error {
	if !bitfieldContains(t.bitfield, pieceNum) {
		outboundMsgs <- messages.CreateChoke()
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
		outboundMsgs <- messages.CreatePiece(uint32(pieceNum), uint32(offset), block)
		offset += blockSize
	}

	t.storeUploaded(t.numUploadedBytes + currPieceSize)

	return nil
}

/*
handleConn reads and writes to the conn, stopping when it receives the Done signal from the connection handler or the peer takes too long to respond.

Multiple read passes may be performed to piece together message fragments.

Messages are parsed and sent to chooseResponse, which sends back a byte stream to be written to the conn.
*/
func (t *Torrent) handleConn(ctx context.Context, cancel context.CancelFunc, conn net.Conn, errs chan<- error) {
	peer := conn.RemoteAddr().String()

	defer conn.Close()
	defer delete(t.activeConns, peer)

	parsedMessages := make(chan messages.PeerMessage, 10)
	defer close(parsedMessages)
	outboundMsgs := make(chan []byte, 10)

	go t.chooseResponse(peer, outboundMsgs, parsedMessages, errs, ctx, cancel)

	t.Logger.Println("CONNECTED to peer", peer)

	handshakeMsg := messages.CreateHandshake(t.infoHash, []byte(CLIENT_PEER_ID))
	if _, err := conn.Write(handshakeMsg); err != nil {
		errs <- err
		return
	}

	msgBuf := make([]byte, 0, 32*1024)

	// TODO: This should really be based on the block size chosen by the peer, but ours seems to be the de facto standard
	tempBuf := make([]byte, MAX_MESSAGE_SIZE)

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
			errs <- messages.ErrMessageTimedOut
			return
		case <-ctx.Done():
			return
		default:
			// fmt.Println("Nothing to write")

			// Move on and attempt to read
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

		// Can't subslice msgBuf because it will be cleared
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
				t.Logger.Println(conn.RemoteAddr(), "sent a", msg.Kind)
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

// Sequentially chooses the next piece to be downloaded, skipping those we already have,
// the peer doesn't have, or those entirely in a file the user doesn't want.
func (t *Torrent) selectNextPieceSeq(currPieceNum int, peerBitfield []byte) (int, error) {
	nextPieceNum := currPieceNum + 1

	for range t.numPieces {
		if currPieceNum == t.numPieces-1 {
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
