package proxywatch

import (
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/table"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"hsh/internal/config"
	"hsh/internal/monitordb"
	"hsh/internal/state"
	"hsh/internal/tui/style"
)

type Model struct {
	db          *sql.DB
	filter      monitordb.Filter
	showIgnored bool
	status      string

	table       table.Model
	pendingRows []monitordb.DeniedHost
	ignoredRows []monitordb.DeniedHost
	rows        []monitordb.DeniedHost
	selected    map[string]bool
	holds       map[string]string

	filtering   bool
	filterInput textinput.Model
	escHelp     string

	width  int
	height int
}

func rowKey(h monitordb.DeniedHost) string {
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

func New(db *sql.DB, filter monitordb.Filter, pending, ignored []monitordb.DeniedHost, showIgnored bool) Model {
	m := Model{
		db:          db,
		filter:      filter,
		showIgnored: showIgnored,
		pendingRows: pending,
		ignoredRows: ignored,
		selected:    map[string]bool{},
		holds:       map[string]string{},
		width:       80,
		height:      24,
	}
	m.holds = loadHolds(filter, pending, ignored)
	m.filterInput = textinput.New()
	m.filterInput.Prompt = "/"
	m.rows = m.visibleRows()
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

func (m Model) Filtering() bool {
	return m.filtering
}

func (m Model) PendingCount() int {
	return len(m.pendingRows)
}

func (m Model) allRows() []monitordb.DeniedHost {
	var rows []monitordb.DeniedHost
	rows = append(rows, m.pendingRows...)
	sort.Slice(rows, func(a, b int) bool {
		if (rows[a].Holding > 0) != (rows[b].Holding > 0) {
			return rows[a].Holding > 0
		}
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

func (m Model) tableRows(rows []monitordb.DeniedHost) []table.Row {
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
		if h.Holding > 0 {
			url += fmt.Sprintf("  HELD x%d", h.Holding)
		}
		out = append(out, table.Row{sel, h.Session, ws, url, fmt.Sprint(h.Count), h.LastSeen})
	}
	return out
}

func (m Model) cursorRow() (monitordb.DeniedHost, bool) {
	c := m.table.Cursor()
	if c < 0 || c >= len(m.rows) {
		return monitordb.DeniedHost{}, false
	}
	return m.rows[c], true
}

func (m *Model) rebuildRows(preserveCursor bool) {
	cursorKey := ""
	if h, ok := m.cursorRow(); ok && preserveCursor {
		cursorKey = rowKey(h)
	}
	m.rows = m.visibleRows()
	m.table.SetRows(m.tableRows(m.rows))
	if m.table.Cursor() < 0 && len(m.rows) > 0 {
		m.table.SetCursor(0)
	}
	if cursorKey != "" {
		for i, h := range m.rows {
			if rowKey(h) == cursorKey {
				m.table.SetCursor(i)
				break
			}
		}
	}
}

func (m Model) filterText() string {
	return strings.ToLower(strings.TrimSpace(m.filterInput.Value()))
}

func (m Model) visibleRows() []monitordb.DeniedHost {
	all := m.allRows()
	f := m.filterText()
	if f == "" {
		return all
	}
	var rows []monitordb.DeniedHost
	for _, h := range all {
		if strings.Contains(strings.ToLower(h.Session), f) ||
			strings.Contains(strings.ToLower(h.Workspace), f) ||
			strings.Contains(strings.ToLower(h.Host), f) {
			rows = append(rows, h)
		}
	}
	return rows
}

func (m Model) completionCandidates() []string {
	seen := map[string]bool{}
	var out []string
	for _, h := range m.allRows() {
		for _, c := range []string{h.Session, h.Workspace, h.Host} {
			if c != "" && !seen[c] {
				seen[c] = true
				out = append(out, c)
			}
		}
	}
	sort.Strings(out)
	return out
}

func (m *Model) completeFilter() {
	input := m.filterInput.Value()
	if input == "" {
		m.status = "type a session, workspace, or host to complete"
		return
	}
	lower := strings.ToLower(input)
	var prefixMatches []string
	var substrMatches []string
	for _, c := range m.completionCandidates() {
		cl := strings.ToLower(c)
		if strings.HasPrefix(cl, lower) {
			prefixMatches = append(prefixMatches, c)
		} else if strings.Contains(cl, lower) {
			substrMatches = append(substrMatches, c)
		}
	}
	switch {
	case len(prefixMatches) == 1:
		m.filterInput.SetValue(prefixMatches[0])
		m.status = ""
	case len(prefixMatches) > 1:
		m.filterInput.SetValue(commonPrefix(prefixMatches))
		m.status = "completions: " + strings.Join(prefixMatches[:min(len(prefixMatches), 5)], "  ")
	case len(substrMatches) == 1:
		m.filterInput.SetValue(substrMatches[0])
		m.status = ""
	case len(substrMatches) > 1:
		m.status = "completions: " + strings.Join(substrMatches[:min(len(substrMatches), 5)], "  ")
	default:
		m.status = "no completion for " + input
	}
	m.rebuildRows(false)
}

func commonPrefix(values []string) string {
	p := values[0]
	for _, v := range values[1:] {
		for !strings.HasPrefix(v, p) && p != "" {
			p = p[:len(p)-1]
		}
	}
	return p
}

func (m *Model) startFiltering() {
	m.filtering = true
	m.status = ""
	m.filterInput.Focus()
	m.filterInput.SetValue("")
}

func (m *Model) stopFiltering(clear bool) {
	m.filtering = false
	m.filterInput.Blur()
	if clear {
		m.filterInput.SetValue("")
	}
	m.rebuildRows(false)
}

func (m Model) Init() tea.Cmd {
	return tea.Tick(refreshInterval, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		h := msg.Height - 6
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

	case holdMsg:
		return m.applyHold(msg)

	case actionMsg:
		return m.applyAction(msg)
	}

	if msg, ok := msg.(tea.KeyMsg); ok {
		if m.filtering {
			switch {
			case msg.Type == tea.KeyCtrlC:
				return m, tea.Quit
			case key.Matches(msg, keys.FilterAccept):
				m.stopFiltering(false)
				return m, nil
			case key.Matches(msg, keys.FilterCancel):
				m.stopFiltering(true)
				return m, nil
			case key.Matches(msg, keys.FilterComplete):
				m.completeFilter()
				return m, nil
			}
			var cmd tea.Cmd
			m.filterInput, cmd = m.filterInput.Update(msg)
			m.rebuildRows(false)
			return m, cmd
		}
		switch {
		case key.Matches(msg, keys.Quit):
			return m, tea.Quit
		case key.Matches(msg, keys.Filter):
			m.startFiltering()
			return m, nil
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
		case key.Matches(msg, keys.ToggleHold):
			return m.toggleHold()
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

func (m Model) selectedRows() []monitordb.DeniedHost {
	var out []monitordb.DeniedHost
	for _, h := range m.rows {
		if !h.Ignored && m.selected[rowKey(h)] {
			out = append(out, h)
		}
	}
	return out
}

func (m Model) toggleSelect() (tea.Model, tea.Cmd) {
	h, ok := m.cursorRow()
	if !ok {
		m.status = "no rows to select"
		return m, nil
	}
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

func (m Model) targetRows() []monitordb.DeniedHost {
	if rows := m.selectedRows(); len(rows) > 0 {
		return rows
	}
	if h, ok := m.cursorRow(); ok && !h.Ignored {
		return []monitordb.DeniedHost{h}
	}
	return nil
}

func (m Model) approve() (tea.Model, tea.Cmd) {
	rows := m.targetRows()
	if len(rows) == 0 {
		m.status = "nothing to approve"
		return m, nil
	}
	return m, m.actionCmd(actionApprove, rows)
}

func (m Model) ignore() (tea.Model, tea.Cmd) {
	rows := m.targetRows()
	if len(rows) == 0 {
		m.status = "nothing to ignore"
		return m, nil
	}
	return m, m.actionCmd(actionIgnore, rows)
}

func (m Model) actionCmd(kind action, rows []monitordb.DeniedHost) tea.Cmd {
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
					if _, err := config.AddAllowedHosts(state.SessionDir(session)+"/config.json", hosts); err != nil {
						return actionMsg{kind: kind, err: fmt.Errorf("session %s: %w", session, err)}
					}
				}
				if err := monitordb.MarkAllowed(db, session, hosts); err != nil {
					return actionMsg{kind: kind, err: err}
				}
			case actionIgnore:
				if err := monitordb.MarkIgnored(db, session, hosts); err != nil {
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
	m.holds = msg.holds
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
	var kept []monitordb.DeniedHost
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
		pending, err := monitordb.Pending(db, filter)
		if err != nil {
			return refreshMsg{err: err}
		}
		ignored, err := monitordb.Ignored(db, filter)
		if err != nil {
			return refreshMsg{err: err}
		}
		return refreshMsg{pending: pending, ignored: ignored, holds: loadHolds(filter, pending, ignored)}
	}
}

func sessionConfigFile(session string) string {
	return state.SessionDir(session) + "/config.json"
}

func loadHolds(f monitordb.Filter, rows ...[]monitordb.DeniedHost) map[string]string {
	sessions := map[string]bool{}
	if f.Session != "" {
		sessions[f.Session] = true
	}
	for _, set := range rows {
		for _, h := range set {
			if h.Session != "" {
				sessions[h.Session] = true
			}
		}
	}
	out := map[string]string{}
	for s := range sessions {
		if cfg, err := config.Load(sessionConfigFile(s)); err == nil {
			out[s] = cfg.Network.HoldSummary()
		}
	}
	return out
}

func (m Model) holdSession() string {
	if h, ok := m.cursorRow(); ok && h.Session != "" {
		return h.Session
	}
	return m.filter.Session
}

func (m Model) toggleHold() (tea.Model, tea.Cmd) {
	session := m.holdSession()
	if session == "" {
		m.status = "no session to change: select a row first"
		return m, nil
	}
	return m, func() tea.Msg {
		path := sessionConfigFile(session)
		cfg, err := config.Load(path)
		if err != nil {
			return holdMsg{session: session, err: err}
		}
		value := "off"
		if cfg.Network.ApprovalWaitDuration() == 0 {
			value = config.SuggestedApprovalWait.String()
		}
		if err := config.SetApprovalWait(path, value); err != nil {
			return holdMsg{session: session, err: err}
		}
		cfg, err = config.Load(path)
		if err != nil {
			return holdMsg{session: session, err: err}
		}
		return holdMsg{session: session, summary: cfg.Network.HoldSummary(), on: cfg.Network.ApprovalWaitDuration() > 0}
	}
}

func (m Model) applyHold(msg holdMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		m.status = "error: " + msg.err.Error()
		return m, nil
	}
	m.holds[msg.session] = msg.summary
	if msg.on {
		m.status = fmt.Sprintf("session %s: %s — denied requests wait for approval while someone is watching", msg.session, msg.summary)
	} else {
		m.status = fmt.Sprintf("session %s: holding off — denied requests fail immediately", msg.session)
	}
	return m, nil
}

func (m Model) holdTitle() string {
	session := m.holdSession()
	summary, ok := m.holds[session]
	if !ok {
		return ""
	}
	if m.filter.Session != "" {
		return summary
	}
	return summary + " (session " + session + ")"
}

func title(showIgnored bool) string {
	if showIgnored {
		return "denied hosts — pending and ignored"
	}
	return "denied hosts awaiting review"
}

func (m Model) headingNotes() string {
	var notes []string
	if m.filter.Session != "" {
		notes = append(notes, "session "+m.filter.Session)
	}
	if m.filter.Workspace != "" {
		notes = append(notes, "workspace "+m.filter.Workspace)
	}
	if h := m.holdTitle(); h != "" {
		notes = append(notes, h)
	}
	return strings.Join(notes, " · ")
}

func (m Model) SetEscHelp(text string) Model {
	m.escHelp = text
	return m
}

func (m Model) View() string {
	total := len(m.pendingRows)
	if m.showIgnored {
		total += len(m.ignoredRows)
	}
	heading := style.Title.Render(title(m.showIgnored))
	if notes := m.headingNotes(); notes != "" {
		heading += style.Note.Render("  " + notes)
	}
	sections := []string{heading, ""}
	switch {
	case len(m.rows) > 0:
		sections = append(sections, m.table.View())
	case m.filterText() != "":
		sections = append(sections, style.Note.Render("  nothing matches the filter"))
	default:
		sections = append(sections, style.Note.Render("  nothing to review"))
	}
	var status []string
	if total > 0 {
		status = append(status, fmt.Sprintf("%d denied host(s)", total))
		if len(m.rows) < total {
			status = append(status, fmt.Sprintf("%d shown", len(m.rows)))
		}
	}
	if n := len(m.selected); n > 0 {
		status = append(status, fmt.Sprintf("%d selected", n))
	}
	if m.status != "" {
		status = append(status, m.status)
	}
	sections = append(sections, "", style.Note.Render(strings.Join(status, "  ·  ")))
	if m.filtering {
		sections = append(sections, "  filter: "+m.filterInput.View())
		sections = append(sections, style.Help.Render("  type to filter (session, workspace, url) • tab complete • enter apply • esc clear"))
	} else {
		esc := m.escHelp
		if esc == "" {
			esc = "q quit"
		}
		sections = append(sections, style.Help.Render("  enter approve • i ignore • space select • a/n all/none • t ignored • w hold • / filter • "+esc))
	}
	return lipgloss.JoinVertical(lipgloss.Left, sections...)
}
