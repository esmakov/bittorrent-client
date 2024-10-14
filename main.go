package main

import (
	"bytes"
	"errors"
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
	"github.com/esmakov/bittorrent-client/torrent"
)

var DefaultAdminListenPort = "2019"

func listenAdminEndpoint(wg *sync.WaitGroup, torrents []*torrent.Torrent) {
	fmt.Println("Admin dashboard running on port " + DefaultAdminListenPort)
	tmpl := template.Must(template.ParseFiles("status_page_template.html"))

	bufs := make([]*bytes.Buffer, 0)
	for _, torr := range torrents {
		bufs = append(bufs, &torr.Logbuf)
	}

	http.HandleFunc("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s := struct{ Bufs []string }{Bufs: make([]string, len(torrents))}
		for i, b := range bufs {
			s.Bufs[i] = b.String()
		}
		err := tmpl.Execute(w, s)
		if err != nil {
			log.Fatal(err)
		}
	}))

	http.HandleFunc("/hello", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, err := w.Write([]byte("Hello"))
		if err != nil {
			log.Fatal(err)
		}
	}))

	http.HandleFunc("/stop", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, err := w.Write([]byte("Stopping..."))
		if err != nil {
			log.Fatal(err)
		}

		// die valiantly
		wg.Done()
	}))

	log.Fatal(http.ListenAndServe("127.0.0.1:"+DefaultAdminListenPort, nil))
}

// All the info we need to persist to start/resume torrents, and none
// of the changing state (bitfield, etc)
type TorrentRecord struct {
	metaInfoFileName string
	userDesiredConns int
	wantedFiles      map[int]struct{}
	lastChecked      map[int]time.Time
}

type GlobalOptions struct {
	maxPeers int
}

// Sets up each torrent based on the info in its associated persistent record
// Opens the files, checks pieces on disk, gets the swarm information, and starts connecting
func kickOffTorrents(torrentRecords []TorrentRecord, torrs []*torrent.Torrent) {
	for i, rec := range torrentRecords {
		t := torrs[i]

		filesToCheck, err := t.OpenOrCreateFiles()
		if err != nil {
			log.Fatal(err)
		}

		for k := range rec.wantedFiles {
			if _, ok := rec.wantedFiles[k]; ok {
				t.Files()[k].Wanted = true
			}
		}

		t.SetWantedBitfield()

		fmt.Println("Checking...")
		_, err = t.CheckAllPieces(filesToCheck)
		if err != nil {
			log.Fatal(err)
		}

		fmt.Println(t)

		peerList, err := t.GetPeersFromTracker()
		if err != nil {
			// TODO: May want to continue and skip problematic torrents/trackers
			fmt.Println("Error:", err)
			return
		}

		if err := t.StartConns(peerList, rec.userDesiredConns); err != nil {
			fmt.Printf("Torrent '%v' failed to start: %v\n", rec.metaInfoFileName, err)
			os.Exit(1)
		}
	}
}

func main() {
	if len(os.Args) < 2 {
		fmt.Printf("USAGE: %v <start>|<stop>|<add>|<info>|<parse> <path/to/file.torrent>\n", filepath.Base(os.Args[0]))
		os.Exit(1)
	}

	// addCmd := flag.NewFlagSet("add", flag.ExitOnError)

	switch os.Args[1] {
	case "run":
		if len(os.Args) >= 4 {
			parentAddr := os.Args[2]
			if os.Args[3] == "--pingback" {
				conn, err := net.Dial("tcp", parentAddr)
				if err != nil {
					log.Fatal(err)
				}

				if _, err = conn.Write([]byte("Starting...\n")); err != nil {
					log.Fatal(err)
				}
			}
		}

		// TODO: Persist a file of records on disk and read from it
		torrentRecords := []TorrentRecord{{
			metaInfoFileName: "debian-12.2.0-amd64-netinst.iso.torrent",
			wantedFiles:      make(map[int]struct{}),
			userDesiredConns: 1,
		}}
		torrentRecords[0].wantedFiles[0] = struct{}{}

		wg := sync.WaitGroup{}
		wg.Add(1)

		torrs := make([]*torrent.Torrent, 0)
		for _, tr := range torrentRecords {
			t, err := torrent.New(tr.metaInfoFileName, false)
			if err != nil {
				log.Fatal(err)
			}
			torrs = append(torrs, t)
		}

		go listenAdminEndpoint(&wg, torrs)

		go kickOffTorrents(torrentRecords, torrs)

		// Keep the process going until the API's stop handler is triggered
		wg.Wait()

	case "start":
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			log.Fatal(err)
		}
		defer listener.Close()

		cmd := exec.Command(os.Args[0], "run", listener.Addr().String(), "--pingback")

		err = cmd.Start()
		if err != nil {
			log.Fatal("start subcommand:", err)
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
			log.Fatalf("Child process exited with error: %v", err)
		}

	case "stop":
		log.Printf("Sending stop request")
		resp, err := http.Post("http://127.0.0.1:"+DefaultAdminListenPort+"/stop", "text/plain", nil)
		if err != nil {
			log.Fatal(err)
		}

		bytes, err := io.ReadAll(resp.Body)
		if err != nil {
			log.Fatal(err)
		}

		log.Printf("Child responded: %s\n", bytes)
		resp.Body.Close()

	case "add":
		panic("WIP")

		// metaInfoFileName := os.Args[2]
		// if filepath.Ext(metaInfoFileName) != ".torrent" {
		// 	fmt.Println(metaInfoFileName, "is not a .torrent file.")
		// 	os.Exit(1)
		// }

		// userDesiredConns := addCmd.Int("max-peers", 5, "Set the max number of peers you want to upload/download with")

		// if err := addCmd.Parse(os.Args[3:]); err != nil {
		// 	fmt.Println(err)
		// 	os.Exit(1)
		// }

		// t, err := torrent.New(metaInfoFileName, false)
		// if err != nil {
		// 	panic(err)
		// }

		// // TODO: Add this torrent to list of torrents via API call

		// var choices []string
		// for _, f := range t.Files() {
		// 	choices = append(choices, f.Path)
		// }

		// m := initialModel(choices)
		// p := tea.NewProgram(m)
		// if _, err := p.Run(); err != nil {
		// 	fmt.Printf("BubbleTea error: %v", err)
		// 	os.Exit(1)
		// }

		// for k := range m.selected {
		// 	t.Files()[k].Wanted = true
		// }

		// if len(m.selected) == 0 {
		// 	os.Exit(0)
		// }

		// t.SetWantedBitfield()

		// // update t.userDesiredConns with the provided flag
		// // add t to TorrentRecords and persist on disk
		// // signal server to refresh?

	case "info":
		// TODO: Should show running status of any active torrent, not just default-initialized state
		metaInfoFileName := os.Args[2]
		if filepath.Ext(metaInfoFileName) != ".torrent" {
			fmt.Println(metaInfoFileName, "is not a .torrent file.")
			os.Exit(1)
		}

		t, err := torrent.New(metaInfoFileName, false)
		if err != nil {
			fmt.Println(err)
			os.Exit(1)
		}

		fmt.Println(t)

	case "parse":
		metaInfoFileName := os.Args[2]
		if filepath.Ext(metaInfoFileName) != ".torrent" {
			fmt.Println(metaInfoFileName, "is not a .torrent file.")
			os.Exit(1)
		}

		_, err := torrent.New(metaInfoFileName, true)
		if err != nil {
			fmt.Println(err)
			os.Exit(1)
		}

	default:
		fmt.Println("USAGE: No such subcommand")
		os.Exit(1)
	}
	return
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
