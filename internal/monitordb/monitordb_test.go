package monitordb

import (
	"bufio"
	"context"
	"database/sql"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

type env struct {
	t    *testing.T
	root string
	recs map[string]*Recorder
}

func newEnv(t *testing.T) *env {
	return &env{t: t, root: t.TempDir(), recs: map[string]*Recorder{}}
}

func (e *env) store() *Store { return NewStore(e.root) }

func (e *env) dir(session string) string { return filepath.Join(e.root, session) }

func (e *env) session(id, workspace string) *Recorder {
	e.t.Helper()
	if r, ok := e.recs[id]; ok {
		return r
	}
	if err := os.MkdirAll(filepath.Join(e.dir(id), "proxy"), 0o700); err != nil {
		e.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(e.dir(id), "workspace"), []byte(workspace+"\n"), 0o644); err != nil {
		e.t.Fatal(err)
	}
	r, err := OpenRecorder(ProxyDBPath(e.dir(id)))
	if err != nil {
		e.t.Fatal(err)
	}
	e.recs[id] = r
	return r
}

func (e *env) record(id, host string) {
	e.t.Helper()
	if err := e.session(id, "/ws/"+id).Record(host, host+":443"); err != nil {
		e.t.Fatal(err)
	}
}

func hosts(rows []DeniedHost) string {
	var out []string
	for _, r := range rows {
		out = append(out, r.Host+"@"+r.Session)
	}
	return strings.Join(out, ",")
}

func TestReadersUnionSessionsWithHostSideAttribution(t *testing.T) {
	e := newEnv(t)
	e.record("s1", "a.example")
	e.record("s1", "a.example")
	e.record("s2", "b.example")
	rows, err := e.store().Pending(Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if got := hosts(rows); got != "a.example@s1,b.example@s2" {
		t.Fatalf("pending = %s", got)
	}
	if rows[0].Count != 2 || rows[0].Workspace != "/ws/s1" || rows[0].Target != "a.example:443" || rows[0].FirstSeen == "" {
		t.Fatalf("row = %+v", rows[0])
	}
	rows, _ = e.store().Pending(Filter{Workspace: "/ws/s2"})
	if got := hosts(rows); got != "b.example@s2" {
		t.Fatalf("workspace filter = %s", got)
	}
	rows, _ = e.store().Pending(Filter{Session: "s2"})
	if got := hosts(rows); got != "b.example@s2" {
		t.Fatalf("session filter = %s", got)
	}
	rows, _ = e.store().Pending(Filter{Session: "../s1"})
	if len(rows) != 0 {
		t.Fatalf("a session filter escaped the sessions directory: %+v", rows)
	}
}

func TestDenyTakesAHostOutOfTheQueue(t *testing.T) {
	e := newEnv(t)
	e.record("s1", "a.example")
	if err := e.store().Deny("s1", []string{"a.example"}); err != nil {
		t.Fatal(err)
	}
	if pending, _ := e.store().Pending(Filter{}); len(pending) != 0 {
		t.Errorf("denied host still pending: %+v", pending)
	}
	denied, _ := e.store().Denied(Filter{})
	if len(denied) != 1 || !denied[0].Denied() || denied[0].Source != SourceUser || denied[0].DecidedAt == "" || denied[0].DeniedBy != "denied by you" {
		t.Fatalf("denied = %+v", denied)
	}
	e.record("s1", "a.example")
	if pending, _ := e.store().Pending(Filter{}); len(pending) != 0 {
		t.Errorf("a later denial brought a denied host back to the review list: %+v", pending)
	}
}

func TestTheLatestUserDecisionWins(t *testing.T) {
	e := newEnv(t)
	e.record("s1", "a.example")
	if err := e.store().Deny("s1", []string{"a.example"}); err != nil {
		t.Fatal(err)
	}
	if err := e.store().Allow("s1", []string{"a.example"}); err != nil {
		t.Fatal(err)
	}
	if denied, _ := e.store().Denied(Filter{}); len(denied) != 0 {
		t.Errorf("approved host still listed as denied: %+v", denied)
	}
	all, _ := e.store().All(Filter{})
	if len(all) != 1 || all[0].State != StateAllowed {
		t.Errorf("unexpected row after approve: %+v", all)
	}
	rules, err := e.store().Rules("s1")
	if err != nil || len(rules) != 1 || rules[0].Access != AccessAllow {
		t.Fatalf("one user row per pattern: %+v %v", rules, err)
	}
}

func TestInitSessionCopiesTheAllowlistOnce(t *testing.T) {
	e := newEnv(t)
	e.record("s1", "a.example")
	e.record("s1", "b.test")
	if err := InitSession(e.dir("s1"), []string{"a.example", "*.test"}); err != nil {
		t.Fatal(err)
	}
	rules, _ := e.store().Rules("s1")
	if len(rules) != 2 || rules[0].Source != SourceConfig || rules[0].Access != AccessAllow {
		t.Fatalf("rules = %+v", rules)
	}
	if pending, _ := e.store().Pending(Filter{}); len(pending) != 0 {
		t.Fatalf("hosts the copied allowlist matches are not pending: %+v", pending)
	}
	if err := e.store().Deny("s1", []string{"a.example"}); err != nil {
		t.Fatal(err)
	}
	if err := InitSession(e.dir("s1"), []string{"c.example"}); err != nil {
		t.Fatal(err)
	}
	rules, _ = e.store().Rules("s1")
	if len(rules) != 3 {
		t.Fatalf("resume must not copy the allowlist again: %+v", rules)
	}
	if denied, _ := e.store().Denied(Filter{}); hosts(denied) != "a.example@s1" {
		t.Fatalf("a user deny beats the config allow: %s", hosts(denied))
	}
}

func TestEvaluate(t *testing.T) {
	rules := []Rule{
		{Pattern: "*.example.com", Source: SourceConfig, Access: AccessAllow},
		{Pattern: "bad.example.com", Source: SourceUser, Access: AccessDeny},
		{Pattern: "bad.example.com", Source: "policy:corp", Access: AccessDeny},
		{Pattern: "quiet.test", Source: "policy:q", Log: LogRecord},
		{Pattern: "quiet.test", Source: "policy:r", Log: LogReview},
		{Pattern: "loud.test", Source: "policy:r", Log: LogReview},
	}
	cases := []struct {
		host, access, log, source string
	}{
		{"api.example.com", AccessAllow, "", SourceConfig},
		{"BAD.example.com.", AccessDeny, "", "policy:corp"},
		{"example.com", "", "", ""},
		{"quiet.test", "", LogRecord, ""},
		{"loud.test", "", LogReview, ""},
	}
	for _, c := range cases {
		m := Evaluate(rules, c.host)
		if m.Access != c.access || m.Log != c.log || m.Rule.Source != c.source {
			t.Errorf("%s: %+v", c.host, m)
		}
	}
	if m := Evaluate(rules, "bad.example.com"); m.Rule.DeniedBy() != "denied by policy corp" {
		t.Errorf("a policy deny is reported over a user deny: %s", m.Rule.DeniedBy())
	}
}

func TestQuietHostsLeaveTheQueue(t *testing.T) {
	e := newEnv(t)
	e.record("s1", "quiet.test")
	if err := InitSession(e.dir("s1"), nil); err != nil {
		t.Fatal(err)
	}
	rawExec(t, MonitorDBPath(e.dir("s1")), `INSERT INTO rules (pattern, source, access, log, decided_at) VALUES ('quiet.test', 'policy:q', NULL, 'record', '2026-01-01T00:00:00Z')`)
	if pending, _ := e.store().Pending(Filter{}); len(pending) != 0 {
		t.Fatalf("quiet host pending: %+v", pending)
	}
	if all, _ := e.store().All(Filter{}); len(all) != 1 || all[0].State != StateQuiet {
		t.Fatalf("all = %+v", all)
	}
}

func TestRulesReachTheProxy(t *testing.T) {
	e := newEnv(t)
	e.record("s1", "a.example")
	e.record("s2", "a.example")
	var logged []string
	r := NewRuleReader(MonitorDBPath(e.dir("s1")), func(f string, a ...any) { logged = append(logged, fmt.Sprintf(f, a...)) })
	defer r.Close()
	if got := r.Rules(); len(got) != 0 {
		t.Fatalf("no rules yet: %+v", got)
	}
	if len(logged) != 1 || !strings.Contains(logged[0], "missing") {
		t.Fatalf("a missing database is logged once: %q", logged)
	}
	if err := e.store().Deny("s1", []string{"a.example"}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(rulesTTL)
	if m := Evaluate(r.Rules(), "a.example"); m.Access != AccessDeny {
		t.Fatalf("denied: %+v", m)
	}
	if len(logged) != 2 || !strings.Contains(logged[1], "1 rule") {
		t.Fatalf("recovery is logged: %q", logged)
	}
	s2 := NewRuleReader(MonitorDBPath(e.dir("s2")), nil)
	defer s2.Close()
	if got := s2.Rules(); len(got) != 0 {
		t.Fatalf("another session's decision leaked: %+v", got)
	}
	if err := e.store().Allow("s1", []string{"a.example"}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(rulesTTL)
	if m := Evaluate(r.Rules(), "a.example"); m.Access != AccessAllow {
		t.Fatalf("approved: %+v", m)
	}
}

func TestAnOldMonitorDatabaseIsRefused(t *testing.T) {
	e := newEnv(t)
	e.record("s1", "a.example")
	rawExec(t, MonitorDBPath(e.dir("s1")), `CREATE TABLE decisions (host TEXT PRIMARY KEY, ignored INTEGER NOT NULL DEFAULT 0, decided_at TEXT NOT NULL)`, `PRAGMA user_version = 1`)
	if err := e.store().Deny("s1", []string{"a.example"}); err == nil || !strings.Contains(err.Error(), "older than this hsh reads") {
		t.Fatalf("deny on a version 1 database: %v", err)
	}
	if _, err := e.store().Pending(Filter{}); err == nil {
		t.Fatal("readers must refuse a version 1 database")
	}
	r := NewRuleReader(MonitorDBPath(e.dir("s1")), nil)
	defer r.Close()
	if got := r.Rules(); got != nil {
		t.Fatalf("the proxy must use no rules: %+v", got)
	}
}

func TestHoldingCounts(t *testing.T) {
	e := newEnv(t)
	for _, h := range []string{"a.example", "b.example", "b.example", "c.example"} {
		e.record("s1", h)
	}
	e.record("s2", "a.example")
	r := e.session("s1", "")
	holding := func(session string) map[string]int {
		rows, err := e.store().Pending(Filter{Session: session})
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]int{}
		for _, r := range rows {
			out[r.Host] = r.Holding
		}
		return out
	}
	_ = r.AdjustHolding("a.example", 1)
	_ = r.AdjustHolding("a.example", 1)
	if got := holding("s1"); got["a.example"] != 2 || got["b.example"] != 0 {
		t.Fatalf("after two holds: %v", got)
	}
	rows, _ := e.store().Pending(Filter{Session: "s1"})
	if rows[0].Host != "a.example" {
		t.Fatalf("held hosts must be listed first, got %s", rows[0].Host)
	}
	_ = r.AdjustHolding("a.example", -1)
	_ = r.AdjustHolding("c.example", -5)
	if got := holding("s1"); got["a.example"] != 1 || got["c.example"] != 0 {
		t.Fatalf("decrements: %v", got)
	}
	_ = e.session("s2", "").AdjustHolding("a.example", 3)
	if _, err := OpenRecorder(ProxyDBPath(e.dir("s1"))); err != nil {
		t.Fatal(err)
	}
	if got := holding("s1"); got["a.example"] != 0 {
		t.Fatalf("a restarted proxy kept stale holds: %v", got)
	}
	if got := holding("s2"); got["a.example"] != 3 {
		t.Fatalf("another session's counts were reset: %v", got)
	}
}

func TestRecorderRefusesMalformedHosts(t *testing.T) {
	e := newEnv(t)
	r := e.session("s1", "")
	for _, h := range []string{"", "a b", "evil\x1b[2J.example", strings.Repeat("a", 300)} {
		if err := r.Record(h, "x:443"); err == nil {
			t.Errorf("recorded %q", h)
		}
	}
}

func rawExec(t *testing.T, path string, stmts ...string) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, q := range stmts {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
}

func TestReadersTreatTheProxyDatabaseAsUntrusted(t *testing.T) {
	e := newEnv(t)
	e.record("s1", "ok.example")
	e.record("s2", "ok.example")
	rawExec(t, ProxyDBPath(e.dir("s1")),
		"INSERT INTO denied (host, target, count, first_seen, last_seen) VALUES ('bad\x1b[2J', 'x', 9, '', '')",
		"INSERT INTO denied (host, target, count, first_seen, last_seen) VALUES (char(101,27,91,50,74), 'x', 9, '', '')",
		"INSERT INTO denied (host, target, count, first_seen, last_seen) VALUES ('t.example', char(27,93,48), 1, '', '')")
	rows, err := e.store().Pending(Filter{Session: "s1"})
	if err != nil {
		t.Fatal(err)
	}
	if got := hosts(rows); got != "ok.example@s1,t.example@s1" {
		t.Fatalf("pending = %s", got)
	}
	for _, r := range rows {
		if r.Host == "t.example" && r.Target != "t.example" {
			t.Fatalf("a malformed target was kept: %q", r.Target)
		}
	}
	entries, _ := os.ReadDir(filepath.Join(e.dir("s1"), "proxy"))
	if len(entries) != 1 || entries[0].Name() != ProxyDBFile {
		t.Fatalf("the reader created files beside the proxy database: %v", entries)
	}

	if err := os.Remove(ProxyDBPath(e.dir("s2"))); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(ProxyDBPath(e.dir("s1")), ProxyDBPath(e.dir("s2"))); err != nil {
		t.Fatal(err)
	}
	if rows, _ := e.store().Pending(Filter{Session: "s2"}); len(rows) != 0 {
		t.Fatalf("a symlinked proxy database was followed: %+v", rows)
	}

	if err := os.WriteFile(ProxyDBPath(e.dir("s1")), []byte("not a database"), 0o600); err != nil {
		t.Fatal(err)
	}
	if rows, err := e.store().Pending(Filter{}); err != nil || len(rows) != 0 {
		t.Fatalf("a corrupt database broke the union: %+v %v", rows, err)
	}
}

func TestANewerProxyDatabaseIsSkippedAndRefusedByTheProxy(t *testing.T) {
	e := newEnv(t)
	e.record("s1", "a.example")
	rawExec(t, ProxyDBPath(e.dir("s1")), "PRAGMA user_version = 2")
	if rows, _ := e.store().Pending(Filter{}); len(rows) != 0 {
		t.Fatalf("a newer database was read: %+v", rows)
	}
	_, err := OpenRecorder(ProxyDBPath(e.dir("s1")))
	if err == nil || !strings.Contains(err.Error(), "newer than this hsh reads (version 1); update hsh") {
		t.Fatalf("err = %v", err)
	}
}

func TestLockHelper(t *testing.T) {
	path, mode := os.Getenv("HSH_LOCK_HELPER_DB"), os.Getenv("HSH_LOCK_HELPER_MODE")
	if path == "" {
		t.Skip("helper process for the lock tests")
	}
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=journal_mode(DELETE)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	switch mode {
	case "exclusive":
		_, err = conn.ExecContext(ctx, "BEGIN EXCLUSIVE")
	case "writing":
		if _, err = conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err == nil {
			_, err = conn.ExecContext(ctx, "INSERT INTO denied (host, target, count, first_seen, last_seen) VALUES ('uncommitted.example', 'x:443', 1, '', '')")
		}
	}
	if err != nil {
		t.Fatal(err)
	}
	fmt.Println("ready")
	_, _ = io.ReadAll(os.Stdin)
}

func holdLock(t *testing.T, path, mode string) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestLockHelper$")
	cmd.Env = append(os.Environ(), "HSH_LOCK_HELPER_DB="+path, "HSH_LOCK_HELPER_MODE="+mode)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		stdin.Close()
		_ = cmd.Wait()
	})
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil || strings.TrimSpace(line) != "ready" {
		t.Fatalf("lock helper: %q %v", line, err)
	}
}

func TestAProxyHoldingALockDoesNotStallTheReaders(t *testing.T) {
	e := newEnv(t)
	e.record("s1", "a.example")
	e.record("s2", "b.example")
	holdLock(t, ProxyDBPath(e.dir("s1")), "exclusive")
	start := time.Now()
	rows, err := e.store().Pending(Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatalf("a locked proxy database stalled the readers for %s", time.Since(start))
	}
	if got := hosts(rows); got != "b.example@s2" {
		t.Fatalf("pending = %s", got)
	}
}

func TestAWriteInProgressShowsTheCommittedRows(t *testing.T) {
	e := newEnv(t)
	e.record("s1", "a.example")
	holdLock(t, ProxyDBPath(e.dir("s1")), "writing")
	if _, err := os.Stat(ProxyDBPath(e.dir("s1")) + "-journal"); err != nil {
		t.Fatalf("the writer has no journal: %v", err)
	}
	rows, _ := e.store().Pending(Filter{})
	if got := hosts(rows); got != "a.example@s1" {
		t.Fatalf("pending during a write = %s", got)
	}
}

func TestAnUnfinishedWriteHidesTheSession(t *testing.T) {
	e := newEnv(t)
	e.record("s1", "a.example")
	if err := os.WriteFile(ProxyDBPath(e.dir("s1"))+"-journal", []byte("journal from a crashed writer"), 0o600); err != nil {
		t.Fatal(err)
	}
	if rows, _ := e.store().Pending(Filter{}); len(rows) != 0 {
		t.Fatalf("read a database with a hot journal: %+v", rows)
	}
}

func TestSwappedInFilesAreRefusedWithoutBlocking(t *testing.T) {
	e := newEnv(t)
	e.record("s1", "a.example")
	path := ProxyDBPath(e.dir("s1"))
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}
	done := make(chan []DeniedHost, 1)
	go func() {
		rows, _ := e.store().Pending(Filter{})
		done <- rows
	}()
	select {
	case rows := <-done:
		if len(rows) != 0 {
			t.Fatalf("read a FIFO: %+v", rows)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("a FIFO in place of proxy.db blocked the reader")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(path+"-journal", 0o600); err != nil {
		t.Fatal(err)
	}
	e.recs = map[string]*Recorder{}
	if err := os.Remove(path + "-journal"); err != nil {
		t.Fatal(err)
	}
	e.record("s1", "a.example")
	if err := syscall.Mkfifo(path+"-journal", 0o600); err != nil {
		t.Fatal(err)
	}
	go func() {
		rows, _ := e.store().Pending(Filter{})
		done <- rows
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("a FIFO in place of the journal blocked the reader")
	}
}

func TestDecisionsNeedASessionDirectory(t *testing.T) {
	e := newEnv(t)
	if err := e.store().Allow("missing", []string{"a.example"}); err == nil {
		t.Fatal("decided for a session that does not exist")
	}
	if err := e.store().Deny("../x", []string{"a.example"}); err == nil {
		t.Fatal("decided outside the sessions directory")
	}
}

func TestConcurrentDecisionsAreAllKept(t *testing.T) {
	e := newEnv(t)
	e.session("s1", "")
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := e.store().Allow("s1", []string{fmt.Sprintf("h%d.example", i)}); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	r := NewRuleReader(MonitorDBPath(e.dir("s1")), nil)
	defer r.Close()
	rules := r.Rules()
	for i := 0; i < 20; i++ {
		if Evaluate(rules, fmt.Sprintf("h%d.example", i)).Access != AccessAllow {
			t.Fatalf("decision %d was lost", i)
		}
	}
}
