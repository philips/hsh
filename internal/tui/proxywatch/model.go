package proxywatch

import (
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/table"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"hsh/internal/config"
	"hsh/internal/denieddb"
)

type Model struct {
	db          *sql.DB
	filter      denieddb.Filter
	showIgnored bool
	status      string

	table       table.Model
	pendingRows []denieddb.DeniedHost
	ignoredRows []denieddb.DeniedHost
	rows        []denieddb.DeniedHost
	selected    map[string]bool

	width  int
	height int
}

func rowKey(h denieddb.DeniedHost) string {
	return h.Host + "@" + h.Session
}

func hostURL(h string) string {
	return "https://" + h
}

func defaultColumns(width int) []table.Column {
	const (
		selW    = 3
		sessW   = 13
		countW  = 6
		seenW   = 21
		minimum = 60
	)
	if width < minimum {
		width = minimum
	}
	avail := width - selW - sessW - countW - seenW - 12
	wsW := avail * 2 / 5
	if wsW < 10 {
		wsW = 10
	}
	urlW := avail - wsW
	if urlW < 20 {
		urlW = 20
	}
	return []table.Column{
		{Title: "✓", Width: selW},
		{Title: "SESSION", Width: sessW},
		{Title: "WORKSPACE", Width: wsW},
		{Title: "URL", Width: urlW},
		{Title: "COUNT", Width: countW},
		{Title: "LAST SEEN", Width: seenW},
	}
}

func New(db *sql.DB, filter denieddb.Filter, pending, ignored []denieddb.DeniedHost, showIgnored bool) Model {
	m := Model{
		db:          db,
		filter:      filter,
		showIgnored: showIgnored,
		pendingRows: pending,
		ignoredRows: ignored,
		selected:    map[string]bool{},
		width:       80,
		height:      24,
	}
	m.rows = m.allRows()
	m.table = table.New(
		table.WithColumns(defaultColumns(80)),
		table.WithRows(m.tableRows(m.rows)),
		table.WithFocused(true),
	)
	m.table.KeyMap = table.KeyMap{
		LineUp:       key.NewBinding(key.WithKeys("up", "k"), key.WithHelp("↑/k", "up")),
		LineDown:     key.NewBinding(key.WithKeys("down", "j"), key.WithHelp("↓/j", "down")),
		PageUp:       key.NewBinding(key.WithKeys("pgup"), key.WithHelp("pgup", "page up")),
		PageDown:     key.NewBinding(key.WithKeys("pgdown"), key.WithHelp("pgdn", "page down")),
		HalfPageUp:   key.NewBinding(key.WithKeys("ctrl+u"), key.WithHelp("ctrl+u", "½ page up")),
		HalfPageDown: key.NewBinding(key.WithKeys("ctrl+d"), key.WithHelp("ctrl+d", "½ page down")),
		GotoTop:      key.NewBinding(key.WithKeys("home", "g"), key.WithHelp("g/home", "go to start")),
		GotoBottom:   key.NewBinding(key.WithKeys("end", "G"), key.WithHelp("G/end", "go to end")),
	}
	return m
}

func (m Model) allRows() []denieddb.DeniedHost {
	var rows []denieddb.DeniedHost
	rows = append(rows, m.pendingRows...)
	sort.Slice(rows, func(a, b int) bool {
		if rows[a].LastSeen != rows[b].LastSeen {
			return rows[a].LastSeen > rows[b].LastSeen
		}
		return rows[a].Host < rows[b].Host
	})
	if m.showIgnored {
		rows = append(rows, m.ignoredRows...)
	}
	return rows
}

func (m Model) tableRows(rows []denieddb.DeniedHost) []table.Row {
	out := make([]table.Row, 0, len(rows))
	for _, h := range rows {
		sel := ""
		if m.selected[rowKey(h)] {
			sel = "✓"
		}
		ws := h.Workspace
		if ws == "" {
			ws = "-"
		}
		url := hostURL(h.Host)
		if h.Ignored {
			url += "  (ignored)"
		}
		out = append(out, table.Row{sel, h.Session, ws, url, fmt.Sprint(h.Count), h.LastSeen})
	}
	return out
}

func (m *Model) rebuildRows(preserveCursor bool) {
	cursorKey := ""
	if preserveCursor && len(m.rows) > 0 && m.table.Cursor() < len(m.rows) {
		cursorKey = rowKey(m.rows[m.table.Cursor()])
	}
	m.rows = m.allRows()
	m.table.SetRows(m.tableRows(m.rows))
	if cursorKey != "" {
		for i, h := range m.rows {
			if rowKey(h) == cursorKey {
				m.table.SetCursor(i)
				break
			}
		}
	}
}

func (m Model) Init() tea.Cmd {
	return tea.Tick(refreshInterval, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		h := msg.Height - 4
		if h < 1 {
			h = 1
		}
		m.table.SetColumns(defaultColumns(msg.Width))
		m.table.SetWidth(msg.Width)
		m.table.SetHeight(h)
		return m, nil

	case tickMsg:
		return m, tea.Batch(m.refreshCmd(), tea.Tick(refreshInterval, func(t time.Time) tea.Msg { return tickMsg(t) }))

	case refreshMsg:
		return m.applyRefresh(msg)

	case actionMsg:
		return m.applyAction(msg)
	}

	if msg, ok := msg.(tea.KeyMsg); ok {
		switch {
		case key.Matches(msg, keys.Quit):
			return m, tea.Quit
		case key.Matches(msg, keys.Refresh):
			return m, m.refreshCmd()
		case key.Matches(msg, keys.Select):
			return m.toggleSelect()
		case key.Matches(msg, keys.SelectAll):
			for _, h := range m.pendingRows {
				m.selected[rowKey(h)] = true
			}
			m.rebuildRows(false)
			return m, nil
		case key.Matches(msg, keys.SelectNone):
			m.selected = map[string]bool{}
			m.rebuildRows(false)
			return m, nil
		case key.Matches(msg, keys.Approve):
			return m.approve()
		case key.Matches(msg, keys.Ignore):
			return m.ignore()
		case key.Matches(msg, keys.ToggleIgnored):
			m.showIgnored = !m.showIgnored
			m.rebuildRows(false)
			return m, nil
		}
	}
	var cmd tea.Cmd
	m.table, cmd = m.table.Update(msg)
	return m, cmd
}

func (m Model) selectedRows() []denieddb.DeniedHost {
	var out []denieddb.DeniedHost
	for _, h := range m.rows {
		if !h.Ignored && m.selected[rowKey(h)] {
			out = append(out, h)
		}
	}
	return out
}

func (m Model) toggleSelect() (tea.Model, tea.Cmd) {
	if len(m.rows) == 0 || m.table.Cursor() >= len(m.rows) {
		m.status = "no rows to select"
		return m, nil
	}
	h := m.rows[m.table.Cursor()]
	if h.Ignored {
		m.status = "ignored rows cannot be selected"
		return m, nil
	}
	k := rowKey(h)
	if m.selected[k] {
		delete(m.selected, k)
	} else {
		m.selected[k] = true
	}
	m.rebuildRows(false)
	return m, nil
}

func (m Model) approve() (tea.Model, tea.Cmd) {
	rows := m.selectedRows()
	if len(rows) == 0 {
		m.status = "select hosts with space"
		return m, nil
	}
	return m, m.actionCmd(actionApprove, rows)
}

func (m Model) ignore() (tea.Model, tea.Cmd) {
	rows := m.selectedRows()
	if len(rows) == 0 {
		m.status = "select hosts with space"
		return m, nil
	}
	return m, m.actionCmd(actionIgnore, rows)
}

func (m Model) actionCmd(kind action, rows []denieddb.DeniedHost) tea.Cmd {
	db := m.db
	return func() tea.Msg {
		bySession := map[string][]string{}
		for _, r := range rows {
			bySession[r.Session] = append(bySession[r.Session], r.Host)
		}
		for session, hosts := range bySession {
			switch kind {
			case actionApprove:
				if session != "" {
					if _, err := config.AddAllowedHosts(denieddb.SessionDir(session)+"/config.json", hosts); err != nil {
						return actionMsg{kind: kind, err: fmt.Errorf("session %s: %w", session, err)}
					}
				}
				if err := denieddb.MarkAllowed(db, session, hosts); err != nil {
					return actionMsg{kind: kind, err: err}
				}
			case actionIgnore:
				if err := denieddb.MarkIgnored(db, session, hosts); err != nil {
					return actionMsg{kind: kind, err: err}
				}
			}
		}
		return actionMsg{kind: kind, rows: rows}
	}
}

func (m Model) applyRefresh(msg refreshMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		m.status = "refresh failed: " + msg.err.Error()
		return m, nil
	}
	m.pendingRows = msg.pending
	m.ignoredRows = msg.ignored
	m.rebuildRows(true)
	return m, nil
}

func (m Model) applyAction(msg actionMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		m.status = "error: " + msg.err.Error()
		return m, nil
	}
	handled := map[string]bool{}
	for _, r := range msg.rows {
		handled[rowKey(r)] = true
		delete(m.selected, rowKey(r))
	}
	var kept []denieddb.DeniedHost
	for _, h := range m.pendingRows {
		if handled[rowKey(h)] {
			if msg.kind == actionIgnore {
				h.Allowed = true
				h.Ignored = true
				h.AllowedAt = time.Now().UTC().Format(time.RFC3339)
				m.ignoredRows = append(m.ignoredRows, h)
			}
			continue
		}
		kept = append(kept, h)
	}
	m.pendingRows = kept
	m.rebuildRows(true)
	switch msg.kind {
	case actionApprove:
		m.status = fmt.Sprintf("approved %d host(s) — proxies reload within ~1s", len(msg.rows))
	case actionIgnore:
		m.status = fmt.Sprintf("ignored %d host(s) — press t to review ignored hosts", len(msg.rows))
	}
	return m, nil
}

func (m Model) refreshCmd() tea.Cmd {
	db := m.db
	filter := m.filter
	return func() tea.Msg {
		pending, err := denieddb.Pending(db, filter)
		if err != nil {
			return refreshMsg{err: err}
		}
		ignored, err := denieddb.Ignored(db, filter)
		if err != nil {
			return refreshMsg{err: err}
		}
		return refreshMsg{pending: pending, ignored: ignored}
	}
}

func title(showIgnored bool, f denieddb.Filter) string {
	t := "denied hosts awaiting review"
	if showIgnored {
		t = "denied hosts — pending and ignored"
	}
	var scope []string
	if f.Session != "" {
		scope = append(scope, "session "+f.Session)
	}
	if f.Workspace != "" {
		scope = append(scope, "workspace "+f.Workspace)
	}
	if len(scope) > 0 {
		t += " — " + strings.Join(scope, ", ")
	}
	return t
}

func (m Model) View() string {
	total := len(m.pendingRows)
	if m.showIgnored {
		total += len(m.ignoredRows)
	}
	var sections []string
	sections = append(sections, title(m.showIgnored, m.filter))
	if len(m.rows) == 0 {
		sections = append(sections, "No denied hosts.")
	} else {
		sections = append(sections, m.table.View())
	}
	status := fmt.Sprintf("%d denied host(s)", total)
	if n := len(m.selected); n > 0 {
		status += fmt.Sprintf("  ·  %d selected", n)
	}
	if m.status != "" {
		status += "  ·  " + m.status
	}
	sections = append(sections, statusStyle.Render(status))
	sections = append(sections, helpStyle.Render("  ↑/k ↓/j move • space select • enter approve • i ignore • a all • n none • t ignored • r refresh • q quit"))
	return lipgloss.JoinVertical(lipgloss.Left, sections...)
}

var (
	statusStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	helpStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
)
