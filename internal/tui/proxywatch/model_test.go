package proxywatch

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"hsh/internal/denieddb"
)

type testEnv struct {
	model      Model
	db         *sql.DB
	configPath string
	dir        string
}

func recordAt(t *testing.T, db *sql.DB, host, target string, lastSeen time.Time) {
	t.Helper()
	stamp := lastSeen.UTC().Format(time.RFC3339)
	_, err := db.Exec(`INSERT INTO denied (host, target, count, first_seen, last_seen, allowed, allowed_at, ignored)
		VALUES (?, ?, 1, ?, ?, 0, NULL, 0)`, host, target, stamp, stamp)
	if err != nil {
		t.Fatal(err)
	}
}

func newTestEnv(t *testing.T, hosts ...string) *testEnv {
	t.Helper()
	dir := t.TempDir()
	db, err := denieddb.Open(filepath.Join(dir, "denied.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	configPath := filepath.Join(dir, "config.hsh")
	if err := os.WriteFile(configPath, []byte(`{"network": {"allow": ["already.example.com"]}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	env := &testEnv{db: db, configPath: configPath, dir: dir}
	for i, h := range hosts {
		recordAt(t, db, h, h+":443", time.Now().UTC().Add(-time.Duration(len(hosts)-i)*time.Hour))
	}
	pending, err := denieddb.Pending(db)
	if err != nil {
		t.Fatal(err)
	}
	env.model = New(db, configPath, pending, nil, false)
	return env
}

func keyMsg(s string) tea.KeyMsg {
	switch s {
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEscape}
	case " ":
		return tea.KeyMsg{Type: tea.KeySpace}
	default:
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
	}
}

func send(m Model, msg tea.Msg) (Model, tea.Cmd) {
	updated, cmd := m.Update(msg)
	return updated.(Model), cmd
}

func runCmd(cmd tea.Cmd) tea.Msg {
	if cmd == nil {
		return nil
	}
	return cmd()
}

func allowlist(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Network struct {
			Allow []string `json:"allow"`
		} `json:"network"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	return cfg.Network.Allow
}

func hasHost(hosts []string, host string) bool {
	for _, h := range hosts {
		if h == host {
			return true
		}
	}
	return false
}

func listHosts(m Model) []string {
	var hosts []string
	for _, it := range m.list.Items() {
		hosts = append(hosts, it.(item).host)
	}
	return hosts
}

func TestApproveSelected(t *testing.T) {
	env := newTestEnv(t, "alpha.example.com", "beta.example.com")
	m, _ := send(env.model, keyMsg(" "))
	m, cmd := send(m, keyMsg("enter"))
	msg := runCmd(cmd)
	if msg == nil {
		t.Fatal("expected an action command")
	}
	m, _ = send(m, msg)

	if got := allowlist(t, env.configPath); !hasHost(got, "beta.example.com") {
		t.Errorf("approved host missing from network.allow: %v", got)
	}
	all, err := denieddb.All(env.db)
	if err != nil {
		t.Fatal(err)
	}
	var row *denieddb.DeniedHost
	for i := range all {
		if all[i].Host == "beta.example.com" {
			row = &all[i]
		}
	}
	if row == nil || !row.Allowed || row.Ignored {
		t.Errorf("approved host not marked allowed in the DB: %+v", row)
	}
	if hasHost(listHosts(m), "beta.example.com") {
		t.Error("approved host still listed")
	}
	if !strings.Contains(m.status, "approved") {
		t.Errorf("unexpected status %q", m.status)
	}
}

func TestIgnoreSelected(t *testing.T) {
	env := newTestEnv(t, "alpha.example.com", "beta.example.com")
	m, _ := send(env.model, keyMsg(" "))
	m, cmd := send(m, keyMsg("i"))
	msg := runCmd(cmd)
	if msg == nil {
		t.Fatal("expected an action command")
	}
	m, _ = send(m, msg)

	if got := allowlist(t, env.configPath); hasHost(got, "beta.example.com") {
		t.Errorf("ignored host was added to the allowlist: %v", got)
	}
	ignored, err := denieddb.Ignored(env.db)
	if err != nil {
		t.Fatal(err)
	}
	if len(ignored) != 1 || ignored[0].Host != "beta.example.com" || !ignored[0].Ignored {
		t.Fatalf("ignored host not recorded: %+v", ignored)
	}
	if hasHost(listHosts(m), "beta.example.com") {
		t.Error("ignored host still listed")
	}
	if !strings.Contains(m.status, "ignored") {
		t.Errorf("unexpected status %q", m.status)
	}
}

func TestShowIgnoredToggle(t *testing.T) {
	env := newTestEnv(t, "alpha.example.com", "beta.example.com")
	m, _ := send(env.model, keyMsg(" "))
	m, cmd := send(m, keyMsg("i"))
	m, _ = send(m, runCmd(cmd))

	m, _ = send(m, keyMsg("t"))
	idx := -1
	for j, it := range m.list.Items() {
		i := it.(item)
		if i.host == "beta.example.com" {
			idx = j
			if !i.ignored {
				t.Error("row shown in the ignored view is not marked ignored")
			}
		}
	}
	if idx < 0 {
		t.Fatal("ignored host not shown after toggling show-ignored")
	}

	m.list.Select(idx)
	m, _ = send(m, keyMsg(" "))
	for _, it := range m.list.Items() {
		if i := it.(item); i.host == "beta.example.com" && i.selected {
			t.Error("ignored host became selectable")
		}
	}

	pending, err := denieddb.Pending(env.db)
	if err != nil {
		t.Fatal(err)
	}
	ignored, err := denieddb.Ignored(env.db)
	if err != nil {
		t.Fatal(err)
	}
	m2 := New(env.db, env.configPath, pending, ignored, true)
	if !hasHost(listHosts(m2), "beta.example.com") {
		t.Error("New with showIgnored=true does not list ignored hosts")
	}
}

func TestSortMostRecentFirst(t *testing.T) {
	env := newTestEnv(t, "old.example.com", "mid.example.com", "new.example.com")
	want := []string{"new.example.com", "mid.example.com", "old.example.com"}
	got := listHosts(env.model)
	if len(got) != len(want) {
		t.Fatalf("got %d items, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("position %d: got %s, want %s (full order: %v)", i, got[i], want[i], got)
		}
	}
}

func TestSelectAllIgnoresFilter(t *testing.T) {
	env := newTestEnv(t, "alpha.example.com", "beta.example.com", "gamma.example.com")
	m := env.model
	m.list.SetFilterText("beta")
	if got := len(m.list.VisibleItems()); got != 1 {
		t.Fatalf("filter did not narrow the list: %d visible", got)
	}
	m, _ = send(m, keyMsg("a"))
	selected := 0
	for _, it := range m.list.Items() {
		if it.(item).selected {
			selected++
		}
	}
	if selected != 3 {
		t.Errorf("select-all acted on the filtered view: %d of 3 selected", selected)
	}
	m, _ = send(m, keyMsg("n"))
	selected = 0
	for _, it := range m.list.Items() {
		if it.(item).selected {
			selected++
		}
	}
	if selected != 0 {
		t.Errorf("select-none acted on the filtered view: %d still selected", selected)
	}
}

func TestRefreshBringsNewDenials(t *testing.T) {
	env := newTestEnv(t, "alpha.example.com")
	recordAt(t, env.db, "fresh.example.com", "fresh.example.com:443", time.Now().UTC())
	msg := runCmd(env.model.refreshCmd())
	m, _ := send(env.model, msg)
	got := listHosts(m)
	if len(got) != 2 {
		t.Fatalf("expected 2 items after refresh, got %v", got)
	}
	if got[0] != "fresh.example.com" {
		t.Errorf("new denial not at the top: %v", got)
	}
	if cmd := env.model.Init(); cmd == nil {
		t.Error("Init did not schedule a refresh tick")
	}
}

func TestApproveEmptySelection(t *testing.T) {
	env := newTestEnv(t, "alpha.example.com")
	m, cmd := send(env.model, keyMsg("enter"))
	if cmd != nil {
		t.Error("approve with an empty selection should be a no-op")
	}
	if !strings.Contains(m.status, "select hosts") {
		t.Errorf("unexpected status %q", m.status)
	}
}

func TestApproveConfigFailure(t *testing.T) {
	env := newTestEnv(t, "alpha.example.com")
	env.model.configPath = filepath.Join(env.dir, "missing.hsh")
	m, _ := send(env.model, keyMsg(" "))
	m, cmd := send(m, keyMsg("enter"))
	msg := runCmd(cmd)
	if msg == nil {
		t.Fatal("expected an action command")
	}
	m, _ = send(m, msg)
	if !strings.Contains(m.status, "error") {
		t.Errorf("config failure not surfaced: %q", m.status)
	}
	pending, err := denieddb.Pending(env.db)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 {
		t.Errorf("DB marked allowed despite the config failure: %+v", pending)
	}
}

func TestQuitKeys(t *testing.T) {
	env := newTestEnv(t, "alpha.example.com")
	_, cmd := send(env.model, keyMsg("esc"))
	if cmd == nil {
		t.Fatal("esc did not quit")
	}
	if _, ok := runCmd(cmd).(tea.QuitMsg); !ok {
		t.Errorf("esc did not produce a quit message: %T", runCmd(cmd))
	}
}
