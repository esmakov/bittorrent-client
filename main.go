package main

import (
	"fmt"
	"os"
	"path/filepath"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/esmakov/bittorrent-client/torrent"
)

type model struct {
	choices  []string
	cursor   int
	selected map[int]struct{}
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
			if m.cursor > 0 {
				m.cursor--
			}

		case "down", "j":
			if m.cursor < len(m.choices)-1 {
				m.cursor++
			}

		case " ":
			_, ok := m.selected[m.cursor]
			if ok {
				delete(m.selected, m.cursor)
			} else {
				m.selected[m.cursor] = struct{}{}
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
	s := "What files do you want to download?\n\n"

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

func main() {
	if len(os.Args) < 3 {
		fmt.Printf("USAGE: %v <add>|<tree> <file.torrent>\n", filepath.Base(os.Args[0]))
		os.Exit(1)
	}

	// addCmd := flag.NewFlagSet("add", flag.ExitOnError)
	// treeCmd := flag.NewFlagSet("tree", flag.ExitOnError)

	metaInfoFileName := os.Args[2]

	if filepath.Ext(metaInfoFileName) != ".torrent" {
		fmt.Println(metaInfoFileName, "is not of type .torrent")
		os.Exit(1)
	}

	switch os.Args[1] {
	case "tree":
		// if err := treeCmd.Parse(os.Args[3:]); err != nil {
		// 	fmt.Println(err)
		// 	os.Exit(1)
		// }

		_, err := torrent.New(metaInfoFileName, true)
		if err != nil {
			fmt.Println(err)
			os.Exit(1)
		}
	case "add":
		// if err := addCmd.Parse(os.Args[3:]); err != nil {
		// 	fmt.Println(err)
		// 	os.Exit(1)
		// }

		t, err := torrent.New(metaInfoFileName, false)
		if err != nil {
			panic(err)
		}

		var choices []string
		for _, f := range t.Files() {
			choices = append(choices, f.Path)
		}

		m := initialModel(choices)
		p := tea.NewProgram(m)
		if _, err := p.Run(); err != nil {
			fmt.Printf("BubbleTea error: %v", err)
			os.Exit(1)
		}

		for k := range m.selected {
			t.Files()[k].Wanted = true
		}

		if len(m.selected) == 0 {
			os.Exit(0)
		}

		filesToCheck, err := t.OpenOrCreateFiles()
		if err != nil {
			panic(err)
		}

		fmt.Println("Checking...")
		_, err = t.CheckAllPieces(filesToCheck)
		if err != nil {
			panic(err)
		}

		if t.IsComplete() {
			fmt.Println(metaInfoFileName, "is complete, starting to seed...")
			// TODO: Start seeding
			return
		}

		fmt.Println(t)

		if err := t.Start(); err != nil {
			fmt.Printf("Torrent '%v' failed to start: %v\n", metaInfoFileName, err)
			os.Exit(1)
		}
	default:
		fmt.Println("USAGE: No such subcommand")
		os.Exit(1)
	}
	return
}
