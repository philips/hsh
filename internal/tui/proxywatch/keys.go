package proxywatch

import "github.com/charmbracelet/bubbles/key"

type keyMap struct {
	Select         key.Binding
	SelectAll      key.Binding
	SelectNone     key.Binding
	Approve        key.Binding
	Deny           key.Binding
	ToggleDenied   key.Binding
	ToggleHold     key.Binding
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
	Deny:           key.NewBinding(key.WithKeys("d"), key.WithHelp("d", "deny")),
	ToggleDenied:   key.NewBinding(key.WithKeys("t"), key.WithHelp("t", "denied")),
	ToggleHold:     key.NewBinding(key.WithKeys("w"), key.WithHelp("w", "hold")),
	Refresh:        key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "refresh")),
	Quit:           key.NewBinding(key.WithKeys("q", "esc", "ctrl+c"), key.WithHelp("q", "quit")),
	Filter:         key.NewBinding(key.WithKeys("/"), key.WithHelp("/", "filter")),
	FilterAccept:   key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "apply filter")),
	FilterCancel:   key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "clear filter")),
	FilterComplete: key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "complete")),
}

func (k keyMap) shortHelp() []key.Binding {
	return []key.Binding{k.Select, k.Approve, k.Deny, k.SelectAll, k.SelectNone, k.ToggleDenied, k.ToggleHold, k.Refresh}
}
