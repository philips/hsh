package cli

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"hsh/internal/monitordb"
	"hsh/internal/policy"
	"hsh/internal/state"
)

func connLogFor(t *testing.T) *monitordb.ConnLog {
	t.Helper()
	l, err := monitordb.OpenConnLog(monitordb.ConnLogPath(state.SessionDir("s1")))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	return l
}

func logConn(t *testing.T, l *monitordb.ConnLog, host string, port int, scheme, access, logLevel string, up, down int64) int64 {
	t.Helper()
	id, err := l.Insert(monitordb.Connection{Time: time.Now().UTC().Format(time.RFC3339), Host: host, Port: port, Scheme: scheme, Access: access, Log: logLevel, BytesUp: up, BytesDown: down})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestProxyReportSummaryAndDraftPolicy(t *testing.T) {
	proxySession(t)
	out, err := runOut(t, "proxy", "report", "--session", "s1")
	if err != nil || !strings.Contains(out, "session s1 has no connections logged") {
		t.Fatalf("empty: %q %v", out, err)
	}
	l := connLogFor(t)
	logConn(t, l, "proxy.golang.org", 443, "tls", "allow", "record", 100, 2000)
	logConn(t, l, "proxy.golang.org", 443, "tls", "allow", "record", 50, 1000)
	logConn(t, l, "sum.golang.org", 443, "tls", "allow", "record", 1, 2)
	logConn(t, l, "evil.example", 8443, "tls", "deny", "review", 0, 0)
	logConn(t, l, "evil.example", 80, "http", "deny", "review", 0, 0)
	logConn(t, l, "10.0.0.1", 22, "tcp", "allow", "record", 5, 5)

	out, err = runOut(t, "proxy", "report", "--session", "s1")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 5 || !strings.HasPrefix(lines[0], "HOST") {
		t.Fatalf("report:\n%s", out)
	}
	for _, want := range []string{"evil.example      2            80,8443  http,tls", "denied", "proxy.golang.org  2            443      tls       150  3000"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}

	out, err = runOut(t, "proxy", "report", "--session", "s1", "--format", "policy")
	if err != nil {
		t.Fatal(err)
	}
	p, err := policy.Parse([]byte(out), "draft")
	if err != nil {
		t.Fatalf("the draft is not a valid policy: %v\n%s", err, out)
	}
	if got := strings.Join(p.Rules[0].Hosts, ","); got != "10.0.0.1,proxy.golang.org,sum.golang.org" || p.Rules[0].Access != "allow" {
		t.Fatalf("draft hosts = %s", got)
	}
}

func TestProxyReportWait(t *testing.T) {
	proxySession(t)
	l := connLogFor(t)
	old := logConn(t, l, "old.example", 443, "tls", "deny", "review", 0, 0)

	go func() {
		time.Sleep(400 * time.Millisecond)
		logConn(t, l, "allowed.example", 443, "tls", "allow", "record", 1, 1)
		time.Sleep(400 * time.Millisecond)
		logConn(t, l, "denied.example", 80, "http", "deny", "review", 0, 0)
	}()
	start := time.Now()
	out, err := runOut(t, "proxy", "report", "--wait", "--denied", "--session", "s1", "--format", "json")
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(start) < 700*time.Millisecond {
		t.Fatal("--wait returned before a denied entry arrived")
	}
	var c monitordb.Connection
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &c); err != nil {
		t.Fatalf("%v: %q", err, out)
	}
	if c.Host != "denied.example" || c.Session != "s1" || c.Access != "deny" || c.Log != "review" || c.ID != old+2 {
		t.Fatalf("entry = %+v", c)
	}

	out, err = runOut(t, "proxy", "report", "--wait", "--session", "s1", "--since", "0")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 3 || !strings.HasPrefix(lines[0], "1 ") || !strings.Contains(lines[0], " old.example 443 tls deny review - -") {
		t.Fatalf("--since 0 must print every entry:\n%s", out)
	}

	_, err = runOut(t, "proxy", "report", "--wait", "--session", "s1", "--since", "3", "--timeout", "300ms")
	if err == nil || ExitStatus(err) != 2 || !strings.Contains(err.Error(), "within 300ms") {
		t.Fatalf("timeout: %v (status %d)", err, ExitStatus(err))
	}
	if _, err := runOut(t, "proxy", "report", "--session", "s1", "--denied"); err == nil {
		t.Fatal("--denied without --wait was accepted")
	}
	if _, err := runOut(t, "proxy", "report", "--wait", "--session", "s1", "--format", "policy"); err == nil {
		t.Fatal("--format policy with --wait was accepted")
	}
}
