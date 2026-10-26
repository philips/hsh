package proxywatch

import (
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"hsh/internal/denieddb"
)

const (
	hostColWidth  = 36
	timeColFormat = "01-02 15:04"
)

type item struct {
	host             string
	session          string
	workspace        string
	port             string
	count            int
	lastSeen         time.Time
	firstSeen        time.Time
	lastSeenDisplay  string
	firstSeenDisplay string
	selected         bool
	ignored          bool
}

func (i item) FilterValue() string { return i.host + " " + i.session + " " + i.workspace }

func (i item) key() string { return i.host + "@" + i.session }

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
		session:          h.Session,
		workspace:        h.Workspace,
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
	line := fmt.Sprintf("%s%s %-30s %5s %5dx %-7s %-14s %s",
		cursor, checkbox, truncate(i.host, 30), i.port, i.count,
		truncate(i.session, 7), truncate(workspaceName(i.workspace), 14),
		i.lastSeenDisplay)
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

func workspaceName(ws string) string {
	if ws == "" {
		return "-"
	}
	return filepath.Base(ws)
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
