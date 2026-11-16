package tui

import "charm.land/bubbles/v2/key"

type keyMap struct {
	NextTab, PrevTab, Tab1to9 key.Binding
	Up, Down, Left, Right     key.Binding
	Enter, Back               key.Binding
	Refresh, Pause, Export    key.Binding
	Stress, Help, Quit        key.Binding
}

func newKeyMap() keyMap {
	return keyMap{
		NextTab: key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "next tab")),
		PrevTab: key.NewBinding(key.WithKeys("shift+tab"), key.WithHelp("⇧tab", "prev tab")),
		Tab1to9: key.NewBinding(key.WithKeys("1", "2", "3", "4", "5", "6", "7", "8", "9", "0"), key.WithHelp("1-0", "tabs")),
		Up:      key.NewBinding(key.WithKeys("up", "k"), key.WithHelp("↑↓", "move")),
		Down:    key.NewBinding(key.WithKeys("down", "j"), key.WithHelp("↓", "down")),
		Left:    key.NewBinding(key.WithKeys("left", "h"), key.WithHelp("←→", "move")),
		Right:   key.NewBinding(key.WithKeys("right", "l"), key.WithHelp("→/l", "right")),
		Enter:   key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "open")),
		Back:    key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "back")),
		Refresh: key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "refresh")),
		Pause:   key.NewBinding(key.WithKeys("p"), key.WithHelp("p", "pause")),
		Export:  key.NewBinding(key.WithKeys("e"), key.WithHelp("e", "export JSON")),
		Stress:  key.NewBinding(key.WithKeys("s"), key.WithHelp("s", "stress")),
		Help:    key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "help")),
		Quit:    key.NewBinding(key.WithKeys("q", "ctrl+c"), key.WithHelp("q", "quit")),
	}
}
