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

	"hsh/internal/monitordb"
	"hsh/internal/state"
)

type testEnv struct {
	model      Model
	db         *sql.DB
	configPath string
	dir        string
	session    string
}

func recordAt(t *testing.T, db *sql.DB, host, target string, lastSeen time.Time, session string) {
	t.Helper()
	stamp := lastSeen.UTC().Format(time.RFC3339)
	_, err := db.Exec(`INSERT INTO denied (host, session, workspace, target, count, first_seen, last_seen, allowed, allowed_at, ignored)
		VALUES (?, ?, ?, ?, 1, ?, ?, 0, NULL, 0)`, host, session, "/tmp/ws", target, stamp, stamp)
	if err != nil {
		t.Fatal(err)
	}
}

func newTestEnv(t *testing.T, hosts ...string) *testEnv {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", filepath.Join(dir, "state"))
	db, err := monitordb.Open(filepath.Join(dir, "monitor.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	session := "testsess"
	sessionDir := state.SessionDir(session)
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(sessionDir, "config.json")
	if err := os.WriteFile(configPath, []byte(`{"version": 1, "network": {"allow": ["already.example.com"]}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	env := &testEnv{db: db, configPath: configPath, dir: dir, session: session}
	for i, h := range hosts {
		recordAt(t, db, h, h+":443", time.Now().UTC().Add(-time.Duration(len(hosts)-i)*time.Hour), session)
	}
	pending, err := monitordb.Pending(db, monitordb.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	env.model = New(db, monitordb.Filter{}, pending, nil, false)
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
	for _, r := range m.rows {
		hosts = append(hosts, r.Host)
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
	all, err := monitordb.All(env.db, monitordb.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	var row *monitordb.DeniedHost
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
	ignored, err := monitordb.Ignored(env.db, monitordb.Filter{})
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
	for j, r := range m.rows {
		if r.Host == "beta.example.com" {
			idx = j
			if !r.Ignored {
				t.Error("row shown in the ignored view is not marked ignored")
			}
		}
	}
	if idx < 0 {
		t.Fatal("ignored host not shown after toggling show-ignored")
	}

	m.table.SetCursor(idx)
	m, _ = send(m, keyMsg(" "))
	if m.selected[rowKey(m.rows[idx])] {
		t.Error("ignored host became selectable")
	}
	if m.status != "ignored rows cannot be selected" {
		t.Errorf("unexpected status %q", m.status)
	}

	pending, err := monitordb.Pending(env.db, monitordb.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	ignored, err := monitordb.Ignored(env.db, monitordb.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	m2 := New(env.db, monitordb.Filter{}, pending, ignored, true)
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

func TestSelectAllAndNone(t *testing.T) {
	env := newTestEnv(t, "alpha.example.com", "beta.example.com", "gamma.example.com")
	m, _ := send(env.model, keyMsg("a"))
	if len(m.selected) != 3 {
		t.Fatalf("select-all selected %d, want 3", len(m.selected))
	}
	m, _ = send(m, keyMsg("n"))
	if len(m.selected) != 0 {
		t.Fatalf("select-none left %d selected", len(m.selected))
	}
}

func TestRefreshBringsNewDenials(t *testing.T) {
	env := newTestEnv(t, "alpha.example.com")
	recordAt(t, env.db, "fresh.example.com", "fresh.example.com:443", time.Now().UTC(), env.session)
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
	if err := os.Remove(env.configPath); err != nil {
		t.Fatal(err)
	}
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
	pending, err := monitordb.Pending(env.db, monitordb.Filter{})
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

func typeRunes(m Model, s string) Model {
	for _, r := range s {
		m, _ = send(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	return m
}

func TestFilterLiveAndAccept(t *testing.T) {
	env := newTestEnv(t, "alpha.example.com", "beta.example.com")
	m, _ := send(env.model, keyMsg("/"))
	if !m.filtering {
		t.Fatal("pressing / did not start filtering")
	}
	m = typeRunes(m, "beta")
	if got := listHosts(m); len(got) != 1 || got[0] != "beta.example.com" {
		t.Fatalf("live filter did not narrow to beta: %v", got)
	}
	m, _ = send(m, keyMsg("enter"))
	if m.filtering {
		t.Fatal("enter did not accept the filter")
	}
	if got := listHosts(m); len(got) != 1 {
		t.Fatalf("accepted filter not applied: %v", got)
	}
	m, _ = send(m, keyMsg("/"))
	m, _ = send(m, keyMsg("esc"))
	if m.filterText() != "" {
		t.Fatal("esc did not clear the filter")
	}
	if got := listHosts(m); len(got) != 2 {
		t.Fatalf("cleared filter did not restore all rows: %v", got)
	}
}

func TestFilterTabCompletion(t *testing.T) {
	env := newTestEnv(t, "alpha.example.com", "beta.example.com")
	m, _ := send(env.model, keyMsg("/"))
	m = typeRunes(m, "al")
	m, _ = send(m, tea.KeyMsg{Type: tea.KeyTab})
	if got := m.filterInput.Value(); got != "alpha.example.com" {
		t.Fatalf("tab did not complete the unique prefix: %q", got)
	}
	if got := listHosts(m); len(got) != 1 || got[0] != "alpha.example.com" {
		t.Fatalf("completed filter not applied: %v", got)
	}

	m, _ = send(m, keyMsg("esc"))
	m, _ = send(m, keyMsg("/"))
	m = typeRunes(m, "example")
	m, _ = send(m, tea.KeyMsg{Type: tea.KeyTab})
	if got := m.filterInput.Value(); got != "example" {
		t.Fatalf("ambiguous completion should keep the common prefix: %q", got)
	}
	if !strings.Contains(m.status, "completions:") {
		t.Fatalf("ambiguous completion should list candidates: %q", m.status)
	}

	m, _ = send(m, keyMsg("esc"))
	m, _ = send(m, keyMsg("/"))
	m = typeRunes(m, "zzz")
	m, _ = send(m, tea.KeyMsg{Type: tea.KeyTab})
	if !strings.Contains(m.status, "no completion") {
		t.Fatalf("no-match completion status: %q", m.status)
	}
}

func TestRefreshEmptyQueueDoesNotPanic(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", filepath.Join(dir, "state"))
	db, err := monitordb.Open(filepath.Join(dir, "monitor.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	m := New(db, monitordb.Filter{}, nil, nil, false)
	msg := runCmd(m.refreshCmd())
	updated, _ := send(m, msg)
	if len(updated.rows) != 0 {
		t.Fatalf("expected no rows, got %d", len(updated.rows))
	}
	updated, _ = send(updated, refreshMsg{})
	if len(updated.rows) != 0 {
		t.Fatalf("expected no rows after empty refresh, got %d", len(updated.rows))
	}
}

func TestRefreshToEmptyPreservesNoCursor(t *testing.T) {
	env := newTestEnv(t, "alpha.example.com")
	m := env.model
	msg := refreshMsg{pending: nil, ignored: nil}
	updated, _ := send(m, msg)
	if len(updated.rows) != 0 {
		t.Fatalf("expected no rows, got %d", len(updated.rows))
	}
	updated, _ = send(updated, refreshMsg{pending: env.pendingSnapshot()})
	if len(updated.rows) != 1 {
		t.Fatalf("expected rows back, got %d", len(updated.rows))
	}
}

func (e *testEnv) pendingSnapshot() []monitordb.DeniedHost {
	pending, err := monitordb.Pending(e.db, monitordb.Filter{})
	if err != nil {
		return nil
	}
	return pending
}

func approvalWaitInConfig(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Network struct {
			ApprovalWait string `json:"approval_wait"`
		} `json:"network"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	return cfg.Network.ApprovalWait
}

func TestToggleHoldWritesTheSessionConfigAndUpdatesTheTitle(t *testing.T) {
	env := newTestEnv(t, "alpha.example.com")
	m, _ := send(env.model, tea.WindowSizeMsg{Width: 140, Height: 30})
	if view := m.View(); !strings.Contains(view, "holding: off (session testsess)") {
		t.Fatalf("the title does not show that holding is off by default:\n%s", view)
	}

	m, cmd := send(m, keyMsg("w"))
	msg := runCmd(cmd)
	if msg == nil {
		t.Fatal("w returned no command")
	}
	m, _ = send(m, msg)
	if got := approvalWaitInConfig(t, env.configPath); got != "30s" {
		t.Fatalf("approval_wait = %q after turning holding on", got)
	}
	if !hasHost(allowlist(t, env.configPath), "already.example.com") {
		t.Fatal("toggling holding lost the allowlist")
	}
	view := m.View()
	if !strings.Contains(view, "holding: 30s (session testsess)") || !strings.Contains(view, "denied requests wait for approval") {
		t.Fatalf("view after turning holding on:\n%s", view)
	}

	m, cmd = send(m, keyMsg("w"))
	m, _ = send(m, runCmd(cmd))
	if got := approvalWaitInConfig(t, env.configPath); got != "off" {
		t.Fatalf("approval_wait = %q after turning holding off", got)
	}
	if !strings.Contains(m.View(), "holding: off") || !strings.Contains(m.View(), "holding off — denied requests fail immediately") {
		t.Fatalf("view after turning holding off:\n%s", m.View())
	}
}

func TestToggleHoldNeedsASession(t *testing.T) {
	env := newTestEnv(t)
	m, cmd := send(env.model, keyMsg("w"))
	if cmd != nil {
		t.Fatal("w with no row and no session scope returned a command")
	}
	if !strings.Contains(m.status, "no session") {
		t.Fatalf("status = %q", m.status)
	}
}

func TestToggleHoldActsOnTheSelectedRowsSession(t *testing.T) {
	env := newTestEnv(t, "alpha.example.com")
	other := state.SessionDir("othersess")
	if err := os.MkdirAll(other, 0o755); err != nil {
		t.Fatal(err)
	}
	otherCfg := filepath.Join(other, "config.json")
	if err := os.WriteFile(otherCfg, []byte(`{"version":1,"network":{"allow":[]}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	recordAt(t, env.db, "zeta.example.com", "zeta.example.com:443", time.Now().UTC().Add(time.Hour), "othersess")
	pending, _ := monitordb.Pending(env.db, monitordb.Filter{})
	m := New(env.db, monitordb.Filter{}, pending, nil, false)
	if m.rows[0].Session != "othersess" {
		t.Fatalf("expected the most recent row (othersess) first, got %s", m.rows[0].Session)
	}
	m, cmd := send(m, keyMsg("w"))
	m, _ = send(m, runCmd(cmd))
	if approvalWaitInConfig(t, otherCfg) != "30s" {
		t.Fatal("the selected row's session was not changed")
	}
	if approvalWaitInConfig(t, env.configPath) != "" {
		t.Fatal("another session's config was changed")
	}
}

func TestTypingWInTheFilterDoesNotToggleHolding(t *testing.T) {
	env := newTestEnv(t, "alpha.example.com")
	m, _ := send(env.model, keyMsg("/"))
	m, cmd := send(m, keyMsg("w"))
	if cmd != nil {
		if msg := runCmd(cmd); msg != nil {
			if _, ok := msg.(holdMsg); ok {
				t.Fatal("w typed into the filter toggled holding")
			}
		}
	}
	if m.filterInput.Value() != "w" {
		t.Fatalf("filter = %q", m.filterInput.Value())
	}
	if approvalWaitInConfig(t, env.configPath) != "" {
		t.Fatal("the config changed")
	}
}

func TestHeldHostsAreListedFirstAndMarked(t *testing.T) {
	env := newTestEnv(t, "alpha.example.com", "beta.example.com")
	if err := monitordb.AdjustHolding(env.db, env.session, "alpha.example.com", 2); err != nil {
		t.Fatal(err)
	}
	pending, _ := monitordb.Pending(env.db, monitordb.Filter{})
	m := New(env.db, monitordb.Filter{}, pending, nil, false)
	m, _ = send(m, tea.WindowSizeMsg{Width: 160, Height: 30})
	if m.rows[0].Host != "alpha.example.com" {
		t.Fatalf("the held host is not first: %v", listHosts(m))
	}
	if !strings.Contains(m.View(), "HELD x2") {
		t.Fatalf("the held marker is missing:\n%s", m.View())
	}
}
