package palette

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"hsh/internal/config"
	"hsh/internal/monitordb"
	"hsh/internal/relay"
	"hsh/internal/settings"
	"hsh/internal/state"
	"hsh/internal/tui/proxywatch"
)

type Options struct {
	DB           *sql.DB
	Filter       monitordb.Filter
	ShowIgnored  bool
	Pending      []monitordb.DeniedHost
	Ignored      []monitordb.DeniedHost
	SettingsPath string
	MonitorKey   string
}

type view int

const (
	viewCommands view = iota
	viewReview
	viewAllowHost
	viewAllowSession
	viewMonitorKey
)

const (
	cmdReview = "review"
	cmdAllow  = "allow"
	cmdKey    = "key"
)

type allowMsg struct {
	session string
	entry   string
	added   bool
	cleared int
	err     error
}

type keyErrMsg struct{ err error }

type Model struct {
	opts       Options
	view       view
	commands   picker
	sessions   picker
	keys       picker
	host       textinput.Model
	allowEntry string
	review     proxywatch.Model
	hasReview  bool
	reviewOnly bool
	monitorKey string
	status     string
	isError    bool
	width      int
	height     int
}

func New(o Options) Model {
	name := o.MonitorKey
	if name == "" {
		name = "ctrl-space"
	}
	m := Model{
		opts:       o,
		monitorKey: relay.DisplayName(name),
		commands:   newPicker("type a command"),
		sessions:   newPicker("type to find a session"),
		keys:       newPicker("type to find a key"),
		host:       newTextInput("host: ", "example.com, *.example.com, or a URL"),
		width:      80,
		height:     24,
	}
	if o.DB != nil {
		m.review = proxywatch.New(o.DB, o.Filter, o.Pending, o.Ignored, o.ShowIgnored)
		m.hasReview = true
	}
	m.refreshCommands()
	if m.hasReview && len(o.Pending) > 0 {
		m.view = viewReview
		m.reviewOnly = true
		m.review = m.review.SetEscHelp("esc close")
	}
	return m
}

func (m *Model) refreshCommands() {
	review := "no denied hosts recorded"
	if m.hasReview {
		switch n := m.review.PendingCount(); n {
		case 0:
			review = "nothing to review"
		default:
			review = fmt.Sprintf("%d need review", n)
		}
	}
	allow := ""
	if s := m.opts.Filter.Session; s != "" {
		allow = "session " + s
	}
	m.commands.setItems([]item{
		{id: cmdReview, text: "Proxy: review denied requests", note: review},
		{id: cmdAllow, text: "Proxy: add new allowlist item", note: allow},
		{id: cmdKey, text: "Global Settings: change monitor key", note: "current: " + m.monitorKey},
	})
}

func (m Model) Init() tea.Cmd {
	if m.hasReview {
		return m.review.Init()
	}
	return nil
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.host.Width = max(msg.Width-10, 10)
		if m.hasReview {
			return m.updateReview(msg)
		}
		return m, nil
	case allowMsg:
		return m.applyAllow(msg), nil
	case relay.KeyChange:
		m.monitorKey = msg.Key.Name
		m.setStatus(fmt.Sprintf("monitor key is now %s (other attached sessions keep their key until re-attached)", msg.Key.Name), false)
		m.back()
		m.refreshCommands()
		return m, nil
	case keyErrMsg:
		m.setStatus("error: "+msg.err.Error(), true)
		return m, nil
	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	if m.hasReview {
		return m.updateReview(msg)
	}
	return m, nil
}

func (m Model) updateReview(msg tea.Msg) (tea.Model, tea.Cmd) {
	updated, cmd := m.review.Update(msg)
	m.review = updated.(proxywatch.Model)
	m.refreshCommands()
	return m, cmd
}

func (m *Model) setStatus(s string, isError bool) {
	m.status, m.isError = s, isError
}

func (m *Model) back() {
	m.view = viewCommands
	m.commands.reset()
}

func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if msg.Type == tea.KeyCtrlC {
		return m, tea.Quit
	}
	switch m.view {
	case viewReview:
		if msg.Type == tea.KeyEsc && !m.review.Filtering() {
			if m.reviewOnly {
				return m, tea.Quit
			}
			m.back()
			return m, nil
		}
		return m.updateReview(msg)
	case viewAllowHost:
		switch msg.Type {
		case tea.KeyEsc:
			m.back()
			return m, nil
		case tea.KeyEnter:
			return m.submitEntry()
		}
		var cmd tea.Cmd
		m.host, cmd = m.host.Update(msg)
		return m, cmd
	case viewAllowSession:
		switch msg.Type {
		case tea.KeyEsc:
			m.view = viewAllowHost
			return m, nil
		case tea.KeyEnter:
			if it, ok := m.sessions.selected(); ok {
				return m, m.allowCmd(it.id, m.allowEntry)
			}
			return m, nil
		}
		return m, m.sessions.update(msg)
	case viewMonitorKey:
		switch msg.Type {
		case tea.KeyEsc:
			m.back()
			return m, nil
		case tea.KeyEnter:
			if it, ok := m.keys.selected(); ok {
				return m, m.saveKeyCmd(it.id)
			}
			return m, nil
		}
		return m, m.keys.update(msg)
	}
	switch msg.Type {
	case tea.KeyEsc:
		return m, tea.Quit
	case tea.KeyEnter:
		return m.run()
	}
	return m, m.commands.update(msg)
}

func (m Model) run() (tea.Model, tea.Cmd) {
	it, ok := m.commands.selected()
	if !ok {
		return m, nil
	}
	m.setStatus("", false)
	switch it.id {
	case cmdReview:
		if !m.hasReview {
			m.setStatus("no denied hosts recorded", false)
			return m, nil
		}
		m.view = viewReview
		m.reviewOnly = false
		m.review = m.review.SetEscHelp("esc back to commands")
	case cmdAllow:
		m.host.SetValue("")
		m.view = viewAllowHost
	case cmdKey:
		m.keys.setItems(keyItems(m.monitorKey))
		m.keys.reset()
		m.keys.selectID(keyID(m.monitorKey))
		m.view = viewMonitorKey
	}
	return m, nil
}

func keyItems(current string) []item {
	var items []item
	for _, name := range relay.KeyNames() {
		it := item{id: name, text: relay.DisplayName(name)}
		if it.text == current {
			it.note = "current"
		}
		items = append(items, it)
	}
	return items
}

func keyID(display string) string {
	for _, name := range relay.KeyNames() {
		if relay.DisplayName(name) == display {
			return name
		}
	}
	return ""
}

func (m Model) saveKeyCmd(name string) tea.Cmd {
	path := m.opts.SettingsPath
	return func() tea.Msg {
		k, err := relay.ParseKey(name)
		if err != nil {
			return keyErrMsg{err: err}
		}
		if err := settings.SetMonitorKey(path, name); err != nil {
			return keyErrMsg{err: err}
		}
		return relay.KeyChange{Key: k}
	}
}

func AllowEntry(input string) (string, error) {
	v := strings.ToLower(strings.TrimSpace(input))
	if v == "" {
		return "", fmt.Errorf("type a host, *.domain, or URL")
	}
	wildcard := strings.HasPrefix(v, "*.")
	host := v
	if wildcard {
		host = v[2:]
	} else {
		host = config.HostFromRef(v)
	}
	host = strings.TrimSuffix(host, ".")
	if host == "" || strings.ContainsAny(host, " \t*/@?#") || strings.HasPrefix(host, ".") || strings.Contains(host, "..") {
		return "", fmt.Errorf("%q is not a host, *.domain, or URL", input)
	}
	if wildcard {
		return "*." + host, nil
	}
	return host, nil
}

func (m Model) submitEntry() (tea.Model, tea.Cmd) {
	entry, err := AllowEntry(m.host.Value())
	if err != nil {
		m.setStatus(err.Error(), true)
		return m, nil
	}
	m.setStatus("", false)
	m.allowEntry = entry
	if s := m.opts.Filter.Session; s != "" {
		return m, m.allowCmd(s, entry)
	}
	sessions := listSessions()
	if len(sessions) == 0 {
		m.setStatus("no sessions to add it to", true)
		return m, nil
	}
	m.sessions.setItems(sessions)
	m.sessions.reset()
	m.view = viewAllowSession
	return m, nil
}

func listSessions() []item {
	entries, err := os.ReadDir(state.SessionsDir())
	if err != nil {
		return nil
	}
	var items []item
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := state.SessionDir(e.Name())
		if _, err := os.Stat(filepath.Join(dir, "config.json")); err != nil {
			continue
		}
		text := e.Name()
		if ws, err := os.ReadFile(filepath.Join(dir, "workspace")); err == nil {
			if w := strings.TrimSpace(string(ws)); w != "" {
				text += "  " + w
			}
		}
		items = append(items, item{id: e.Name(), text: text})
	}
	sort.Slice(items, func(a, b int) bool { return items[a].text < items[b].text })
	return items
}

func (m Model) allowCmd(session, entry string) tea.Cmd {
	db := m.opts.DB
	return func() tea.Msg {
		path := filepath.Join(state.SessionDir(session), "config.json")
		added, err := config.AddAllowedHosts(path, []string{entry})
		if err != nil {
			return allowMsg{err: fmt.Errorf("session %s: %w", session, err)}
		}
		cleared := 0
		if db != nil {
			pending, err := monitordb.Pending(db, monitordb.Filter{Session: session})
			if err != nil {
				return allowMsg{err: err}
			}
			var hosts []string
			for _, h := range pending {
				if config.HostMatches([]string{entry}, h.Host) {
					hosts = append(hosts, h.Host)
				}
			}
			if len(hosts) > 0 {
				if err := monitordb.MarkAllowed(db, session, hosts); err != nil {
					return allowMsg{err: err}
				}
			}
			cleared = len(hosts)
		}
		return allowMsg{session: session, entry: entry, added: len(added) > 0, cleared: cleared}
	}
}

func (m Model) applyAllow(msg allowMsg) Model {
	if msg.err != nil {
		m.setStatus("error: "+msg.err.Error(), true)
		return m
	}
	text := fmt.Sprintf("%s is already in session %s's allowlist", msg.entry, msg.session)
	if msg.added {
		text = fmt.Sprintf("added %s to session %s's allowlist — the proxy reloads within ~1s", msg.entry, msg.session)
	}
	if msg.cleared > 0 {
		text += fmt.Sprintf("; %d pending host(s) approved", msg.cleared)
	}
	m.setStatus(text, false)
	m.back()
	return m
}

func (m Model) statusView() string {
	if m.status == "" {
		return ""
	}
	if m.isError {
		return errorStyle.Render(m.status)
	}
	return noteStyle.Render(m.status)
}

func (m Model) heading(text string) string {
	if s := m.opts.Filter.Session; s != "" {
		text += noteStyle.Render("  session " + s)
	}
	return titleStyle.Render(text)
}

func (m Model) View() string {
	listRows := m.height - 7
	var sections []string
	switch m.view {
	case viewReview:
		return m.review.View()
	case viewAllowHost:
		sections = []string{
			m.heading("Proxy: add new allowlist item"),
			"",
			m.host.View(),
			"",
			noteStyle.Render("example.com matches that host; *.example.com matches every subdomain; a URL is reduced to its host"),
			m.statusView(),
			helpStyle.Render("  enter add • esc back"),
		}
	case viewAllowSession:
		sections = []string{
			titleStyle.Render("Proxy: add " + m.allowEntry + " to which session?"),
			"",
			m.sessions.view(listRows),
			"",
			m.statusView(),
			helpStyle.Render("  type to filter • ↑/↓ move • enter add • esc back"),
		}
	case viewMonitorKey:
		sections = []string{
			titleStyle.Render("Global Settings: change monitor key"),
			noteStyle.Render("Press the key twice to send it to the program in the session. Saved in " + m.opts.SettingsPath),
			m.keys.view(listRows - 1),
			"",
			m.statusView(),
			helpStyle.Render("  type to filter • ↑/↓ move • enter choose • esc back"),
		}
	default:
		sections = []string{
			m.heading("hsh monitor"),
			"",
			m.commands.view(listRows),
			"",
			m.statusView(),
			helpStyle.Render("  type to filter • ↑/↓ move • enter run • esc close"),
		}
	}
	return lipgloss.JoinVertical(lipgloss.Left, sections...)
}
