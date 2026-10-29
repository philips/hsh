package forward

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"hsh/internal/sshd"
)

func TestParseSpec(t *testing.T) {
	good := map[string]Spec{
		"8000":      {8000, 8000},
		"9000:8000": {9000, 8000},
		"1:65535":   {1, 65535},
	}
	for in, want := range good {
		got, err := ParseSpec(in)
		if err != nil || got != want {
			t.Errorf("ParseSpec(%q) = %+v, %v; want %+v", in, got, err, want)
		}
	}
	for _, in := range []string{"", "0", "65536", "abc", "80:", ":80", "1:2:3", "-1", "8000:x"} {
		if _, err := ParseSpec(in); err == nil {
			t.Errorf("ParseSpec(%q) succeeded", in)
		}
	}
}

func TestParseSpecsDuplicateLocal(t *testing.T) {
	if _, err := ParseSpecs([]string{"8000", "8000:9000"}); err == nil {
		t.Fatal("duplicate local ports accepted")
	}
	specs, err := ParseSpecs([]string{"8000", "9000:8001"})
	if err != nil || len(specs) != 2 {
		t.Fatalf("%+v %v", specs, err)
	}
}

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

func upperBackend(t *testing.T) int {
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
			go func() {
				defer c.Close()
				data, _ := io.ReadAll(c)
				c.Write(bytes.ToUpper(data))
			}()
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port
}

type session struct {
	client *ssh.Client
	server net.Conn
	mu     sync.Mutex
	ports  []sshd.Listener
	dials  map[int]int
	fail   error
}

func (s *session) setPorts(ports ...int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ports = nil
	for _, p := range ports {
		s.ports = append(s.ports, sshd.Listener{Port: p, Addr: "127.0.0.1"})
	}
}

func newSession(t *testing.T) *session {
	t.Helper()
	s := &session{dials: map[int]int{}}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	accepted := make(chan net.Conn, 1)
	go func() {
		c, _ := ln.Accept()
		accepted <- c
	}()
	cc, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	s.server = <-accepted
	go func() {
		_ = sshd.Serve(s.server, sshd.Config{
			Dial: func(port int) (net.Conn, error) {
				s.mu.Lock()
				target, ok := s.dials[port]
				fail := s.fail
				s.mu.Unlock()
				if fail != nil {
					return nil, fail
				}
				if !ok {
					target = port
				}
				return net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", target), 2*time.Second)
			},
			List: func() ([]sshd.Listener, error) {
				s.mu.Lock()
				defer s.mu.Unlock()
				return append([]sshd.Listener(nil), s.ports...), nil
			},
		})
	}()
	s.client, err = NewClient(cc)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.client.Close() })
	return s
}

func roundTrip(t *testing.T, port int, msg string) string {
	t.Helper()
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.Write([]byte(msg))
	conn.(*net.TCPConn).CloseWrite()
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	got, err := io.ReadAll(conn)
	if err != nil {
		t.Fatal(err)
	}
	return string(got)
}

type logSink struct {
	mu    sync.Mutex
	lines []string
}

func (l *logSink) logf(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, fmt.Sprintf(format, args...))
}

func (l *logSink) count(substr string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for _, line := range l.lines {
		if strings.Contains(line, substr) {
			n++
		}
	}
	return n
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestRunExplicitSpecs(t *testing.T) {
	backend := upperBackend(t)
	s := newSession(t)
	local := freePort(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	ready := make(chan Spec, 1)
	go func() {
		done <- Run(ctx, s.client, Options{
			Specs:     []Spec{{Local: local, Remote: backend}},
			OnForward: func(bind string, spec Spec, auto bool) { ready <- spec },
		})
	}()
	select {
	case <-ready:
	case err := <-done:
		t.Fatal(err)
	case <-time.After(5 * time.Second):
		t.Fatal("never forwarded")
	}
	if got := roundTrip(t, local, "hello"); got != "HELLO" {
		t.Fatalf("got %q", got)
	}
	big := strings.Repeat("x", 3<<20)
	if got := roundTrip(t, local, big); got != strings.ToUpper(big) {
		t.Fatalf("large transfer: got %d bytes", len(got))
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run after cancel: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
	if _, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", local), time.Second); err == nil {
		t.Fatal("the local listener outlived Run")
	}
}

func TestRunBusyLocalPortFailsUp(t *testing.T) {
	s := newSession(t)
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()
	port := busy.Addr().(*net.TCPAddr).Port
	err = Run(context.Background(), s.client, Options{Specs: []Spec{{Local: freePort(t), Remote: 5}, {Local: port, Remote: 8000}}})
	if err == nil || !strings.Contains(err.Error(), "LOCAL:8000") {
		t.Fatalf("err = %v, want a hint to use LOCAL:8000", err)
	}
}

func TestRunReportsClosedSession(t *testing.T) {
	s := newSession(t)
	done := make(chan error, 1)
	go func() {
		done <- Run(context.Background(), s.client, Options{Specs: []Spec{{Local: freePort(t), Remote: 8000}}})
	}()
	time.Sleep(200 * time.Millisecond)
	s.server.Close()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "session") {
			t.Fatalf("err = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not notice the session closing")
	}
}

func TestRunAutoFollowsListeners(t *testing.T) {
	backend := upperBackend(t)
	s := newSession(t)
	remote := freePort(t)
	s.mu.Lock()
	s.dials[remote] = backend
	s.mu.Unlock()

	var mu sync.Mutex
	var forwarded, released []int
	logs := &logSink{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, s.client, Options{
			Auto:      true,
			PollEvery: 30 * time.Millisecond,
			Logf:      logs.logf,
			OnForward: func(bind string, spec Spec, auto bool) {
				mu.Lock()
				forwarded = append(forwarded, spec.Remote)
				mu.Unlock()
			},
			OnRelease: func(bind string, spec Spec) {
				mu.Lock()
				released = append(released, spec.Remote)
				mu.Unlock()
			},
		})
	}()
	time.Sleep(150 * time.Millisecond)
	mu.Lock()
	if len(forwarded) != 0 {
		t.Fatalf("forwarded %v before anything listened", forwarded)
	}
	mu.Unlock()

	s.setPorts(remote)
	waitFor(t, "the new listener to be forwarded", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(forwarded) == 1
	})
	if got := roundTrip(t, remote, "auto"); got != "AUTO" {
		t.Fatalf("got %q", got)
	}

	s.setPorts()
	waitFor(t, "the vanished listener to be released", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(released) == 1
	})
	if _, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", remote), time.Second); err == nil {
		t.Fatal("the released listener still accepts")
	}

	s.setPorts(remote)
	waitFor(t, "the listener to come back", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(forwarded) == 2
	})
	if got := roundTrip(t, remote, "again"); got != "AGAIN" {
		t.Fatalf("got %q", got)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if logs.count("") != 0 {
		t.Fatalf("unexpected log lines: %v", logs.lines)
	}
}

func TestRunAutoWarnsOnceForBusyLocalPort(t *testing.T) {
	s := newSession(t)
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()
	port := busy.Addr().(*net.TCPAddr).Port
	s.setPorts(port)
	logs := &logSink{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go Run(ctx, s.client, Options{Auto: true, PollEvery: 20 * time.Millisecond, Logf: logs.logf})
	waitFor(t, "a busy-port warning", func() bool { return logs.count("LOCAL:") == 1 })
	time.Sleep(200 * time.Millisecond)
	if n := logs.count("LOCAL:"); n != 1 {
		t.Fatalf("warned %d times", n)
	}
}

func TestDialFailureWarnsOnceAndKeepsRunning(t *testing.T) {
	s := newSession(t)
	s.mu.Lock()
	s.fail = fmt.Errorf("nothing is listening on port 8000 in this session")
	s.mu.Unlock()
	local := freePort(t)
	logs := &logSink{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ready := make(chan struct{}, 1)
	go Run(ctx, s.client, Options{
		Specs:     []Spec{{Local: local, Remote: 8000}},
		Logf:      logs.logf,
		OnForward: func(string, Spec, bool) { ready <- struct{}{} },
	})
	<-ready
	for i := 0; i < 3; i++ {
		conn, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", local))
		if err != nil {
			t.Fatal(err)
		}
		conn.SetReadDeadline(time.Now().Add(3 * time.Second))
		if _, err := io.ReadAll(conn); err != nil {
			t.Fatal(err)
		}
		conn.Close()
	}
	if n := logs.count("nothing is listening on port 8000"); n != 1 {
		t.Fatalf("warned %d times, lines %v", n, logs.lines)
	}
	backend := upperBackend(t)
	s.mu.Lock()
	s.fail = nil
	s.dials[8000] = backend
	s.mu.Unlock()
	if got := roundTrip(t, local, "ok"); got != "OK" {
		t.Fatalf("after recovery got %q", got)
	}
}

func TestListPorts(t *testing.T) {
	s := newSession(t)
	s.setPorts(3000, 8000)
	got, err := ListPorts(s.client)
	if err != nil || len(got) != 2 || got[0].Port != 3000 || got[1].Port != 8000 {
		t.Fatalf("%+v %v", got, err)
	}
}
