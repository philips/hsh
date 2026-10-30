package palette

import (
	"database/sql"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"hsh/internal/config"
	"hsh/internal/monitordb"
	"hsh/internal/relay"
	"hsh/internal/settings"
	"hsh/internal/state"
)

type env struct {
	db       *sql.DB
	settings string
}

func newEnv(t *testing.T) *env {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", filepath.Join(dir, "state"))
	db, err := monitordb.Open(filepath.Join(dir, "monitor.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return &env{db: db, settings: filepath.Join(dir, "config", "hsh", "settings.json")}
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
	return path
}

func (e *env) model(t *testing.T, session string) Model {
	t.Helper()
	f := monitordb.Filter{Session: session}
	pending, err := monitordb.Pending(e.db, f)
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
	if err := monitordb.Record(e.db, "/ws", "s1", "a.example", "a.example:443"); err != nil {
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
	_ = monitordb.Record(e.db, "/ws", "s1", "a.example", "a.example:443")
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
	m, _ = press(t, m, down, down, enter)
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
	m, cmd := press(t, m, down, down, enter, down, enter)
	m = run(t, m, cmd)
	if m.view != viewMonitorKey || !strings.Contains(m.View(), "bogus") {
		t.Fatalf("the error is not shown:\n%s", m.View())
	}
}

func TestAddAllowlistItemInTheSession(t *testing.T) {
	e := newEnv(t)
	path := e.session(t, "s1", "/ws")
	m := e.model(t, "s1")
	_ = monitordb.Record(e.db, "/ws", "s1", "api.example.com", "api.example.com:443")
	_ = monitordb.Record(e.db, "/ws", "s1", "other.test", "other.test:443")
	m, _ = press(t, m, down, enter)
	if m.view != viewAllowHost {
		t.Fatalf("view = %v", m.view)
	}
	m, cmd := press(t, m, typed("*.Example.com"), enter)
	m = run(t, m, cmd)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(cfg.Network.Allow, ",") != "keep.example,*.example.com" {
		t.Fatalf("allow = %v", cfg.Network.Allow)
	}
	pending, _ := monitordb.Pending(e.db, monitordb.Filter{Session: "s1"})
	if len(pending) != 1 || pending[0].Host != "other.test" {
		t.Fatalf("the wildcard did not approve the matching pending host: %+v", pending)
	}
	if m.view != viewCommands || !strings.Contains(m.View(), "added *.example.com to session s1") {
		t.Fatalf("no confirmation:\n%s", m.View())
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
	cfg, _ := config.Load(path)
	if strings.Join(cfg.Network.Allow, ",") != "keep.example,docs.example.org" {
		t.Fatalf("allow = %v", cfg.Network.Allow)
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

func TestAllowEntry(t *testing.T) {
	cases := map[string]string{
		"example.com":                "example.com",
		"  Example.COM. ":            "example.com",
		"*.example.com":              "*.example.com",
		"https://api.x.io:8443/v1?q": "api.x.io",
		"api.x.io:8443":              "api.x.io",
		"api.x.io/path":              "api.x.io",
	}
	for in, want := range cases {
		if got, err := AllowEntry(in); err != nil || got != want {
			t.Errorf("%q: got %q %v want %q", in, got, err, want)
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
