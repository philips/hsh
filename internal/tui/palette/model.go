package palette

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"hsh/internal/config"
	"hsh/internal/monitordb"
	"hsh/internal/relay"
	"hsh/internal/settings"
	"hsh/internal/sshd"
	"hsh/internal/state"
	"hsh/internal/tui/proxywatch"
)

type Options struct {
	DB           *monitordb.Store
	Filter       monitordb.Filter
	ShowDenied   bool
	Pending      []monitordb.DeniedHost
	Denied       []monitordb.DeniedHost
	SettingsPath string
	MonitorKey   string
	Sessions     *SessionOps
	Forwards     *ForwardOps
	InSession    bool
}

type SessionOps struct {
	Attached func(id string) bool
	Detach   func(id string) (string, error)
	Exit     func(id string) (string, error)
}

type Managed struct {
	ID      string
	Session string
	Bind    string
	Local   int
	Remote  int
	PID     int
	Started time.Time
}

type ForwardOps struct {
	List      func() ([]Managed, error)
	Start     func(session string, remote int) (Managed, error)
	Stop      func(id string) (string, error)
	Listening func(session string) ([]sshd.Listener, error)
}

type view int

const (
	viewCommands view = iota
	viewReview
	viewAllowHost
	viewAllowSession
	viewSessionPick
	viewConfirmExit
	viewMonitorKey
	viewPortPick
	viewForwardPick
)

const (
	cmdReview       = "review"
	cmdAllow        = "allow"
	cmdDeny         = "deny"
	cmdKey          = "key"
	cmdDetach       = "detach"
	cmdExit         = "exit"
	cmdForwardStart = "fwd-start"
	cmdForwardStop  = "fwd-stop"
)

type sessionOpMsg struct {
	text string
	quit bool
	err  error
}

type allowMsg struct {
	session     string
	entry       string
	access      string
	already     bool
	cleared     int
	stillDenied string
	err         error
}

type keyErrMsg struct{ err error }

type portsMsg struct {
	session string
	ports   []sshd.Listener
	err     error
}

type forwardStartMsg struct {
	managed Managed
	err     error
}

type forwardStopMsg struct {
	text string
	err  error
}

type Model struct {
	opts           Options
	view           view
	commands       picker
	sessions       picker
	keys           picker
	ports          picker
	forwards       picker
	host           textinput.Model
	allowEntry     string
	entryAccess    string
	forwardSession string
	pickFor        string
	target         string
	review         proxywatch.Model
	hasReview      bool
	reviewOnly     bool
	monitorKey     string
	status         string
	isError        bool
	width          int
	height         int
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
		ports:      newPicker("type to find a port"),
		forwards:   newPicker("type to find a forward"),
		host:       newTextInput("host: ", "example.com, *.example.com, or a URL"),
		width:      80,
		height:     24,
	}
	if o.DB != nil {
		m.review = proxywatch.New(o.DB, o.Filter, o.Pending, o.Denied, o.ShowDenied)
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
	items := []item{
		{id: cmdReview, text: "Proxy: review denied requests", note: review},
		{id: cmdAllow, text: "Proxy: add new allowlist item", note: allow},
		{id: cmdDeny, text: "Proxy: add new deny item", note: allow},
		{id: cmdKey, text: "Global Settings: change monitor key", note: "current: " + m.monitorKey},
	}
	if m.opts.Sessions != nil {
		items = append(items,
			item{id: cmdDetach, text: "Session: detach session", note: allow},
			item{id: cmdExit, text: "Session: exit session", note: allow},
		)
	}
	if m.opts.Forwards != nil {
		note := ""
		if active, err := m.opts.Forwards.List(); err == nil {
			note = fmt.Sprintf("%d active", len(active))
		}
		items = append(items,
			item{id: cmdForwardStart, text: "Forward: start a forward", note: note},
			item{id: cmdForwardStop, text: "Forward: stop a forward", note: note},
		)
	}
	m.commands.setItems(items)
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
	case sessionOpMsg:
		if msg.err != nil {
			m.setStatus("error: "+msg.err.Error(), true)
			m.back()
			return m, nil
		}
		m.setStatus(msg.text, false)
		m.back()
		if msg.quit {
			return m, tea.Quit
		}
		return m, nil
	case keyErrMsg:
		m.setStatus("error: "+msg.err.Error(), true)
		return m, nil
	case portsMsg:
		return m.applyPorts(msg), nil
	case forwardStartMsg:
		return m.applyForwardStart(msg), nil
	case forwardStopMsg:
		return m.applyForwardStop(msg), nil
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
				return m, m.ruleCmd(it.id, m.allowEntry, m.entryAccess)
			}
			return m, nil
		}
		return m, m.sessions.update(msg)
	case viewSessionPick:
		switch msg.Type {
		case tea.KeyEsc:
			m.back()
			return m, nil
		case tea.KeyEnter:
			if it, ok := m.sessions.selected(); ok {
				return m.sessionAction(m.pickFor, it.id)
			}
			return m, nil
		}
		return m, m.sessions.update(msg)
	case viewPortPick:
		switch msg.Type {
		case tea.KeyEsc:
			m.back()
			return m, nil
		case tea.KeyEnter:
			if it, ok := m.ports.selected(); ok {
				port, err := strconv.Atoi(it.id)
				if err != nil {
					return m, nil
				}
				return m, m.startForwardCmd(m.forwardSession, port)
			}
			return m, nil
		}
		return m, m.ports.update(msg)
	case viewForwardPick:
		switch msg.Type {
		case tea.KeyEsc:
			m.back()
			return m, nil
		case tea.KeyEnter:
			if it, ok := m.forwards.selected(); ok {
				return m, m.stopForwardCmd(it.id)
			}
			return m, nil
		}
		return m, m.forwards.update(msg)
	case viewConfirmExit:
		switch msg.Type {
		case tea.KeyEsc:
			m.back()
			return m, nil
		case tea.KeyEnter:
			return m, m.sessionOpCmd(cmdExit, m.target)
		}
		return m, nil
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
	case cmdAllow, cmdDeny:
		m.host.SetValue("")
		m.entryAccess = monitordb.AccessAllow
		if it.id == cmdDeny {
			m.entryAccess = monitordb.AccessDeny
		}
		m.view = viewAllowHost
	case cmdDetach, cmdExit:
		if s := m.opts.Filter.Session; s != "" {
			return m.sessionAction(it.id, s)
		}
		items := m.sessionItems(it.id == cmdDetach)
		if len(items) == 0 {
			if it.id == cmdDetach {
				m.setStatus("no attached sessions", false)
			} else {
				m.setStatus("no sessions", false)
			}
			return m, nil
		}
		m.pickFor = it.id
		m.sessions.setItems(items)
		m.sessions.reset()
		m.view = viewSessionPick
	case cmdKey:
		m.keys.setItems(keyItems(m.monitorKey))
		m.keys.reset()
		m.keys.selectID(keyID(m.monitorKey))
		m.view = viewMonitorKey
	case cmdForwardStart:
		if s := m.opts.Filter.Session; s != "" {
			return m.listPorts(s)
		}
		sessions := listSessions()
		if len(sessions) == 0 {
			m.setStatus("no sessions", false)
			return m, nil
		}
		m.pickFor = cmdForwardStart
		m.sessions.setItems(sessions)
		m.sessions.reset()
		m.view = viewSessionPick
	case cmdForwardStop:
		active, err := m.opts.Forwards.List()
		if err != nil {
			m.setStatus("error: "+err.Error(), true)
			return m, nil
		}
		if len(active) == 0 {
			m.setStatus("no active forwards", false)
			return m, nil
		}
		m.forwards.setItems(forwardItems(active))
		m.forwards.reset()
		m.view = viewForwardPick
	}
	return m, nil
}

func (m Model) sessionItems(attachedOnly bool) []item {
	var out []item
	for _, it := range listSessions() {
		attached := m.opts.Sessions.Attached != nil && m.opts.Sessions.Attached(it.id)
		if attachedOnly && !attached {
			continue
		}
		if attached && !attachedOnly {
			it.note = "attached"
		}
		out = append(out, it)
	}
	return out
}

func (m Model) sessionAction(kind, id string) (tea.Model, tea.Cmd) {
	if kind == cmdForwardStart {
		return m.listPorts(id)
	}
	m.target = id
	if kind == cmdExit {
		m.view = viewConfirmExit
		return m, nil
	}
	return m, m.sessionOpCmd(kind, id)
}

func (m Model) sessionOpCmd(kind, id string) tea.Cmd {
	ops := m.opts.Sessions
	quit := m.opts.InSession && id == m.opts.Filter.Session
	return func() tea.Msg {
		op := ops.Detach
		if kind == cmdExit {
			op = ops.Exit
		}
		text, err := op(id)
		return sessionOpMsg{text: text, quit: quit && err == nil, err: err}
	}
}

func (m Model) listPorts(session string) (tea.Model, tea.Cmd) {
	m.forwardSession = session
	m.ports.setItems(nil)
	m.ports.reset()
	m.setStatus("listing ports…", false)
	m.view = viewPortPick
	ops := m.opts.Forwards
	return m, func() tea.Msg {
		ports, err := ops.Listening(session)
		return portsMsg{session: session, ports: ports, err: err}
	}
}

func (m Model) applyPorts(msg portsMsg) Model {
	if m.view != viewPortPick || msg.session != m.forwardSession {
		return m
	}
	if msg.err != nil {
		m.setStatus("error: "+msg.err.Error(), true)
		m.back()
		return m
	}
	if len(msg.ports) == 0 {
		m.setStatus("session "+msg.session+" is not listening on any port", false)
		m.back()
		return m
	}
	m.setStatus("", false)
	m.ports.setItems(portItems(msg.ports))
	m.ports.reset()
	return m
}

func portItems(ports []sshd.Listener) []item {
	var items []item
	for _, l := range ports {
		items = append(items, item{id: strconv.Itoa(l.Port), text: strconv.Itoa(l.Port), note: l.Addr})
	}
	return items
}

func (m Model) startForwardCmd(session string, remote int) tea.Cmd {
	ops := m.opts.Forwards
	return func() tea.Msg {
		f, err := ops.Start(session, remote)
		return forwardStartMsg{managed: f, err: err}
	}
}

func (m Model) applyForwardStart(msg forwardStartMsg) Model {
	if msg.err != nil {
		m.setStatus("error: "+msg.err.Error(), true)
		return m
	}
	m.setStatus(fmt.Sprintf("forwarding %s -> session %s's port %d", net.JoinHostPort(msg.managed.Bind, strconv.Itoa(msg.managed.Local)), msg.managed.Session, msg.managed.Remote), false)
	m.back()
	m.refreshCommands()
	return m
}

func (m Model) stopForwardCmd(id string) tea.Cmd {
	ops := m.opts.Forwards
	return func() tea.Msg {
		text, err := ops.Stop(id)
		return forwardStopMsg{text: text, err: err}
	}
}

func (m Model) applyForwardStop(msg forwardStopMsg) Model {
	if msg.err != nil {
		m.setStatus("error: "+msg.err.Error(), true)
		return m
	}
	m.setStatus(msg.text, false)
	m.back()
	m.refreshCommands()
	return m
}

func forwardItems(active []Managed) []item {
	var items []item
	for _, f := range active {
		items = append(items, item{
			id:   f.ID,
			text: fmt.Sprintf("%s  %d -> %d", f.Session, f.Local, f.Remote),
			note: "started " + forwardAge(time.Since(f.Started)),
		})
	}
	return items
}

func forwardAge(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	default:
		return fmt.Sprintf("%dh%dm", int(d.Hours()), int(d.Minutes())%60)
	}
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

func (m Model) submitEntry() (tea.Model, tea.Cmd) {
	entry, err := config.HostPattern(m.host.Value())
	if err != nil {
		m.setStatus(err.Error(), true)
		return m, nil
	}
	m.setStatus("", false)
	m.allowEntry = entry
	if s := m.opts.Filter.Session; s != "" {
		return m, m.ruleCmd(s, entry, m.entryAccess)
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

func sessionLabel(id string) string {
	if n := state.ReadName(id); n != "" {
		return n
	}
	return id
}

func sessionText(id string) string {
	if n := state.ReadName(id); n != "" {
		return n + "  " + id
	}
	return id
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
		text := sessionText(e.Name())
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

func (m Model) ruleCmd(session, entry, access string) tea.Cmd {
	db := m.opts.DB
	if db == nil {
		db = monitordb.DefaultStore()
	}
	return func() tea.Msg {
		msg := allowMsg{session: session, entry: entry, access: access}
		rules, err := db.Rules(session)
		if err != nil {
			return allowMsg{err: fmt.Errorf("session %s: %w", session, err)}
		}
		for _, r := range rules {
			if r.Pattern == entry && r.Access == access {
				msg.already = true
			}
		}
		pending, err := db.Pending(monitordb.Filter{Session: session})
		if err != nil {
			return allowMsg{err: err}
		}
		for _, h := range pending {
			if config.HostMatches([]string{entry}, h.Host) {
				msg.cleared++
			}
		}
		write := db.Allow
		if access == monitordb.AccessDeny {
			write = db.Deny
		}
		if err := write(session, []string{entry}); err != nil {
			return allowMsg{err: fmt.Errorf("session %s: %w", session, err)}
		}
		if access == monitordb.AccessAllow && !strings.HasPrefix(entry, "*.") {
			if rules, err = db.Rules(session); err != nil {
				return allowMsg{err: err}
			}
			if r := monitordb.Evaluate(rules, entry); r.Access == monitordb.AccessDeny {
				msg.stillDenied = r.Rule.DeniedBy()
			}
		}
		return msg
	}
}

func (m Model) applyAllow(msg allowMsg) Model {
	if msg.err != nil {
		m.setStatus("error: "+msg.err.Error(), true)
		return m
	}
	var text string
	switch {
	case msg.access == monitordb.AccessDeny && msg.already:
		text = fmt.Sprintf("%s is already denied in session %s", msg.entry, msg.session)
	case msg.access == monitordb.AccessDeny:
		text = fmt.Sprintf("denied %s in session %s — the proxy reloads within ~1s", msg.entry, msg.session)
	case msg.already:
		text = fmt.Sprintf("%s is already in session %s's allowlist", msg.entry, msg.session)
	default:
		text = fmt.Sprintf("added %s to session %s's allowlist — the proxy reloads within ~1s", msg.entry, msg.session)
	}
	if msg.cleared > 0 {
		verb := "approved"
		if msg.access == monitordb.AccessDeny {
			verb = "denied"
		}
		text += fmt.Sprintf("; %d pending host(s) %s", msg.cleared, verb)
	}
	if msg.stillDenied != "" {
		text += "; it stays " + msg.stillDenied
	}
	m.setStatus(text, false)
	m.back()
	return m
}

func (m Model) entryHeading() string {
	if m.entryAccess == monitordb.AccessDeny {
		return "Proxy: add new deny item"
	}
	return "Proxy: add new allowlist item"
}

func (m Model) sessionHeading() string {
	if m.entryAccess == monitordb.AccessDeny {
		return "Proxy: deny " + m.allowEntry + " in which session?"
	}
	return "Proxy: add " + m.allowEntry + " to which session?"
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
			m.heading(m.entryHeading()),
			"",
			m.host.View(),
			"",
			noteStyle.Render("example.com matches that host; *.example.com matches every subdomain; a URL is reduced to its host"),
			m.statusView(),
			helpStyle.Render("  enter add • esc back"),
		}
	case viewAllowSession:
		sections = []string{
			titleStyle.Render(m.sessionHeading()),
			"",
			m.sessions.view(listRows),
			"",
			m.statusView(),
			helpStyle.Render("  type to filter • ↑/↓ move • enter add • esc back"),
		}
	case viewSessionPick:
		heading := "Session: detach which session?"
		switch m.pickFor {
		case cmdExit:
			heading = "Session: exit which session?"
		case cmdForwardStart:
			heading = "Forward: start a forward in which session?"
		}
		sections = []string{
			titleStyle.Render(heading),
			"",
			m.sessions.view(listRows),
			"",
			m.statusView(),
			helpStyle.Render("  type to filter • ↑/↓ move • enter choose • esc back"),
		}
	case viewPortPick:
		sections = []string{
			titleStyle.Render("Forward: forward which port of session " + m.forwardSession + "?"),
			"",
			m.ports.view(listRows),
			"",
			m.statusView(),
			helpStyle.Render("  type to filter • ↑/↓ move • enter forward • esc back"),
		}
	case viewForwardPick:
		sections = []string{
			titleStyle.Render("Forward: stop which forward?"),
			"",
			m.forwards.view(listRows),
			"",
			m.statusView(),
			helpStyle.Render("  type to filter • ↑/↓ move • enter stop • esc back"),
		}
	case viewConfirmExit:
		sections = []string{
			titleStyle.Render("Session: exit session " + sessionLabel(m.target) + "?"),
			"",
			"Its shell and everything running in it stop. The session's state is kept;",
			"hsh resume " + sessionLabel(m.target) + " starts a new shell.",
			"",
			m.statusView(),
			helpStyle.Render("  enter exit the session • esc cancel"),
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
