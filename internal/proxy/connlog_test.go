package proxy

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"hsh/internal/monitordb"
)

func (e *holdEnv) connections() []monitordb.Connection {
	e.t.Helper()
	rows, err := e.store.Connections("s1", 0, 1000)
	if err != nil {
		e.t.Fatal(err)
	}
	return rows
}

func (e *holdEnv) waitConnections(n int) []monitordb.Connection {
	e.t.Helper()
	var rows []monitordb.Connection
	waitFor(e.t, "connection log rows", func() bool {
		rows = e.connections()
		return len(rows) >= n
	})
	return rows
}

func TestDenialsAreLoggedAtOnce(t *testing.T) {
	e := startHoldProxy(t, "allowlist", "off", true)
	if line, _ := e.send(connectTo("blocked.test:8443")).within(t, 2*time.Second); !strings.Contains(line, "403") {
		t.Fatalf("got %q", line)
	}
	r := e.waitConnections(1)[0]
	if r.Host != "blocked.test" || r.Port != 8443 || r.Scheme != monitordb.SchemeTLS || r.Access != monitordb.AccessDeny || r.Log != monitordb.LogReview || r.Pattern != "" {
		t.Fatalf("row = %+v", r)
	}
	e.deny("noisy.test")
	time.Sleep(300 * time.Millisecond)
	e.send(connectTo("noisy.test:443")).within(t, 2*time.Second)
	r = e.waitConnections(2)[1]
	if r.Access != monitordb.AccessDeny || r.Log != monitordb.LogRecord || r.Source != monitordb.SourceUser || r.Pattern != "noisy.test" {
		t.Fatalf("a rule denial: %+v", r)
	}
}

func TestAHeldRequestIsLoggedBeforeTheHoldEnds(t *testing.T) {
	e := startHoldProxy(t, "allowlist", "10s", true)
	up := upstreamListener(t)
	host, _, _ := net.SplitHostPort(up)
	req := e.send(connectTo(up))
	rows := e.waitConnections(1)
	if rows[0].Access != monitordb.AccessDeny || rows[0].Log != monitordb.LogReview || e.activeHolds() != 1 {
		t.Fatalf("while held: %+v (holds %d)", rows[0], e.activeHolds())
	}
	time.Sleep(300 * time.Millisecond)
	e.allow(host)
	if line, _ := req.within(t, 4*time.Second); !strings.Contains(line, "200") {
		t.Fatalf("got %q", line)
	}
	var r monitordb.Connection
	waitFor(t, "the row to show the approval", func() bool {
		r = e.connections()[0]
		return r.Access == monitordb.AccessAllow
	})
	if len(e.connections()) != 1 || r.HeldMS < 200 || r.Log != monitordb.LogReview || r.Source != monitordb.SourceUser {
		t.Fatalf("after approval: %+v", r)
	}
	req.conn.Close()
	waitFor(t, "the duration to cover the hold", func() bool { return e.connections()[0].DurationMS >= r.HeldMS })
}

func TestAllowedRequestsAreLoggedWhenTheyEnd(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Write([]byte("hello world"))
	}))
	defer srv.Close()
	e := startHoldProxy(t, "allowlist", "off", false)
	e.allow("127.0.0.1")
	time.Sleep(300 * time.Millisecond)
	_, port, _ := net.SplitHostPort(srv.Listener.Addr().String())
	body := "posted-body"
	req := "POST http://127.0.0.1:" + port + "/ HTTP/1.1\r\nHost: 127.0.0.1:" + port + "\r\nContent-Length: 11\r\nConnection: close\r\n\r\n" + body
	if line, _ := e.send(req).within(t, 3*time.Second); !strings.Contains(line, "200") {
		t.Fatalf("got %q", line)
	}
	r := e.waitConnections(1)[0]
	if r.Scheme != monitordb.SchemeHTTP || r.Access != monitordb.AccessAllow || r.Log != monitordb.LogRecord || r.BytesUp != 11 || r.BytesDown != 11 || r.Source != monitordb.SourceUser {
		t.Fatalf("row = %+v", r)
	}
}

func TestReportModeAllowsEverythingButDenies(t *testing.T) {
	e := startHoldProxy(t, "report", "10s", true)
	for _, port := range []string{"443", "8443", "22"} {
		if !e.proxy.connectAllowed("any.test", port) {
			t.Errorf("report mode refused port %s", port)
		}
	}
	if !e.proxy.plaintextAllowed("any.test") || !e.proxy.opaqueAllowed("any.test") {
		t.Error("report mode refused plaintext or raw TCP")
	}
	e.deny("bad.test")
	waitFor(t, "the deny", func() bool { return !e.proxy.plaintextAllowed("bad.test") })
	if e.proxy.connectAllowed("bad.test", "443") {
		t.Error("a deny rule does not apply in report mode")
	}
	start := time.Now()
	if line, _ := e.send(connectTo("bad.test:443")).within(t, 2*time.Second); !strings.Contains(line, "403") || time.Since(start) > time.Second {
		t.Fatalf("a denied host in report mode: %q", line)
	}
}
