package monitordb

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func (e *env) connLog(id string) *ConnLog {
	e.t.Helper()
	e.session(id, "/ws/"+id)
	l, err := OpenConnLog(ConnLogPath(e.dir(id)))
	if err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(func() { l.Close() })
	return l
}

func conn(host, access, logLevel string) Connection {
	return Connection{Time: time.Now().UTC().Format(time.RFC3339), Host: host, Port: 443, Scheme: SchemeTLS, Access: access, Log: logLevel}
}

func TestConnectionLogInsertUpdateAndRead(t *testing.T) {
	e := newEnv(t)
	l := e.connLog("s1")
	if rows, err := e.store().Connections("s1", 0, 10); err != nil || len(rows) != 0 {
		t.Fatalf("empty log: %+v %v", rows, err)
	}
	id, err := l.Insert(conn("a.example", AccessDeny, LogReview))
	if err != nil || id != 1 {
		t.Fatalf("insert: %d %v", id, err)
	}
	c := conn("a.example", AccessAllow, LogReview)
	c.ID, c.HeldMS, c.BytesUp, c.BytesDown, c.DurationMS, c.Pattern, c.Source = id, 1200, 10, 20, 1500, "a.example", SourceUser
	if err := l.Update(c); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Insert(conn("b.example", AccessAllow, LogRecord)); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Insert(conn("bad\x1b[2J", AccessAllow, LogRecord)); err == nil {
		t.Fatal("a malformed host was logged")
	}
	rows, err := e.store().Connections("s1", 0, 10)
	if err != nil || len(rows) != 2 {
		t.Fatalf("%+v %v", rows, err)
	}
	r := rows[0]
	if r.Session != "s1" || r.Access != AccessAllow || r.HeldMS != 1200 || r.BytesDown != 20 || r.Source != SourceUser || r.Pattern != "a.example" {
		t.Fatalf("row = %+v", r)
	}
	if rows, _ := e.store().Connections("s1", 1, 10); len(rows) != 1 || rows[0].Host != "b.example" {
		t.Fatalf("since: %+v", rows)
	}
	if last, err := e.store().LastConnectionID("s1"); err != nil || last != 2 {
		t.Fatalf("last = %d %v", last, err)
	}
	if last, err := e.store().LastConnectionID("nolog"); err != nil || last != 0 {
		t.Fatalf("a session without a log: %d %v", last, err)
	}
}

func TestConnectionLogIsCapped(t *testing.T) {
	old := MaxConnections
	MaxConnections = 5
	t.Cleanup(func() { MaxConnections = old })
	e := newEnv(t)
	l := e.connLog("s1")
	for i := 0; i < 12; i++ {
		if _, err := l.Insert(conn("a.example", AccessAllow, LogRecord)); err != nil {
			t.Fatal(err)
		}
	}
	rows, _ := e.store().Connections("s1", 0, 100)
	if len(rows) != 5 || rows[0].ID != 8 || rows[4].ID != 12 {
		t.Fatalf("rows = %d, first %d", len(rows), rows[0].ID)
	}
	l.Close()
	l2, err := OpenConnLog(ConnLogPath(e.dir("s1")))
	if err != nil {
		t.Fatal(err)
	}
	defer l2.Close()
	if id, _ := l2.Insert(conn("a.example", AccessAllow, LogRecord)); id != 13 {
		t.Fatalf("ids were reused after deleting: %d", id)
	}
}

func TestConnectionLogReadersTreatTheFileAsUntrusted(t *testing.T) {
	e := newEnv(t)
	l := e.connLog("s1")
	if _, err := l.Insert(conn("ok.example", AccessAllow, LogRecord)); err != nil {
		t.Fatal(err)
	}
	l.Close()
	rawExec(t, ConnLogPath(e.dir("s1")),
		"INSERT INTO connections (time, host, port, scheme, access, log) VALUES ('2026-01-01T00:00:00Z', 'e\x1b[2J', 443, 'tls', 'allow', 'record')",
		"INSERT INTO connections (time, host, port, scheme, access, log) VALUES ('2026-01-01T00:00:00Z', 'x.example', 443, 'gopher', 'allow', 'record')",
		"INSERT INTO connections (time, host, port, scheme, access, log) VALUES ('2026-01-01T00:00:00Z', 'x.example', 443, 'tls', 'maybe', 'record')",
		"INSERT INTO connections (time, host, port, scheme, access, log, source) VALUES ('2026-01-01T00:00:00Z', 'x.example', 443, 'tls', 'allow', 'record', char(27))",
		"INSERT INTO connections (time, host, port, scheme, access, log, pattern) VALUES ('2026-01-01T00:00:00Z', 'x.example', 443, 'tls', 'allow', 'record', '*.com')",
		"INSERT INTO connections (time, host, port, scheme, access, log) VALUES ('not a time', 'x.example', 443, 'tls', 'allow', 'record')",
		"INSERT INTO connections (time, host, port, scheme, access, log) VALUES ('2026-01-01T00:00:00Z', 'x.example', 99999, 'tls', 'allow', 'record')",
		"INSERT INTO connections (time, host, port, scheme, access, log, held_ms) VALUES ('2026-01-01T00:00:00Z', 'x.example', 443, 'tls', 'allow', 'record', -1)",
		"INSERT INTO connections (time, host, port, scheme, access, log, source) VALUES ('2026-01-01T00:00:00Z', 'good.example', 443, 'tls', 'deny', 'record', 'policy:corp')",
	)
	rows, err := e.store().Connections("s1", 0, 100)
	if err != nil || len(rows) != 2 || rows[0].Host != "ok.example" || rows[1].Host != "good.example" {
		t.Fatalf("%+v %v", rows, err)
	}
	path := ConnLogPath(e.dir("s1"))
	if err := os.Rename(path, path+".real"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Base(path)+".real", path); err != nil {
		t.Fatal(err)
	}
	if _, err := e.store().Connections("s1", 0, 100); err == nil {
		t.Fatal("a symlinked connection log was read")
	}
	if _, err := e.store().Connections("../s1", 0, 100); err == nil {
		t.Fatal("a session outside the sessions directory was read")
	}
}

func TestConnLogChanged(t *testing.T) {
	e := newEnv(t)
	if _, _, changed := e.store().ConnLogChanged("s1", time.Time{}, 0); changed {
		t.Fatal("a missing log changed")
	}
	l := e.connLog("s1")
	mt, size, changed := e.store().ConnLogChanged("s1", time.Time{}, 0)
	if !changed {
		t.Fatal("a new log did not change")
	}
	if _, _, changed := e.store().ConnLogChanged("s1", mt, size); changed {
		t.Fatal("an untouched log changed")
	}
	time.Sleep(10 * time.Millisecond)
	if _, err := l.Insert(conn("a.example", AccessAllow, LogRecord)); err != nil {
		t.Fatal(err)
	}
	if _, _, changed := e.store().ConnLogChanged("s1", mt, size); !changed {
		t.Fatal("an insert did not change the log")
	}
}
