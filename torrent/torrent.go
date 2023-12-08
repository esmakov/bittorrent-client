package torrent

import (
	"fmt"
	"sync"
)

type Torrent struct {
	TrackerHostname  string
	InfoHash         []byte
	TrackerId        string
	metaInfoFileName string
	totalSize        int
	files            int
	comment          string
	isPrivate        bool
	pieceHashes      map[int]string
	downloaded       int
	uploaded         int
	left             int
	seeders          int
	leechers         int
	sync.Mutex
}

func New(metaInfoFileName string, metaInfoMap map[string]any, infoHash []byte, pieceHashes map[int]string) *Torrent {
	// Fields common to single-file and multi-file torrents
	trackerHostname := metaInfoMap["announce"].(string)
	infoMap := metaInfoMap["info"].(map[string]any)

	// Optional common fields
	comment := ""
	if commentEntry, ok := metaInfoMap["comment"]; ok {
		comment = commentEntry.(string)
	}

	isPrivate := 0
	if isPrivateEntry, ok := infoMap["private"]; ok {
		isPrivate = isPrivateEntry.(int)
	}

	fileMode := ""
	files := infoMap["files"]
	if files != nil {
		fileMode = "multiple"
	} else {
		fileMode = "single"
	}

	totalSize := 0
	fileNum := 1

	if fileMode == "multiple" {
		files := files.([]any)
		fileNum = len(files)

		for _, v := range files {
			fileDict := v.(map[string]any)

			length := fileDict["length"].(int)
			totalSize += length

			pl := fileDict["path"].([]any)
			var pathList []string
			for _, v := range pl {
				pathList = append(pathList, v.(string))
			}
			// TODO: Concatenate all files together and then extract piece hashes
			// fmt.Println(strings.Join(pathList, "/"), length, "bytes")
		}
	} else if fileMode == "single" {
		name := infoMap["name"].(string)
		_ = name
		length := infoMap["length"].(int)
		totalSize += length
	}

	return &Torrent{
		metaInfoFileName: metaInfoFileName,
		TrackerHostname:  trackerHostname,
		comment:          comment,
		InfoHash:         infoHash,
		files:            fileNum,
		pieceHashes:      pieceHashes,
		isPrivate:        isPrivate != 0,
		totalSize:        totalSize,
		left:             totalSize,
	}
}

func (t *Torrent) SetSeedersSafely(n int) {
	t.Lock()
	defer t.Unlock()
	t.seeders = n
}

func (t *Torrent) GetLeechersSafely() int {
	t.Lock()
	defer t.Unlock()
	return t.leechers
}

func (t *Torrent) SetLeechersSafely(n int) {
	t.Lock()
	defer t.Unlock()
	t.leechers = n
}

func (t *Torrent) GetSeedersSafely() int {
	t.Lock()
	defer t.Unlock()
	return t.seeders
}

func (t *Torrent) SetUploadedSafely(n int) {
	t.Lock()
	defer t.Unlock()
	t.uploaded = n
}

func (t *Torrent) GetUploadedSafely() int {
	t.Lock()
	defer t.Unlock()
	return t.uploaded
}

func (t *Torrent) SetDownloadedSafely(n int) {
	t.Lock()
	defer t.Unlock()
	t.downloaded = n
}

func (t *Torrent) GetDownloadedSafely() int {
	t.Lock()
	defer t.Unlock()
	return t.downloaded
}

func (t *Torrent) SetLeftSafely(n int) {
	t.Lock()
	defer t.Unlock()
	t.left = n
}

func (t *Torrent) GetLeftSafely() int {
	t.Lock()
	defer t.Unlock()
	return t.left
}

func (t *Torrent) String() string {
	str := fmt.Sprintln(
		fmt.Sprintln("-------------Torrent Info---------------"),
		fmt.Sprintln("File path:", t.metaInfoFileName),
		fmt.Sprintln("Tracker:", t.TrackerHostname),
		fmt.Sprintf("Hash: % x\n", t.InfoHash),
		fmt.Sprintf("Total size: %v\n", t.totalSize),
		fmt.Sprintf("# of files: %v\n", t.files))

	if t.comment != "" {
		str += fmt.Sprintln("Comment:", t.comment)
	}
	return str
}
