package palette

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"hsh/internal/config"
	"hsh/internal/monitordb"
	"hsh/internal/monitordb/monitordbtest"
	"hsh/internal/relay"
	"hsh/internal/settings"
	"hsh/internal/sshd"
	"hsh/internal/state"
)

type env struct {
	db       *monitordb.Store
	settings string
}

func newEnv(t *testing.T) *env {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", filepath.Join(dir, "state"))
	return &env{db: monitordb.DefaultStore(), settings: filepath.Join(dir, "config", "hsh", "settings.json")}
}

func (e *env) session(t *testing.T, id, workspace string) string {
	t.Helper()
	dir := state.SessionDir(id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(`{"version":1,"network":{"allow":["keep.example"]}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "workspace"), []byte(workspace+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := monitordb.InitSession(dir, []string{"keep.example"}); err != nil {
		t.Fatal(err)
	}
	return path
}

func (e *env) rules(t *testing.T, id string) string {
	t.Helper()
	rules, err := e.db.Rules(id)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, r := range rules {
		out = append(out, r.Source+":"+r.Pattern+"="+r.Access)
	}
	return strings.Join(out, ",")
}

func (e *env) model(t *testing.T, session string) Model {
	t.Helper()
	f := monitordb.Filter{Session: session}
	pending, err := e.db.Pending(f)
	if err != nil {
		t.Fatal(err)
	}
	m := New(Options{DB: e.db, Filter: f, Pending: pending, SettingsPath: e.settings, MonitorKey: "ctrl-space"})
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	return updated.(Model)
}

func press(t *testing.T, m Model, keys ...tea.KeyMsg) (Model, tea.Cmd) {
	t.Helper()
	var cmd tea.Cmd
	for _, k := range keys {
		var updated tea.Model
		updated, cmd = m.Update(k)
		m = updated.(Model)
	}
	return m, cmd
}

func typed(s string) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

var (
	enter = tea.KeyMsg{Type: tea.KeyEnter}
	esc   = tea.KeyMsg{Type: tea.KeyEscape}
	down  = tea.KeyMsg{Type: tea.KeyDown}
)

func isQuit(cmd tea.Cmd) bool {
	return cmd != nil && reflect.ValueOf(cmd).Pointer() == reflect.ValueOf(tea.Quit).Pointer()
}

func run(t *testing.T, m Model, cmd tea.Cmd) Model {
	t.Helper()
	if cmd == nil {
		t.Fatal("no command to run")
	}
	updated, _ := m.Update(cmd())
	return updated.(Model)
}

func TestOpensThePaletteWhenNothingNeedsReview(t *testing.T) {
	e := newEnv(t)
	m := e.model(t, "")
	if m.view != viewCommands {
		t.Fatalf("view = %v, want the command list", m.view)
	}
	view := m.View()
	for _, want := range []string{"Proxy: review denied requests", "Proxy: add new allowlist item", "Global Settings: change monitor key", "current: Ctrl+Space"} {
		if !strings.Contains(view, want) {
			t.Errorf("missing %q in\n%s", want, view)
		}
	}
	if _, cmd := press(t, m, esc); !isQuit(cmd) {
		t.Fatal("esc did not close the palette")
	}
}

func TestOpensTheReviewWhenSomethingNeedsReview(t *testing.T) {
	e := newEnv(t)
	e.session(t, "s1", "/ws")
	if err := monitordbtest.Seed(state.SessionDir("s1"), monitordb.DeniedHost{Host: "a.example", Target: "a.example:443"}); err != nil {
		t.Fatal(err)
	}
	m := e.model(t, "s1")
	if m.view != viewReview || !strings.Contains(m.View(), "https://a.example") {
		t.Fatalf("did not open the review list:\n%s", m.View())
	}
	if !strings.Contains(m.View(), "esc close") {
		t.Errorf("the help does not say esc closes:\n%s", m.View())
	}
	m, cmd := press(t, m, enter)
	m = run(t, m, cmd)
	if m.view != viewReview || !strings.Contains(m.View(), "nothing to review") {
		t.Fatalf("enter did not approve the host in place:\n%s", m.View())
	}
	if _, cmd := press(t, m, esc); !isQuit(cmd) {
		t.Fatal("one esc after approving did not close the monitor")
	}
	if _, cmd := press(t, m, typed("q")); !isQuit(cmd) {
		t.Fatal("q in the review did not close the monitor")
	}
}

func TestEscInAReviewOpenedFromThePaletteGoesBack(t *testing.T) {
	e := newEnv(t)
	m := e.model(t, "")
	m, _ = press(t, m, enter)
	if m.view != viewReview || !strings.Contains(m.View(), "esc back to commands") {
		t.Fatalf("the review did not open from the palette:\n%s", m.View())
	}
	m, cmd := press(t, m, esc)
	if isQuit(cmd) || m.view != viewCommands {
		t.Fatal("esc in a review opened from the palette did not go back")
	}
}

func TestEscInTheReviewFilterClearsTheFilterFirst(t *testing.T) {
	e := newEnv(t)
	e.session(t, "s1", "/ws")
	_ = monitordbtest.Seed(state.SessionDir("s1"), monitordb.DeniedHost{Host: "a.example", Target: "a.example:443"})
	m := e.model(t, "s1")
	m, _ = press(t, m, typed("/"), typed("a"), esc)
	if m.view != viewReview {
		t.Fatal("esc while filtering left the review")
	}
	if _, cmd := press(t, m, esc); !isQuit(cmd) {
		t.Fatal("a second esc did not close the monitor")
	}
}

func TestTypingFiltersTheCommandsFuzzily(t *testing.T) {
	e := newEnv(t)
	m := e.model(t, "")
	m, _ = press(t, m, typed("m"), typed("k"), typed("e"), typed("y"))
	it, ok := m.commands.selected()
	if !ok || it.id != cmdKey {
		t.Fatalf("mkey selected %+v", it)
	}
	m, _ = press(t, m, typed("z"), typed("z"))
	if _, ok := m.commands.selected(); ok {
		t.Fatal("a query that matches nothing still selected a command")
	}
	if !strings.Contains(m.View(), "no matches") {
		t.Errorf("no empty state:\n%s", m.View())
	}
	m, cmd := press(t, m, enter)
	if cmd != nil || m.view != viewCommands {
		t.Fatal("enter with no match did something")
	}
}

func TestChangeMonitorKey(t *testing.T) {
	e := newEnv(t)
	m := e.model(t, "")
	m, _ = press(t, m, down, down, down, enter)
	if m.view != viewMonitorKey {
		t.Fatalf("view = %v", m.view)
	}
	if it, _ := m.keys.selected(); it.id != "ctrl-space" {
		t.Fatalf("the current key is not preselected: %+v", it)
	}
	m, cmd := press(t, m, down, enter)
	msg := cmd()
	kc, ok := msg.(relay.KeyChange)
	if !ok || kc.Key.Byte != 0x01 || kc.Key.Name != "Ctrl+A" {
		t.Fatalf("got %#v, want a KeyChange to Ctrl+A", msg)
	}
	s, err := settings.Load(e.settings)
	if err != nil || s.MonitorKey != "ctrl-a" {
		t.Fatalf("settings: %+v %v", s, err)
	}
	updated, _ := m.Update(msg)
	m = updated.(Model)
	if m.view != viewCommands || !strings.Contains(m.View(), "current: Ctrl+A") || !strings.Contains(m.View(), "monitor key is now Ctrl+A") {
		t.Fatalf("the palette did not take the new key:\n%s", m.View())
	}
}

func TestChangeMonitorKeyReportsAnUnreadableSettingsFile(t *testing.T) {
	e := newEnv(t)
	if err := os.MkdirAll(filepath.Dir(e.settings), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(e.settings, []byte(`{"bogus":1}`), 0o644); err != nil {
		t.Fatal(err)
	}
	m := e.model(t, "")
	m, cmd := press(t, m, down, down, down, enter, down, enter)
	m = run(t, m, cmd)
	if m.view != viewMonitorKey || !strings.Contains(m.View(), "bogus") {
		t.Fatalf("the error is not shown:\n%s", m.View())
	}
}

func TestAddAllowlistItemInTheSession(t *testing.T) {
	e := newEnv(t)
	path := e.session(t, "s1", "/ws")
	m := e.model(t, "s1")
	_ = monitordbtest.Seed(state.SessionDir("s1"), monitordb.DeniedHost{Host: "api.example.com", Target: "api.example.com:443"})
	_ = monitordbtest.Seed(state.SessionDir("s1"), monitordb.DeniedHost{Host: "other.test", Target: "other.test:443"})
	m, _ = press(t, m, down, enter)
	if m.view != viewAllowHost {
		t.Fatalf("view = %v", m.view)
	}
	m, cmd := press(t, m, typed("*.Example.com"), enter)
	m = run(t, m, cmd)
	if got := e.rules(t, "s1"); got != "config:keep.example=allow,user:*.example.com=allow" {
		t.Fatalf("rules = %s", got)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(cfg.Network.Allow, ",") != "keep.example" {
		t.Fatalf("the session config was edited: %v", cfg.Network.Allow)
	}
	pending, _ := e.db.Pending(monitordb.Filter{Session: "s1"})
	if len(pending) != 1 || pending[0].Host != "other.test" {
		t.Fatalf("the wildcard did not approve the matching pending host: %+v", pending)
	}
	if m.view != viewCommands || !strings.Contains(m.View(), "added *.example.com to session s1") || !strings.Contains(m.View(), "1 pending host(s) approved") {
		t.Fatalf("no confirmation:\n%s", m.View())
	}
}

func TestAddDenyItemInTheSession(t *testing.T) {
	e := newEnv(t)
	e.session(t, "s1", "/ws")
	m := e.model(t, "s1")
	_ = monitordbtest.Seed(state.SessionDir("s1"), monitordb.DeniedHost{Host: "gist.example.com", Target: "gist.example.com:443"})
	m, _ = press(t, m, down, down, enter)
	if m.view != viewAllowHost || !strings.Contains(m.View(), "Proxy: add new deny item") {
		t.Fatalf("view = %v\n%s", m.view, m.View())
	}
	m, cmd := press(t, m, typed("*.example.com"), enter)
	m = run(t, m, cmd)
	if got := e.rules(t, "s1"); got != "config:keep.example=allow,user:*.example.com=deny" {
		t.Fatalf("rules = %s", got)
	}
	if pending, _ := e.db.Pending(monitordb.Filter{Session: "s1"}); len(pending) != 0 {
		t.Fatalf("the deny did not take the matching host out of the queue: %+v", pending)
	}
	if !strings.Contains(m.View(), "denied *.example.com in session s1") || !strings.Contains(m.View(), "1 pending host(s) denied") {
		t.Fatalf("no confirmation:\n%s", m.View())
	}
	m, cmd = press(t, m, down, enter, typed("api.example.com"), enter)
	m = run(t, m, cmd)
	if !strings.Contains(m.View(), "it stays denied by you") {
		t.Fatalf("allowing a denied host did not say it stays denied:\n%s", m.View())
	}
}

func TestAddAllowlistItemPicksASession(t *testing.T) {
	e := newEnv(t)
	e.session(t, "aaa", "/home/u/one")
	path := e.session(t, "bbb", "/home/u/two")
	m := e.model(t, "")
	m, _ = press(t, m, down, enter, typed("https://Docs.Example.org/x?y"), enter)
	if m.view != viewAllowSession {
		t.Fatalf("view = %v\n%s", m.view, m.View())
	}
	for _, want := range []string{"docs.example.org", "aaa  /home/u/one", "bbb  /home/u/two"} {
		if !strings.Contains(m.View(), want) {
			t.Errorf("missing %q in\n%s", want, m.View())
		}
	}
	m, cmd := press(t, m, typed("two"), enter)
	m = run(t, m, cmd)
	_ = path
	if got := e.rules(t, "bbb"); got != "config:keep.example=allow,user:docs.example.org=allow" {
		t.Fatalf("rules = %s", got)
	}
	if got := e.rules(t, "aaa"); got != "config:keep.example=allow" {
		t.Fatalf("another session changed: %s", got)
	}
	m, cmd = press(t, m, down, enter, typed("docs.example.org"), enter, typed("two"), enter)
	m = run(t, m, cmd)
	if !strings.Contains(m.View(), "docs.example.org is already in session bbb's allowlist") {
		t.Fatalf("adding an existing entry did not say so:\n%s", m.View())
	}
}

func TestAddAllowlistItemRejectsJunk(t *testing.T) {
	e := newEnv(t)
	e.session(t, "s1", "/ws")
	m := e.model(t, "s1")
	for _, bad := range []string{"", "a b", "*.", "foo..bar", "*.*.x", "user@host"} {
		m2, _ := press(t, m, down, enter)
		if bad != "" {
			m2, _ = press(t, m2, typed(bad))
		}
		m2, cmd := press(t, m2, enter)
		if cmd != nil || m2.view != viewAllowHost || !m2.isError {
			t.Errorf("%q was accepted", bad)
		}
	}
}

func TestWithoutADatabaseTheReviewIsUnavailable(t *testing.T) {
	m := New(Options{SettingsPath: filepath.Join(t.TempDir(), "s.json")})
	if m.view != viewCommands || !strings.Contains(m.View(), "no denied hosts recorded") {
		t.Fatalf("view:\n%s", m.View())
	}
	m, _ = press(t, m, enter)
	if m.view != viewCommands {
		t.Fatal("opened a review with no database")
	}
}

type fakeOps struct {
	attached map[string]bool
	calls    []string
	err      error
}

func (f *fakeOps) ops() *SessionOps {
	return &SessionOps{
		Attached: func(id string) bool { return f.attached[id] },
		Detach: func(id string) (string, error) {
			f.calls = append(f.calls, "detach "+id)
			return "detached " + id, f.err
		},
		Exit: func(id string) (string, error) {
			f.calls = append(f.calls, "exit "+id)
			return "exited " + id, f.err
		},
	}
}

func (e *env) sessionModel(t *testing.T, session string, inSession bool, f *fakeOps) Model {
	t.Helper()
	m := New(Options{DB: e.db, Filter: monitordb.Filter{Session: session}, SettingsPath: e.settings,
		Sessions: f.ops(), InSession: inSession})
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	return updated.(Model)
}

func pick(t *testing.T, m Model, query string) (Model, tea.Cmd) {
	t.Helper()
	m, _ = press(t, m, typed(query))
	it, _ := m.commands.selected()
	if !strings.HasPrefix(it.text, "Session: ") {
		t.Fatalf("%q selected %q", query, it.text)
	}
	return press(t, m, enter)
}

func TestSessionCommandsNeedSessionOps(t *testing.T) {
	e := newEnv(t)
	if strings.Contains(e.model(t, "").View(), "Session:") {
		t.Fatal("session commands listed without session operations")
	}
	view := e.sessionModel(t, "", false, &fakeOps{}).View()
	if !strings.Contains(view, "Session: detach session") || !strings.Contains(view, "Session: exit session") {
		t.Fatalf("session commands missing:\n%s", view)
	}
}

func TestDetachFromInsideTheSessionClosesTheMonitor(t *testing.T) {
	e := newEnv(t)
	f := &fakeOps{}
	m := e.sessionModel(t, "s1", true, f)
	m, cmd := pick(t, m, "detach")
	if _, quit := m.Update(cmd()); !isQuit(quit) {
		t.Fatal("detaching this session did not close the monitor")
	}
	if strings.Join(f.calls, ",") != "detach s1" {
		t.Fatalf("calls %v", f.calls)
	}
}

func TestExitAsksFirst(t *testing.T) {
	e := newEnv(t)
	f := &fakeOps{}
	m := e.sessionModel(t, "s1", true, f)
	m, cmd := pick(t, m, "exit")
	if cmd != nil || m.view != viewConfirmExit || !strings.Contains(m.View(), "exit session s1?") {
		t.Fatalf("no confirmation:\n%s", m.View())
	}
	m, _ = press(t, m, esc)
	if m.view != viewCommands || len(f.calls) != 0 {
		t.Fatalf("esc did not cancel: %v", f.calls)
	}
	m, _ = pick(t, m, "exit")
	m, cmd = press(t, m, enter)
	if _, quit := m.Update(cmd()); !isQuit(quit) {
		t.Fatal("exiting this session did not close the monitor")
	}
	if strings.Join(f.calls, ",") != "exit s1" {
		t.Fatalf("calls %v", f.calls)
	}
}

func TestDetachFromHshMonitorPicksAnAttachedSession(t *testing.T) {
	e := newEnv(t)
	e.session(t, "aaa", "/one")
	e.session(t, "bbb", "/two")
	f := &fakeOps{attached: map[string]bool{"bbb": true}}
	m := e.sessionModel(t, "", false, f)
	m, _ = pick(t, m, "detach")
	if m.view != viewSessionPick || strings.Contains(m.View(), "aaa") || !strings.Contains(m.View(), "bbb  /two") {
		t.Fatalf("the picker does not list just the attached session:\n%s", m.View())
	}
	m, cmd := press(t, m, enter)
	updated, quit := m.Update(cmd())
	m = updated.(Model)
	if isQuit(quit) || m.view != viewCommands || !strings.Contains(m.View(), "detached bbb") {
		t.Fatalf("detaching another session should report and stay:\n%s", m.View())
	}
}

func TestExitFromHshMonitorListsEverySession(t *testing.T) {
	e := newEnv(t)
	e.session(t, "aaa", "/one")
	e.session(t, "bbb", "/two")
	f := &fakeOps{attached: map[string]bool{"bbb": true}}
	m := e.sessionModel(t, "", false, f)
	m, _ = pick(t, m, "exit")
	view := m.View()
	if !strings.Contains(view, "aaa  /one") || !strings.Contains(view, "bbb  /two  attached") {
		t.Fatalf("picker:\n%s", view)
	}
	m, _ = press(t, m, enter)
	if m.view != viewConfirmExit || !strings.Contains(m.View(), "exit session aaa?") {
		t.Fatalf("no confirmation:\n%s", m.View())
	}
}

func TestDetachWithNoAttachedSessions(t *testing.T) {
	e := newEnv(t)
	e.session(t, "aaa", "/one")
	m := e.sessionModel(t, "", false, &fakeOps{})
	m, cmd := pick(t, m, "detach")
	if cmd != nil || m.view != viewCommands || !strings.Contains(m.View(), "no attached sessions") {
		t.Fatalf("view:\n%s", m.View())
	}
}

func TestSessionOperationErrorsAreShown(t *testing.T) {
	e := newEnv(t)
	f := &fakeOps{err: os.ErrPermission}
	m := e.sessionModel(t, "s1", true, f)
	m, cmd := pick(t, m, "detach")
	updated, quit := m.Update(cmd())
	m = updated.(Model)
	if isQuit(quit) || !strings.Contains(m.View(), "error: permission denied") {
		t.Fatalf("view:\n%s", m.View())
	}
}

type fakeForwards struct {
	active    []Managed
	ports     []sshd.Listener
	started   []string
	stopped   []string
	listErr   error
	startErr  error
	stopErr   error
	listenErr error
}

func (f *fakeForwards) ops() *ForwardOps {
	return &ForwardOps{
		List: func() ([]Managed, error) { return f.active, f.listErr },
		Start: func(session string, remote int) (Managed, error) {
			if f.startErr != nil {
				return Managed{}, f.startErr
			}
			f.started = append(f.started, fmt.Sprintf("%s:%d", session, remote))
			return Managed{ID: fmt.Sprintf("%s-%d", session, remote), Session: session, Bind: "127.0.0.1", Local: remote, Remote: remote, PID: 42}, nil
		},
		Stop: func(id string) (string, error) {
			if f.stopErr != nil {
				return "", f.stopErr
			}
			f.stopped = append(f.stopped, id)
			return "stopped forwarding " + id, nil
		},
		Listening: func(string) ([]sshd.Listener, error) { return f.ports, f.listenErr },
	}
}

func (e *env) forwardModel(t *testing.T, session string, f *fakeForwards) Model {
	t.Helper()
	m := New(Options{DB: e.db, Filter: monitordb.Filter{Session: session}, SettingsPath: e.settings, Forwards: f.ops()})
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	return updated.(Model)
}

func fwdCmd(t *testing.T, m Model, query string) (Model, tea.Cmd) {
	t.Helper()
	m, _ = press(t, m, typed(query))
	it, ok := m.commands.selected()
	if !ok || !strings.HasPrefix(it.text, "Forward: ") {
		t.Fatalf("%q selected %q", query, it.text)
	}
	return press(t, m, enter)
}

func TestForwardCommandsNeedForwardOps(t *testing.T) {
	e := newEnv(t)
	if strings.Contains(e.model(t, "").View(), "Forward:") {
		t.Fatal("forward commands listed without forward operations")
	}
	m := e.forwardModel(t, "s1", &fakeForwards{})
	view := m.View()
	for _, want := range []string{"Forward: start a forward", "Forward: stop a forward", "0 active"} {
		if !strings.Contains(view, want) {
			t.Errorf("missing %q in\n%s", want, view)
		}
	}
}

func TestStartForwardPicksAPort(t *testing.T) {
	e := newEnv(t)
	f := &fakeForwards{ports: []sshd.Listener{{Port: 8000, Addr: "127.0.0.1:8000"}}}
	m := e.forwardModel(t, "s1", f)
	m, cmd := fwdCmd(t, m, "start a forward")
	if m.view != viewPortPick || !strings.Contains(m.View(), "listing ports…") {
		t.Fatalf("no port picker:\n%s", m.View())
	}
	m = run(t, m, cmd)
	if m.view != viewPortPick || !strings.Contains(m.View(), "8000") || strings.Contains(m.View(), "listing ports…") {
		t.Fatalf("the ports did not arrive:\n%s", m.View())
	}
	m, cmd = press(t, m, enter)
	m = run(t, m, cmd)
	if strings.Join(f.started, ",") != "s1:8000" {
		t.Fatalf("started %v", f.started)
	}
	if m.view != viewCommands || !strings.Contains(m.View(), "forwarding 127.0.0.1:8000 -> session s1's port 8000") {
		t.Fatalf("no confirmation:\n%s", m.View())
	}
}

func TestStartForwardPicksASessionFirst(t *testing.T) {
	e := newEnv(t)
	e.session(t, "s1", "/ws")
	f := &fakeForwards{ports: []sshd.Listener{{Port: 8000, Addr: "127.0.0.1:8000"}}}
	m := e.forwardModel(t, "", f)
	m, cmd := fwdCmd(t, m, "start a forward")
	if cmd != nil || m.view != viewSessionPick || !strings.Contains(m.View(), "Forward: start a forward in which session?") {
		t.Fatalf("no session picker:\n%s", m.View())
	}
	m, cmd = press(t, m, enter)
	m = run(t, m, cmd)
	if m.view != viewPortPick || !strings.Contains(m.View(), "8000") {
		t.Fatalf("no port picker after the session:\n%s", m.View())
	}
	m, cmd = press(t, m, enter)
	m = run(t, m, cmd)
	if strings.Join(f.started, ",") != "s1:8000" {
		t.Fatalf("started %v", f.started)
	}
}

func TestStartForwardWithNoSessions(t *testing.T) {
	e := newEnv(t)
	m := e.forwardModel(t, "", &fakeForwards{})
	m, cmd := fwdCmd(t, m, "start a forward")
	if cmd != nil || m.view != viewCommands || !strings.Contains(m.View(), "no sessions") {
		t.Fatalf("view:\n%s", m.View())
	}
}

func TestStartForwardShowsListingErrors(t *testing.T) {
	e := newEnv(t)
	f := &fakeForwards{listenErr: errors.New("session s1 has no running workspace container; start it with: hsh resume s1")}
	m := e.forwardModel(t, "s1", f)
	m, cmd := fwdCmd(t, m, "start a forward")
	m = run(t, m, cmd)
	if m.view != viewCommands || !strings.Contains(m.View(), "error: session s1 has no running workspace container") {
		t.Fatalf("view:\n%s", m.View())
	}
}

func TestStartForwardWithNothingListening(t *testing.T) {
	e := newEnv(t)
	m := e.forwardModel(t, "s1", &fakeForwards{})
	m, cmd := fwdCmd(t, m, "start a forward")
	m = run(t, m, cmd)
	if m.view != viewCommands || !strings.Contains(m.View(), "session s1 is not listening on any port") {
		t.Fatalf("view:\n%s", m.View())
	}
}

func TestStartForwardErrorStaysInThePortPicker(t *testing.T) {
	e := newEnv(t)
	f := &fakeForwards{ports: []sshd.Listener{{Port: 8000, Addr: "127.0.0.1:8000"}}, startErr: errors.New("local port 8000 is already in use; hsh forward <other>:8000")}
	m := e.forwardModel(t, "s1", f)
	m, cmd := fwdCmd(t, m, "start a forward")
	m = run(t, m, cmd)
	m, cmd = press(t, m, enter)
	m = run(t, m, cmd)
	if m.view != viewPortPick || !strings.Contains(m.View(), "error: local port 8000 is already in use") {
		t.Fatalf("view:\n%s", m.View())
	}
}

func TestStopForwardStopsThePickedForward(t *testing.T) {
	e := newEnv(t)
	f := &fakeForwards{active: []Managed{{ID: "s1-8000", Session: "s1", Bind: "127.0.0.1", Local: 8000, Remote: 8000, PID: 42, Started: time.Now().Add(-5 * time.Minute)}}}
	m := e.forwardModel(t, "", f)
	if !strings.Contains(m.View(), "1 active") {
		t.Fatalf("the command note does not count the active forwards:\n%s", m.View())
	}
	m, cmd := fwdCmd(t, m, "stop a forward")
	if cmd != nil || m.view != viewForwardPick {
		t.Fatalf("no forward picker:\n%s", m.View())
	}
	view := m.View()
	if !strings.Contains(view, "s1  8000 -> 8000") || !strings.Contains(view, "started 5m") {
		t.Fatalf("picker:\n%s", view)
	}
	m, cmd = press(t, m, enter)
	m = run(t, m, cmd)
	if strings.Join(f.stopped, ",") != "s1-8000" {
		t.Fatalf("stopped %v", f.stopped)
	}
	if m.view != viewCommands || !strings.Contains(m.View(), "stopped forwarding s1-8000") {
		t.Fatalf("no confirmation:\n%s", m.View())
	}
}

func TestStopForwardWithNoActiveForwards(t *testing.T) {
	e := newEnv(t)
	m := e.forwardModel(t, "", &fakeForwards{})
	m, cmd := fwdCmd(t, m, "stop a forward")
	if cmd != nil || m.view != viewCommands || !strings.Contains(m.View(), "no active forwards") {
		t.Fatalf("view:\n%s", m.View())
	}
}

func TestStopForwardErrorStaysInThePicker(t *testing.T) {
	e := newEnv(t)
	f := &fakeForwards{active: []Managed{{ID: "s1-8000", Session: "s1", Bind: "127.0.0.1", Local: 8000, Remote: 8000, PID: 42, Started: time.Now()}}, stopErr: errors.New("no active forward s1-8000")}
	m := e.forwardModel(t, "", f)
	m, _ = fwdCmd(t, m, "stop a forward")
	m, cmd := press(t, m, enter)
	m = run(t, m, cmd)
	if m.view != viewForwardPick || !strings.Contains(m.View(), "error: no active forward s1-8000") {
		t.Fatalf("view:\n%s", m.View())
	}
}

func TestForwardPickersEscBack(t *testing.T) {
	e := newEnv(t)
	f := &fakeForwards{
		ports:  []sshd.Listener{{Port: 8000, Addr: "127.0.0.1:8000"}},
		active: []Managed{{ID: "s1-8000", Session: "s1", Bind: "127.0.0.1", Local: 8000, Remote: 8000, PID: 42, Started: time.Now()}},
	}
	m := e.forwardModel(t, "s1", f)
	m, cmd := fwdCmd(t, m, "start a forward")
	m = run(t, m, cmd)
	if m.view != viewPortPick {
		t.Fatalf("view = %v", m.view)
	}
	m, cmd = press(t, m, esc)
	if isQuit(cmd) || m.view != viewCommands {
		t.Fatal("esc in the port picker did not go back")
	}
	m, _ = fwdCmd(t, m, "stop a forward")
	if m.view != viewForwardPick {
		t.Fatalf("view = %v", m.view)
	}
	m, cmd = press(t, m, esc)
	if isQuit(cmd) || m.view != viewCommands {
		t.Fatal("esc in the forward picker did not go back")
	}
}
