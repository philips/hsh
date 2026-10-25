package proxywatch

import (
	"fmt"
	"io"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"harnessh/internal/denieddb"
)

const (
	hostColWidth  = 36
	timeColFormat = "01-02 15:04"
)

type item struct {
	host             string
	port             string
	count            int
	lastSeen         time.Time
	firstSeen        time.Time
	lastSeenDisplay  string
	firstSeenDisplay string
	selected         bool
	ignored          bool
}

func (i item) FilterValue() string { return i.host }

func newItem(h denieddb.DeniedHost, selected bool) item {
	port := h.Target
	if idx := strings.LastIndex(h.Target, ":"); idx >= 0 {
		port = h.Target[idx+1:]
	}
	last, errLast := time.Parse(time.RFC3339, h.LastSeen)
	first, errFirst := time.Parse(time.RFC3339, h.FirstSeen)
	lastDisplay := h.LastSeen
	if errLast == nil {
		lastDisplay = last.Format(timeColFormat)
	}
	firstDisplay := h.FirstSeen
	if errFirst == nil {
		firstDisplay = first.Format(timeColFormat)
	}
	return item{
		host:             h.Host,
		port:             port,
		count:            h.Count,
		lastSeen:         last,
		firstSeen:        first,
		lastSeenDisplay:  lastDisplay,
		firstSeenDisplay: firstDisplay,
		selected:         selected,
		ignored:          h.Ignored,
	}
}

var (
	cursorStyle   = lipgloss.NewStyle().Bold(true)
	checkboxStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("212"))
	ignoredStyle  = lipgloss.NewStyle().Faint(true)
)

type delegate struct{}

func (d delegate) Height() int  { return 1 }
func (d delegate) Spacing() int { return 0 }

func (d delegate) Update(_ tea.Msg, _ *list.Model) tea.Cmd { return nil }

func (d delegate) Render(w io.Writer, m list.Model, index int, listItem list.Item) {
	i, ok := listItem.(item)
	if !ok {
		return
	}
	cursor := "  "
	if index == m.Index() {
		cursor = "▸ "
	}
	var checkbox string
	switch {
	case i.ignored:
		checkbox = "·"
	case i.selected:
		checkbox = checkboxStyle.Render("[x]")
	default:
		checkbox = "[ ]"
	}
	line := fmt.Sprintf("%s%s %-36s %5s %5dx  %s  %s",
		cursor, checkbox, truncate(i.host, hostColWidth), i.port, i.count,
		i.lastSeenDisplay, i.firstSeenDisplay)
	if i.ignored {
		line += "  ignored"
	}
	switch {
	case index == m.Index():
		line = cursorStyle.Render(line)
	case i.ignored:
		line = ignoredStyle.Render(line)
	}
	fmt.Fprint(w, line)
}

func truncate(s string, width int) string {
	if utf8.RuneCountInString(s) <= width {
		return s
	}
	runes := []rune(s)
	if width < 2 {
		return string(runes[:width])
	}
	return string(runes[:width-1]) + "…"
}
