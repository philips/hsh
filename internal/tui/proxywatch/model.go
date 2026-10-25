package proxywatch

import (
	"database/sql"
	"fmt"
	"sort"
	"time"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"hsh/internal/config"
	"hsh/internal/denieddb"
)

// Model is the bubbletea model for `hsh proxy-watch`: a multi-select
// list of proxy denials with approve/ignore actions.
type Model struct {
	list        list.Model
	db          *sql.DB
	configPath  string
	showIgnored bool
	status      string
	pendingRows []denieddb.DeniedHost
	ignoredRows []denieddb.DeniedHost
}

func New(db *sql.DB, configPath string, pending, ignored []denieddb.DeniedHost, showIgnored bool) Model {
	m := Model{
		db:          db,
		configPath:  configPath,
		showIgnored: showIgnored,
		pendingRows: pending,
		ignoredRows: ignored,
	}
	l := list.New(buildItems(pending, ignored, showIgnored, nil), delegate{}, 80, 24)
	l.Title = title(showIgnored)
	l.SetStatusBarItemName("denied host", "denied hosts")
	l.AdditionalShortHelpKeys = keys.shortHelp
	l.AdditionalFullHelpKeys = keys.shortHelp
	m.list = l
	return m
}

func (m Model) Init() tea.Cmd {
	return tea.Tick(refreshInterval, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		h := msg.Height - 6
		if h < 1 {
			h = 1
		}
		m.list.SetSize(msg.Width, h)
		return m, nil

	case tickMsg:
		return m, tea.Batch(m.refreshCmd(), tea.Tick(refreshInterval, func(t time.Time) tea.Msg { return tickMsg(t) }))

	case refreshMsg:
		return m.applyRefresh(msg)

	case actionMsg:
		return m.applyAction(msg)
	}

	if msg, ok := msg.(tea.KeyMsg); ok && m.list.FilterState() != list.Filtering {
		switch {
		case key.Matches(msg, keys.Select):
			return m, m.toggleSelect()
		case key.Matches(msg, keys.SelectAll):
			return m, m.setSelected(true)
		case key.Matches(msg, keys.SelectNone):
			return m, m.setSelected(false)
		case key.Matches(msg, keys.Approve):
			return m.approve()
		case key.Matches(msg, keys.Ignore):
			return m.ignore()
		case key.Matches(msg, keys.ToggleIgnored):
			return m.toggleIgnored()
		case key.Matches(msg, keys.Refresh):
			return m, m.refreshCmd()
		case msg.String() == "esc" && m.list.FilterState() == list.Unfiltered:
			return m, tea.Quit
		}
	}

	var cmd tea.Cmd
	m.list, cmd = m.list.Update(msg)
	return m, cmd
}

func (m Model) View() string {
	status := fmt.Sprintf("%d selected", countSelected(m.list))
	if m.status != "" {
		status += "  ·  " + m.status
	}
	return lipgloss.JoinVertical(lipgloss.Left, m.list.View(), statusStyle.Render(status))
}

var statusStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))

func title(showIgnored bool) string {
	if showIgnored {
		return "denied hosts — pending and ignored"
	}
	return "denied hosts awaiting review"
}

func buildItems(pending, ignored []denieddb.DeniedHost, showIgnored bool, selected map[string]bool) []list.Item {
	var items []item
	for _, h := range pending {
		items = append(items, newItem(h, selected[h.Host]))
	}
	if showIgnored {
		for _, h := range ignored {
			items = append(items, newItem(h, false))
		}
	}
	sort.Slice(items, func(a, b int) bool {
		if !items[a].lastSeen.Equal(items[b].lastSeen) {
			return items[a].lastSeen.After(items[b].lastSeen)
		}
		return items[a].host < items[b].host
	})
	out := make([]list.Item, len(items))
	for i, it := range items {
		out[i] = it
	}
	return out
}

func currentSelection(l list.Model) map[string]bool {
	selected := map[string]bool{}
	for _, it := range l.Items() {
		if i, ok := it.(item); ok && i.selected && !i.ignored {
			selected[i.host] = true
		}
	}
	return selected
}

func selectedHosts(l list.Model) []string {
	var hosts []string
	for _, it := range l.Items() {
		if i, ok := it.(item); ok && i.selected && !i.ignored {
			hosts = append(hosts, i.host)
		}
	}
	return hosts
}

func countSelected(l list.Model) int {
	return len(selectedHosts(l))
}

func (m Model) applyRefresh(msg refreshMsg) (Model, tea.Cmd) {
	if msg.err != nil {
		m.status = "refresh failed: " + msg.err.Error()
		return m, nil
	}
	m.pendingRows = msg.pending
	m.ignoredRows = msg.ignored
	return m, m.list.SetItems(buildItems(m.pendingRows, m.ignoredRows, m.showIgnored, currentSelection(m.list)))
}

func (m Model) refreshCmd() tea.Cmd {
	db := m.db
	return func() tea.Msg {
		pending, err := denieddb.Pending(db)
		if err != nil {
			return refreshMsg{err: err}
		}
		ignored, err := denieddb.Ignored(db)
		if err != nil {
			return refreshMsg{err: err}
		}
		return refreshMsg{pending: pending, ignored: ignored}
	}
}

func (m Model) toggleSelect() tea.Cmd {
	sel := m.list.SelectedItem()
	i, ok := sel.(item)
	if !ok || i.ignored {
		return nil
	}
	i.selected = !i.selected
	idx := -1
	for j, it := range m.list.Items() {
		if it.(item).host == i.host {
			idx = j
			break
		}
	}
	if idx < 0 {
		return nil
	}
	return m.list.SetItem(idx, i)
}

// setSelected selects or clears every pending item, ignoring any active
// filter. Ignored rows are never selectable.
func (m Model) setSelected(selected bool) tea.Cmd {
	items := m.list.Items()
	changed := false
	for idx, it := range items {
		i, ok := it.(item)
		if !ok || i.ignored {
			continue
		}
		if i.selected != selected {
			i.selected = selected
			items[idx] = i
			changed = true
		}
	}
	if !changed {
		return nil
	}
	return m.list.SetItems(items)
}

func (m Model) toggleIgnored() (Model, tea.Cmd) {
	m.showIgnored = !m.showIgnored
	m.list.Title = title(m.showIgnored)
	return m, m.list.SetItems(buildItems(m.pendingRows, m.ignoredRows, m.showIgnored, currentSelection(m.list)))
}

func (m Model) approve() (Model, tea.Cmd) {
	hosts := selectedHosts(m.list)
	if len(hosts) == 0 {
		m.status = "select hosts with space"
		return m, nil
	}
	return m, m.actionCmd(actionApprove, hosts)
}

func (m Model) ignore() (Model, tea.Cmd) {
	hosts := selectedHosts(m.list)
	if len(hosts) == 0 {
		m.status = "select hosts with space"
		return m, nil
	}
	return m, m.actionCmd(actionIgnore, hosts)
}

func (m Model) actionCmd(kind action, hosts []string) tea.Cmd {
	db, configPath := m.db, m.configPath
	return func() tea.Msg {
		switch kind {
		case actionApprove:
			if _, err := config.AddAllowedHosts(configPath, hosts); err != nil {
				return actionMsg{kind: kind, err: err}
			}
			if err := denieddb.MarkAllowed(db, hosts); err != nil {
				return actionMsg{kind: kind, err: err}
			}
		case actionIgnore:
			if err := denieddb.MarkIgnored(db, hosts); err != nil {
				return actionMsg{kind: kind, err: err}
			}
		}
		return actionMsg{kind: kind, hosts: hosts}
	}
}

func (m Model) applyAction(msg actionMsg) (Model, tea.Cmd) {
	if msg.err != nil {
		m.status = "error: " + msg.err.Error()
		return m, nil
	}
	handled := map[string]bool{}
	for _, h := range msg.hosts {
		handled[h] = true
	}
	var kept []denieddb.DeniedHost
	for _, h := range m.pendingRows {
		if handled[h.Host] {
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
	switch msg.kind {
	case actionApprove:
		m.status = fmt.Sprintf("approved %d host(s) — proxies reload within ~1s", len(msg.hosts))
	case actionIgnore:
		m.status = fmt.Sprintf("ignored %d host(s) — press t to review ignored hosts", len(msg.hosts))
	}
	return m, m.list.SetItems(buildItems(m.pendingRows, m.ignoredRows, m.showIgnored, currentSelection(m.list)))
}
