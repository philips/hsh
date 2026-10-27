package proxywatch

import "github.com/charmbracelet/bubbles/key"

type keyMap struct {
	Select         key.Binding
	SelectAll      key.Binding
	SelectNone     key.Binding
	Approve        key.Binding
	Ignore         key.Binding
	ToggleIgnored  key.Binding
	Refresh        key.Binding
	Quit           key.Binding
	Filter         key.Binding
	FilterAccept   key.Binding
	FilterCancel   key.Binding
	FilterComplete key.Binding
}

var keys = keyMap{
	Select:         key.NewBinding(key.WithKeys(" "), key.WithHelp("space", "select")),
	SelectAll:      key.NewBinding(key.WithKeys("a"), key.WithHelp("a", "all")),
	SelectNone:     key.NewBinding(key.WithKeys("n"), key.WithHelp("n", "none")),
	Approve:        key.NewBinding(key.WithKeys("enter", "o"), key.WithHelp("enter", "approve")),
	Ignore:         key.NewBinding(key.WithKeys("i"), key.WithHelp("i", "ignore")),
	ToggleIgnored:  key.NewBinding(key.WithKeys("t"), key.WithHelp("t", "ignored")),
	Refresh:        key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "refresh")),
	Quit:           key.NewBinding(key.WithKeys("q", "esc", "ctrl+c"), key.WithHelp("q", "quit")),
	Filter:         key.NewBinding(key.WithKeys("/"), key.WithHelp("/", "filter")),
	FilterAccept:   key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "apply filter")),
	FilterCancel:   key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "clear filter")),
	FilterComplete: key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "complete")),
}

func (k keyMap) shortHelp() []key.Binding {
	return []key.Binding{k.Select, k.Approve, k.Ignore, k.SelectAll, k.SelectNone, k.ToggleIgnored, k.Refresh}
}
