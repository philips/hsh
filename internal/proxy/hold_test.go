package proxy

import (
	"bufio"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/miekg/dns"

	"hsh/internal/attend"
	"hsh/internal/certs"
	"hsh/internal/config"
	"hsh/internal/denieddb"
)

type holdEnv struct {
	t        *testing.T
	addr     string
	proxy    *Proxy
	cfgPath  string
	attended string
	db       string
}

func startHoldProxy(t *testing.T, mode, wait string, attended bool) *holdEnv {
	t.Helper()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	writeCfg := func(allow string) {
		doc := fmt.Sprintf(`{"network": {"mode": %q, "allow": [%s]%s}}`, mode, allow, waitJSON(wait))
		if err := os.WriteFile(cfgPath, []byte(doc), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeCfg("")
	ca, err := certs.NewCA()
	if err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string][]byte{"ca.crt": ca.CertPEM(), "ca.key": ca.KeyPEM(), "secrets.json": []byte(`{"secrets":[]}`)} {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	e := &holdEnv{t: t, cfgPath: cfgPath, attended: filepath.Join(dir, "attended"), db: filepath.Join(dir, "denied.db")}
	if attended {
		if err := attend.Touch(e.attended); err != nil {
			t.Fatal(err)
		}
	}
	p, err := New(Options{
		ListenAddr: "127.0.0.1:0", ConfigPath: cfgPath, SecretsPath: filepath.Join(dir, "secrets.json"),
		CACertPath: filepath.Join(dir, "ca.crt"), CAKeyPath: filepath.Join(dir, "ca.key"),
		DeniedDB: e.db, OAuthStore: filepath.Join(dir, "tokens.json"),
		Session: "s1", Workspace: "/ws", AttendedPath: e.attended,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := p.reload(); err != nil {
		t.Fatal(err)
	}
	go p.watch(100 * time.Millisecond)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = (&http.Server{Handler: p}).Serve(ln) }()
	t.Cleanup(func() { ln.Close() })
	e.addr, e.proxy = ln.Addr().String(), p
	return e
}

func waitJSON(wait string) string {
	if wait == "" {
		return ""
	}
	return fmt.Sprintf(`, "approval_wait": %q`, wait)
}

func upstreamListener(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() { defer c.Close(); _, _ = c.Read(make([]byte, 1)) }()
		}
	}()
	return ln.Addr().String()
}

type pending struct {
	conn net.Conn
	line chan string
	body chan string
}

func (e *holdEnv) send(req string) *pending {
	e.t.Helper()
	conn, err := net.Dial("tcp", e.addr)
	if err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(func() { conn.Close() })
	if _, err := conn.Write([]byte(req)); err != nil {
		e.t.Fatal(err)
	}
	p := &pending{conn: conn, line: make(chan string, 1), body: make(chan string, 1)}
	go func() {
		r := bufio.NewReader(conn)
		l, _ := r.ReadString('\n')
		p.line <- l
		var rest strings.Builder
		_ = conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
		for {
			b, err := r.ReadString('\n')
			rest.WriteString(b)
			if err != nil {
				break
			}
		}
		p.body <- rest.String()
	}()
	return p
}

func (p *pending) within(t *testing.T, d time.Duration) (string, bool) {
	t.Helper()
	select {
	case l := <-p.line:
		return l, true
	case <-time.After(d):
		return "", false
	}
}

func connectTo(addr string) string {
	return fmt.Sprintf("CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", addr, addr)
}

func (e *holdEnv) allow(hosts ...string) {
	e.t.Helper()
	if _, err := config.AddAllowedHosts(e.cfgPath, hosts); err != nil {
		e.t.Fatal(err)
	}
}

func (e *holdEnv) ignore(host string) {
	e.t.Helper()
	db, err := denieddb.Open(e.db)
	if err != nil {
		e.t.Fatal(err)
	}
	defer db.Close()
	if err := denieddb.MarkIgnored(db, "s1", []string{host}); err != nil {
		e.t.Fatal(err)
	}
}

func (e *holdEnv) activeHolds() int64 { return e.proxy.holds.active.Load() }

func TestHeldConnectIsReleasedByAnApproval(t *testing.T) {
	e := startHoldProxy(t, "allowlist", "10s", true)
	up := upstreamListener(t)
	host, _, _ := net.SplitHostPort(up)
	req := e.send(connectTo(up))
	if line, done := req.within(t, 700*time.Millisecond); done {
		t.Fatalf("the request was answered while it should be held: %q", line)
	}
	if e.activeHolds() != 1 {
		t.Fatalf("active holds = %d", e.activeHolds())
	}
	e.allow(host)
	line, done := req.within(t, 4*time.Second)
	if !done || !strings.Contains(line, "200") {
		t.Fatalf("the approved request did not go through: %q (answered=%v)", line, done)
	}
}

func TestHeldRequestIsDeniedAtOnceWhenTheUserIgnoresTheHost(t *testing.T) {
	e := startHoldProxy(t, "allowlist", "10s", true)
	req := e.send(connectTo("blocked.test:443"))
	if _, done := req.within(t, 600*time.Millisecond); done {
		t.Fatal("not held")
	}
	e.ignore("blocked.test")
	line, done := req.within(t, 3*time.Second)
	if !done || !strings.Contains(line, "407") {
		t.Fatalf("an ignored host was not denied promptly: %q", line)
	}
	if body := <-req.body; !strings.Contains(body, "needs the user's approval") || !strings.Contains(body, "Do not look for workarounds") {
		t.Fatalf("the denial does not tell the agent what to do: %q", body)
	}
}

func TestAlreadyIgnoredHostsAreNotHeld(t *testing.T) {
	e := startHoldProxy(t, "allowlist", "10s", true)
	first := e.send(connectTo("noisy.test:443"))
	first.within(t, 400*time.Millisecond)
	e.ignore("noisy.test")
	first.within(t, 2*time.Second)
	start := time.Now()
	second := e.send(connectTo("noisy.test:443"))
	line, done := second.within(t, 2*time.Second)
	if !done || !strings.Contains(line, "407") || time.Since(start) > time.Second {
		t.Fatalf("a host the user ignored was held again: %q after %s", line, time.Since(start))
	}
}

func TestHoldTimesOutIntoTheNormalDenial(t *testing.T) {
	e := startHoldProxy(t, "allowlist", "800ms", true)
	start := time.Now()
	req := e.send(connectTo("blocked.test:443"))
	line, done := req.within(t, 4*time.Second)
	elapsed := time.Since(start)
	if !done || !strings.Contains(line, "407") {
		t.Fatalf("expected 407 after the wait: %q", line)
	}
	if elapsed < 700*time.Millisecond {
		t.Fatalf("denied after only %s", elapsed)
	}
	if e.activeHolds() != 0 {
		t.Fatalf("hold slot leaked: %d", e.activeHolds())
	}
}

func TestPlaintextRequestsAreHeldToo(t *testing.T) {
	e := startHoldProxy(t, "https", "10s", true)
	req := e.send("GET http://blocked.test/ HTTP/1.1\r\nHost: blocked.test\r\n\r\n")
	if _, done := req.within(t, 600*time.Millisecond); done {
		t.Fatal("plaintext request not held")
	}
	e.ignore("blocked.test")
	if line, done := req.within(t, 3*time.Second); !done || !strings.Contains(line, "407") {
		t.Fatalf("got %q", line)
	}
}

func TestNoHoldWhenDisabledUnattendedOrNone(t *testing.T) {
	cases := []struct {
		name, mode, wait string
		attended         bool
	}{
		{"off", "allowlist", "off", true},
		{"zero", "allowlist", "0s", true},
		{"unattended", "allowlist", "10s", false},
		{"none mode", "none", "10s", true},
	}
	for _, c := range cases {
		e := startHoldProxy(t, c.mode, c.wait, c.attended)
		start := time.Now()
		line, done := e.send(connectTo("blocked.test:443")).within(t, 2*time.Second)
		if !done || !strings.Contains(line, "407") || time.Since(start) > time.Second {
			t.Errorf("%s: got %q after %s", c.name, line, time.Since(start))
		}
	}
}

func TestStaleAttendedMarkerDoesNotHold(t *testing.T) {
	e := startHoldProxy(t, "allowlist", "10s", true)
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(e.attended, old, old); err != nil {
		t.Fatal(err)
	}
	line, done := e.send(connectTo("blocked.test:443")).within(t, time.Second)
	if !done || !strings.Contains(line, "407") {
		t.Fatalf("a stale marker still held the request: %q", line)
	}
}

func TestTurningHoldingOffReleasesHeldRequests(t *testing.T) {
	e := startHoldProxy(t, "allowlist", "30s", true)
	req := e.send(connectTo("blocked.test:443"))
	if _, done := req.within(t, 500*time.Millisecond); done {
		t.Fatal("not held")
	}
	if err := config.SetApprovalWait(e.cfgPath, "off"); err != nil {
		t.Fatal(err)
	}
	line, done := req.within(t, 4*time.Second)
	if !done || !strings.Contains(line, "407") {
		t.Fatalf("turning holding off did not release the request: %q", line)
	}
}

func TestClientDisconnectFreesTheHoldSlot(t *testing.T) {
	e := startHoldProxy(t, "allowlist", "30s", true)
	req := e.send(connectTo("blocked.test:443"))
	time.Sleep(500 * time.Millisecond)
	if e.activeHolds() != 1 {
		t.Fatalf("active holds = %d", e.activeHolds())
	}
	req.conn.Close()
	deadline := time.Now().Add(3 * time.Second)
	for e.activeHolds() != 0 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if e.activeHolds() != 0 {
		t.Fatal("the slot was not freed after the client went away")
	}
}

func TestTooManyHoldsAreDeniedAtOnce(t *testing.T) {
	old := maxHolds
	maxHolds = 2
	defer func() { maxHolds = old }()
	e := startHoldProxy(t, "allowlist", "30s", true)
	e.send(connectTo("a.test:443"))
	e.send(connectTo("b.test:443"))
	time.Sleep(500 * time.Millisecond)
	if e.activeHolds() != 2 {
		t.Fatalf("active holds = %d", e.activeHolds())
	}
	line, done := e.send(connectTo("c.test:443")).within(t, time.Second)
	if !done || !strings.Contains(line, "407") {
		t.Fatalf("the request over the cap was not denied at once: %q", line)
	}
}

func TestHTTPSModeStillTunnelsPort443WithoutHolding(t *testing.T) {
	e := startHoldProxy(t, "https", "10s", true)
	start := time.Now()
	line, done := e.send(connectTo("127.0.0.1:443")).within(t, 3*time.Second)
	if !done || strings.Contains(line, "407") || time.Since(start) > time.Second || e.activeHolds() != 0 {
		t.Fatalf("an allowed :443 CONNECT in https mode was held or denied: %q after %s", line, time.Since(start))
	}
}

func startDNS(t *testing.T, e *holdEnv) string {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := pc.LocalAddr().String()
	pc.Close()
	go e.proxy.startDNS(addr)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		c, err := net.Dial("udp", addr)
		if err == nil {
			c.Close()
			break
		}
	}
	time.Sleep(200 * time.Millisecond)
	return addr
}

func query(addr, name string, timeout time.Duration) (*dns.Msg, error) {
	m := new(dns.Msg)
	m.SetQuestion(dns.Fqdn(name), dns.TypeA)
	c := &dns.Client{Timeout: timeout}
	resp, _, err := c.Exchange(m, addr)
	return resp, err
}

func TestHeldDNSQueryIsAnsweredAfterApprovalAndRefusedOtherwise(t *testing.T) {
	e := startHoldProxy(t, "allowlist", "30s", true)
	addr := startDNS(t, e)

	type result struct {
		resp *dns.Msg
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		r, err := query(addr, "localhost", 8*time.Second)
		ch <- result{r, err}
	}()
	select {
	case r := <-ch:
		t.Fatalf("the query was answered while it should be held: %v %v", r.resp, r.err)
	case <-time.After(700 * time.Millisecond):
	}
	e.allow("localhost")
	select {
	case r := <-ch:
		if r.err != nil || r.resp.Rcode == dns.RcodeRefused {
			t.Fatalf("approved query: %v %v", r.resp, r.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("approved query never answered")
	}

	if err := config.SetApprovalWait(e.cfgPath, "600ms"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(400 * time.Millisecond)
	start := time.Now()
	resp, err := query(addr, "never-approved.test", 5*time.Second)
	if err != nil || resp.Rcode != dns.RcodeRefused {
		t.Fatalf("unapproved query: %v %v", resp, err)
	}
	if time.Since(start) < 500*time.Millisecond {
		t.Fatalf("refused after only %s", time.Since(start))
	}
}

func TestDNSHoldIsCappedBelowResolverPatience(t *testing.T) {
	if dnsHoldLimit > 9*time.Second || dnsHoldLimit < 3*time.Second {
		t.Fatalf("dnsHoldLimit = %s", dnsHoldLimit)
	}
}

func TestHeldRequestsAreCountedInTheDenialDatabase(t *testing.T) {
	e := startHoldProxy(t, "allowlist", "30s", true)
	req := e.send(connectTo("blocked.test:443"))
	time.Sleep(500 * time.Millisecond)
	holding := func() int {
		db, err := denieddb.Open(e.db)
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		rows, err := denieddb.All(db, denieddb.Filter{Session: "s1"})
		if err != nil || len(rows) != 1 {
			t.Fatalf("%+v %v", rows, err)
		}
		return rows[0].Holding
	}
	if holding() != 1 {
		t.Fatalf("holding = %d while a request is held", holding())
	}
	e.ignore("blocked.test")
	req.within(t, 3*time.Second)
	time.Sleep(200 * time.Millisecond)
	if holding() != 0 {
		t.Fatalf("holding = %d after the request ended", holding())
	}
}

func TestProxyStartClearsStaleHoldingCounts(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "denied.db")
	db, err := denieddb.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := denieddb.Record(db, "/ws", "s1", "stale.test", "stale.test:443"); err != nil {
		t.Fatal(err)
	}
	if err := denieddb.AdjustHolding(db, "s1", "stale.test", 4); err != nil {
		t.Fatal(err)
	}
	db.Close()
	ca, _ := certs.NewCA()
	os.WriteFile(filepath.Join(dir, "ca.crt"), ca.CertPEM(), 0o600)
	os.WriteFile(filepath.Join(dir, "ca.key"), ca.KeyPEM(), 0o600)
	if _, err := New(Options{CACertPath: filepath.Join(dir, "ca.crt"), CAKeyPath: filepath.Join(dir, "ca.key"), DeniedDB: dbPath, Session: "s1"}); err != nil {
		t.Fatal(err)
	}
	db, _ = denieddb.Open(dbPath)
	defer db.Close()
	rows, _ := denieddb.Pending(db, denieddb.Filter{Session: "s1"})
	if len(rows) != 1 || rows[0].Holding != 0 {
		t.Fatalf("stale counts survived the proxy start: %+v", rows)
	}
}


func TestHoldingIsOffUnlessTheConfigAsksForIt(t *testing.T) {
	e := startHoldProxy(t, "allowlist", "", true)
	start := time.Now()
	line, done := e.send(connectTo("blocked.test:443")).within(t, 2*time.Second)
	if !done || !strings.Contains(line, "407") || time.Since(start) > time.Second {
		t.Fatalf("with no approval_wait in the config the request was held: %q after %s", line, time.Since(start))
	}
	if e.activeHolds() != 0 {
		t.Fatalf("active holds = %d", e.activeHolds())
	}
}
