package monitordb

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
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

func (e *env) allowConfig(id string, hosts ...string) {
	e.t.Helper()
	data, err := json.Marshal(map[string]any{"network": map[string]any{"allow": hosts}})
	if err != nil {
		e.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(e.dir(id), "config.json"), data, 0o644); err != nil {
		e.t.Fatal(err)
	}
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

func TestMarkIgnored(t *testing.T) {
	e := newEnv(t)
	e.record("s1", "a.example")
	if err := e.store().MarkIgnored("s1", []string{"a.example"}); err != nil {
		t.Fatal(err)
	}
	pending, _ := e.store().Pending(Filter{})
	if len(pending) != 0 {
		t.Errorf("ignored host still pending: %+v", pending)
	}
	ignored, _ := e.store().Ignored(Filter{})
	if len(ignored) != 1 || !ignored[0].Ignored || ignored[0].AllowedAt == "" {
		t.Fatalf("ignored = %+v", ignored)
	}
	e.record("s1", "a.example")
	if pending, _ := e.store().Pending(Filter{}); len(pending) != 0 {
		t.Errorf("a later denial brought an ignored host back to the review list: %+v", pending)
	}
}

func TestMarkAllowedClearsIgnored(t *testing.T) {
	e := newEnv(t)
	e.record("s1", "a.example")
	e.allowConfig("s1", "a.example")
	if err := e.store().MarkIgnored("s1", []string{"a.example"}); err != nil {
		t.Fatal(err)
	}
	if err := e.store().MarkAllowed("s1", []string{"a.example"}); err != nil {
		t.Fatal(err)
	}
	if ignored, _ := e.store().Ignored(Filter{}); len(ignored) != 0 {
		t.Errorf("approved host still listed as ignored: %+v", ignored)
	}
	all, _ := e.store().All(Filter{})
	if len(all) != 1 || !all[0].Allowed || all[0].Ignored {
		t.Errorf("unexpected row after approve: %+v", all)
	}
	e.allowConfig("s1")
	pending, _ := e.store().Pending(Filter{})
	if len(pending) != 1 || pending[0].Allowed {
		t.Fatalf("removing the host from the config must put it back in the review list: %+v", pending)
	}
}

func TestDecisionsReachTheProxy(t *testing.T) {
	e := newEnv(t)
	e.record("s1", "a.example")
	e.record("s2", "a.example")
	d := NewDecisions(MonitorDBPath(e.dir("s1")))
	if got := d.Lookup("a.example"); got.Found {
		t.Fatalf("undecided: %+v", got)
	}
	if err := e.store().MarkIgnored("s1", []string{"a.example"}); err != nil {
		t.Fatal(err)
	}
	if got := d.Lookup("a.example"); !got.Ignored {
		t.Fatalf("ignored: %+v", got)
	}
	if got := NewDecisions(MonitorDBPath(e.dir("s2"))).Lookup("a.example"); got.Found {
		t.Fatalf("another session's decision leaked: %+v", got)
	}
	if err := e.store().MarkAllowed("s1", []string{"a.example"}); err != nil {
		t.Fatal(err)
	}
	if got := d.Lookup("a.example"); !got.Allowed || got.Ignored {
		t.Fatalf("approved: %+v", got)
	}
	if got, _ := e.store().Lookup("s1", "a.example"); !got.Allowed {
		t.Fatalf("store lookup: %+v", got)
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

func TestMarkAllowedNeedsASessionDirectory(t *testing.T) {
	e := newEnv(t)
	if err := e.store().MarkAllowed("missing", []string{"a.example"}); err == nil {
		t.Fatal("decided for a session that does not exist")
	}
	if err := e.store().MarkAllowed("../x", []string{"a.example"}); err == nil {
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
			if err := e.store().MarkAllowed("s1", []string{fmt.Sprintf("h%d.example", i)}); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	d := NewDecisions(MonitorDBPath(e.dir("s1")))
	for i := 0; i < 20; i++ {
		if !d.Lookup(fmt.Sprintf("h%d.example", i)).Allowed {
			t.Fatalf("decision %d was lost", i)
		}
	}
}
