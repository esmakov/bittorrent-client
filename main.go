package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"html/template"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/itchyny/gojq"

	"github.com/esmakov/bittorrent-client/torrent"
)

var DefaultAdminListenPort = "2019"

func listenAdminEndpoint(wg *sync.WaitGroup, torrents []*torrent.Torrent) {
	fmt.Println("Admin dashboard running on port " + DefaultAdminListenPort)

	tmpl := template.Must(template.ParseFiles("static/status_page_template.html"))

	http.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("static"))))

	http.HandleFunc("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reports := make([]struct {
			Info          torrent.TorrentMetaInfo
			Bitfield      []byte
			NumDownloaded int
			NumUploaded   int
			Seeders       int
			Leechers      int
		}, len(torrents))

		for i, torr := range torrents {
			reports[i].Info = torr.TorrentMetaInfo
			reports[i].Bitfield = torr.Bitfield
			reports[i].NumDownloaded = torr.NumBytesDownloaded()
			reports[i].NumUploaded = torr.NumBytesUploaded
			reports[i].Seeders = torr.Seeders
			reports[i].Leechers = torr.Leechers
		}

		err := tmpl.Execute(w, reports)
		if err != nil {
			log.Fatalln(err)
		}
	}))

	type clientMessage struct {
		Name string
		Body string
	}
	// We do some double-buffering and store a read offset to
	// allow for replaying messages
	var msgLog []clientMessage
	offset := 0

	http.HandleFunc("/events", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")

		sendSSE(w, []byte("Connected to SSE"))

		// TODO: Replace timer with a channel
		ticker := time.NewTicker(1 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				for _, msg := range msgLog[offset:] {
					bytes, err := json.Marshal(msg)
					if err != nil {
						log.Fatalln(err)
					}

					sendSSE(w, bytes)
					offset += 1
				}

				for _, t := range torrents {
					logEntry, err := t.Logbuf.ReadString('\n')
					if errors.Is(err, io.EOF) || logEntry == "" {
						continue
					} else if err != nil {
						log.Fatalln(err)
					}

					msgLog = append(msgLog,
						clientMessage{
							Name: t.MetaInfoFileName,
							Body: logEntry,
						})

				}

			case <-r.Context().Done():
				fmt.Println("Client disconnected")
				offset = 0
				return
			}
		}
	}))

	http.HandleFunc("/stop", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, err := w.Write([]byte("Stopping..."))
		if err != nil {
			log.Fatalln(err)
		}

		// die valiantly
		wg.Done()
	}))

	log.Fatalln(http.ListenAndServe("127.0.0.1:"+DefaultAdminListenPort, nil))
}

func sendSSE(w http.ResponseWriter, bytes []byte) {
	if _, err := fmt.Fprintf(w, "data: %s\n\n", bytes); err != nil {
		log.Println("SSE write error:", err)
		return
	}

	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

// All the info we need to persist to start/resume torrents, and none
// of the changing state (bitfield, etc)
type TorrentRecord struct {
	MetaInfoFileName string
	UserDesiredConns int
	WantedFiles      map[int]struct{}
	LastChecked      map[int]time.Time
}

type GlobalOptions struct {
	maxPeers int
}

func kickOffTorrents(torrs []*torrent.Torrent) {
	for _, t := range torrs {
		if _, err := t.CreateFiles(); err != nil {
			t.Logger.Error(err.Error())
		}

		t.SetWantedBitfield()

		t.Logger.Info("Checking...")
		_, err := t.CheckAllPieces()
		if err != nil {
			t.Logger.Error(err.Error())
		}

		t.Logger.Info(t.String())

		peerList, err := t.GetPeersFromTracker()
		if err != nil {
			// Continue and skip problematic torrents/trackers
			t.Logger.Error(err.Error())
			continue
		}

		go t.StartConns(peerList, t.UserDesiredConns)
	}
}

func main() {
	if len(os.Args) < 2 {
		fmt.Printf("USAGE: %v <run>|<start>|<stop>\n\t[<add>|<info>|<parse> <path/to/file.torrent>]\n", filepath.Base(os.Args[0]))
		os.Exit(1)
	}

	addCmd := flag.NewFlagSet("add", flag.ExitOnError)

	switch os.Args[1] {
	case "run":
		if len(os.Args) >= 4 {
			parentAddr := os.Args[2]
			if os.Args[3] == "--pingback" {
				conn, err := net.Dial("tcp", parentAddr)
				if err != nil {
					log.Fatalln(err)
				}

				if _, err = conn.Write([]byte("Starting...\n")); err != nil {
					log.Fatalln(err)
				}
			}
		}

		cf, err := openConfigFile()
		if err != nil {
			log.Fatalln(err)
		}

		configBytes, err := io.ReadAll(cf)
		if err != nil {
			log.Fatalln(err)
		}

		if len(configBytes) == 0 {
			// Initialize with top level array
			_, err := cf.WriteString("[]")
			if err != nil {
				log.Fatalln(err)
			}

			// Move the file cursor back
			_, err = cf.Seek(0, io.SeekStart)
			if err != nil {
				log.Fatalln(err)
			}

		}

		var records []TorrentRecord
		err = json.Unmarshal(configBytes, &records)
		if err != nil {
			log.Fatalln(err)
		}

		wg := sync.WaitGroup{}
		wg.Add(1)

		var torrs []*torrent.Torrent
		for _, rec := range records {
			t, err := torrent.New(rec.MetaInfoFileName, false)
			if err != nil {
				log.Fatalln(err)
			}

			for k := range rec.WantedFiles {
				if _, ok := rec.WantedFiles[k]; ok {
					t.Files[k].Wanted = true
				}
			}

			t.UserDesiredConns = rec.UserDesiredConns

			torrs = append(torrs, t)
		}

		go listenAdminEndpoint(&wg, torrs)

		go kickOffTorrents(torrs)

		// Keep the process going until the API's stop handler is triggered
		wg.Wait()

	case "start":
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			log.Fatalln(err)
		}
		defer listener.Close()

		cmd := exec.Command(os.Args[0], "run", listener.Addr().String(), "--pingback")

		err = cmd.Start()
		if err != nil {
			log.Fatalln("Start subcommand:", err)
		}

		// there are two ways we know we're done: either
		// the process will connect to our listener, or
		// it will exit with an error
		success, exit := make(chan struct{}), make(chan error)

		// in one goroutine, we await the success of the child process
		go func() {
			for {
				conn, err := listener.Accept()
				defer conn.Close()

				if err != nil {
					if !errors.Is(err, net.ErrClosed) {
						log.Println(err)
					}
					break
				}

				tempBuf := make([]byte, 512)
				_, err = conn.Read(tempBuf)
				if err != nil {
					log.Println(err)
				}

				fmt.Printf("%s\n", tempBuf)
				close(success)
				break
			}
		}()

		// in another goroutine, we await the failure of the child process
		go func() {
			err := cmd.Wait() // don't send on this line! Wait blocks, but send starts before it unblocks
			exit <- err       // sending on separate line ensures select won't trigger until after Wait unblocks
		}()

		// when one of the goroutines unblocks, we're done and can exit
		select {
		case <-success:
			fmt.Printf("Successfully started (pid=%d) in the background\n", cmd.Process.Pid)
		case err := <-exit:
			log.Fatalln("Child process exited with error: ", err)
		}

	case "stop":
		log.Printf("Sending stop request")
		resp, err := http.Post("http://127.0.0.1:"+DefaultAdminListenPort+"/stop", "text/plain", nil)
		if err != nil {
			log.Fatalln(err)
		}

		bytes, err := io.ReadAll(resp.Body)
		if err != nil {
			log.Fatalln(err)
		}

		log.Printf("Child responded: %s\n", bytes)
		resp.Body.Close()

	case "add":
		if len(os.Args) < 3 {
			log.Fatalln("Not enough arguments")
		}

		metaInfoFileName := os.Args[2]
		if filepath.Ext(metaInfoFileName) != ".torrent" {
			fmt.Println(metaInfoFileName, "is not a .torrent file.")
			os.Exit(1)
		}

		userDesiredConns := addCmd.Int("conns", 5, "Set the max number of peers you want to upload/download with")

		if err := addCmd.Parse(os.Args[3:]); err != nil {
			fmt.Println(err)
			os.Exit(1)
		}

		t, err := torrent.New(metaInfoFileName, false)
		if err != nil {
			panic(err)
		}

		var choices []string
		for _, f := range t.Files {
			choices = append(choices, f.Path)
		}

		m := initialModel(choices)
		p := tea.NewProgram(m)
		if _, err := p.Run(); err != nil {
			fmt.Printf("BubbleTea error: %v", err)
			os.Exit(1)
		}

		rec := TorrentRecord{
			MetaInfoFileName: metaInfoFileName,
			UserDesiredConns: *userDesiredConns,
			WantedFiles:      make(map[int]struct{}),
		}

		for k := range m.selected {
			if _, ok := m.selected[k]; ok {
				rec.WantedFiles[k] = struct{}{}
			}
		}

		cf, err := openConfigFile()
		if err != nil {
			log.Fatalln(err)
		}

		info, err := cf.Stat()
		if err != nil {
			log.Fatalln(err)
		}

		if info.Size() == int64(0) {
			// Initialize with top level array
			_, err := cf.WriteString("[]")
			if err != nil {
				log.Fatalln(err)
			}

			// Move the file cursor back
			_, err = cf.Seek(0, io.SeekStart)
			if err != nil {
				log.Fatalln(err)
			}
		}

		configBytes, err := io.ReadAll(cf)
		if err != nil {
			log.Fatalln(err)
		}

		var oldRecs []any
		if err = json.Unmarshal(configBytes, &oldRecs); err != nil {
			log.Fatalln("unmarshal:", err)
		}

		// Filter out existing record for this torrent
		q, err := gojq.Parse(".[] | select(.MetaInfoFileName != $s)")
		if err != nil {
			log.Fatalln(err)
		}

		code, err := gojq.Compile(q, gojq.WithVariables([]string{"$s"}))
		if err != nil {
			log.Fatalln(err)
		}

		var newRecs []any
		iter := code.Run(oldRecs, rec.MetaInfoFileName)
		for {
			v, ok := iter.Next()
			if !ok {
				break
			}
			if err, ok := v.(error); ok {
				if err, ok := err.(*gojq.HaltError); ok && err.Value() == nil {
					break
				}
				log.Fatalln(err)
			}

			newRecs = append(newRecs, v)
		}

		newRecs = append(newRecs, rec)

		b, err := json.Marshal(newRecs)
		if err != nil {
			log.Fatalln(err)
		}

		// Overwrite existing contents
		if err = cf.Truncate(0); err != nil {
			log.Fatalln(err)
		}

		_, err = cf.Seek(0, io.SeekStart)
		if err != nil {
			log.Fatalln(err)
		}

		_, err = cf.Write(b)
		if err != nil {
			log.Fatalln(err)
		}

		// signal client to refresh?

	case "remove":
		if len(os.Args) < 3 {
			log.Fatalln("Not enough arguments")
		}

		metaInfoFileName := os.Args[2]
		if filepath.Ext(metaInfoFileName) != ".torrent" {
			fmt.Println(metaInfoFileName, "is not a .torrent file.")
			os.Exit(1)
		}

		cf, err := openConfigFile()
		if err != nil {
			log.Fatalln(err)
		}

		configBytes, err := io.ReadAll(cf)
		if err != nil {
			log.Fatalln(err)
		}

		var oldRecs []any
		if err = json.Unmarshal(configBytes, &oldRecs); err != nil {
			log.Fatalln("Unmarshal:", err)
		}

		// Filter out existing record for this torrent
		q, err := gojq.Parse(".[] | select(.MetaInfoFileName != $s)")
		if err != nil {
			log.Fatalln(err)
		}

		code, err := gojq.Compile(q, gojq.WithVariables([]string{"$s"}))
		if err != nil {
			log.Fatalln(err)
		}

		var newRecs []any
		iter := code.Run(oldRecs, metaInfoFileName)
		for {
			v, ok := iter.Next()
			if !ok {
				break
			}
			if err, ok := v.(error); ok {
				if err, ok := err.(*gojq.HaltError); ok && err.Value() == nil {
					break
				}
				log.Fatalln(err)
			}

			newRecs = append(newRecs, v)
		}

		b, err := json.Marshal(newRecs)
		if err != nil {
			log.Fatalln(err)
		}

		// Overwrite existing contents
		if err = cf.Truncate(0); err != nil {
			log.Fatalln(err)
		}

		_, err = cf.Seek(0, io.SeekStart)
		if err != nil {
			log.Fatalln(err)
		}

		_, err = cf.Write(b)
		if err != nil {
			log.Fatalln(err)
		}

		// signal client to refresh?

	case "info":
		// TODO: Should show running status of any active torrent, not just default-initialized state
		metaInfoFileName := os.Args[2]
		if filepath.Ext(metaInfoFileName) != ".torrent" {
			log.Fatalln(metaInfoFileName, "is not a .torrent file.")
		}

		t, err := torrent.New(metaInfoFileName, false)
		if err != nil {
			log.Fatalln(err)
		}

		fmt.Println(t)

	case "parse":
		metaInfoFileName := os.Args[2]
		if filepath.Ext(metaInfoFileName) != ".torrent" {
			log.Fatalln(metaInfoFileName, "is not a .torrent file.")
		}

		_, err := torrent.New(metaInfoFileName, true)
		if err != nil {
			log.Fatalln(err)
		}

	default:
		fmt.Println("USAGE: No such subcommand")
		os.Exit(1)
	}
	return
}

func openConfigFile() (*os.File, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}

	configFile, err := os.OpenFile(filepath.Join(homeDir, ".config", "abc.json"), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}

	return configFile, nil
}

type model struct {
	choices     []string
	cursor      int
	selected    map[int]struct{}
	selectedAll bool
}

func initialModel(choices []string) model {
	return model{
		choices:  choices,
		selected: make(map[int]struct{}),
	}
}

func (m model) Init() tea.Cmd {
	// No initial I/O
	return nil
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "q":
			os.Exit(0)

		case "up", "k":
			m.cursor--
			if m.cursor < 0 {
				m.cursor = len(m.choices)
			}

		case "down", "j":
			m.cursor++
			if m.cursor == len(m.choices) {
				m.cursor = 0
			}

		case " ":
			_, ok := m.selected[m.cursor]
			if ok {
				delete(m.selected, m.cursor)
			} else {
				m.selected[m.cursor] = struct{}{}
			}
		case "A", "a":
			if m.selectedAll {
				for choice := range m.choices {
					delete(m.selected, choice)
				}
				m.selectedAll = false
			} else {
				for choice := range m.choices {
					m.selected[choice] = struct{}{}
				}
				m.selectedAll = true
			}

		case "enter":
			return m, tea.Quit
		}
	}

	// Return the updated model to the Bubble Tea runtime for processing.
	// Note that we're not returning a command.
	return m, nil
}

func (m model) View() string {
	s := "What files do you want to download?\nPress A to toggle all/none.\n\n"

	for i, choice := range m.choices {
		cursor := " "
		if m.cursor == i {
			cursor = ">"
		}

		checked := " "
		if _, ok := m.selected[i]; ok {
			checked = "x"
		}

		s += fmt.Sprintf("%s [%s] %s\n", cursor, checked, choice)
	}

	s += "\nPress Enter to submit or q to quit.\n"

	// Send the UI for rendering
	return s
}
