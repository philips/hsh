package sshd

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

type harness struct {
	client *ssh.Client
	dials  *atomic.Int32
}

func startServer(t *testing.T, cfg Config) *harness {
	t.Helper()
	dials := &atomic.Int32{}
	realDial := cfg.Dial
	cfg.Dial = func(port int) (net.Conn, error) {
		dials.Add(1)
		return realDial(port)
	}
	cs, ss := tcpPair(t)
	go func() { _ = Serve(ss, cfg) }()
	cc, chans, reqs, err := ssh.NewClientConn(cs, "hsh", &ssh.ClientConfig{
		User:            "hsh",
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	client := ssh.NewClient(cc, chans, reqs)
	t.Cleanup(func() { client.Close() })
	return &harness{client: client, dials: dials}
}

func tcpPair(t *testing.T) (net.Conn, net.Conn) {
	t.Helper()
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
	c, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	return c, <-accepted
}

func loopbackDial(port int) (net.Conn, error) {
	return net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 2*time.Second)
}

func upperServer(t *testing.T) int {
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

func TestDirectTCPIPRoundTripLargeHalfClose(t *testing.T) {
	port := upperServer(t)
	h := startServer(t, Config{Dial: loopbackDial})
	payload := bytes.Repeat([]byte("hello ssh "), 300000)
	for _, host := range []string{"localhost", "127.0.0.1", "::1"} {
		conn, err := h.client.Dial("tcp", net.JoinHostPort(host, fmt.Sprint(port)))
		if err != nil {
			t.Fatalf("%s: %v", host, err)
		}
		go func() {
			conn.Write(payload)
			conn.(ssh.Channel).CloseWrite()
		}()
		got, err := io.ReadAll(conn)
		conn.Close()
		if err != nil {
			t.Fatalf("%s: %v", host, err)
		}
		if !bytes.Equal(got, bytes.ToUpper(payload)) {
			t.Fatalf("%s: got %d bytes, want %d", host, len(got), len(payload))
		}
	}
}

func TestConcurrentChannels(t *testing.T) {
	port := upperServer(t)
	h := startServer(t, Config{Dial: loopbackDial})
	errs := make(chan error, 20)
	for i := 0; i < 20; i++ {
		go func(i int) {
			conn, err := h.client.Dial("tcp", fmt.Sprintf("localhost:%d", port))
			if err != nil {
				errs <- err
				return
			}
			defer conn.Close()
			msg := fmt.Sprintf("conn-%d", i)
			conn.Write([]byte(msg))
			conn.(ssh.Channel).CloseWrite()
			got, err := io.ReadAll(conn)
			if err == nil && string(got) != strings.ToUpper(msg) {
				err = fmt.Errorf("got %q", got)
			}
			errs <- err
		}(i)
	}
	for i := 0; i < 20; i++ {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
}

func TestRejectsNonSandboxDestinations(t *testing.T) {
	h := startServer(t, Config{Dial: loopbackDial})
	for _, dest := range []string{
		"example.com:443", "10.77.0.1:8080", "192.168.1.1:80", "0.0.0.0:80",
		"169.254.169.254:80", "127.0.0.2:80", "localhost.example.com:80", "[::ffff:127.0.0.1]:80",
	} {
		_, err := h.client.Dial("tcp", dest)
		var openErr *ssh.OpenChannelError
		if !errors.As(err, &openErr) || openErr.Reason != ssh.Prohibited {
			t.Errorf("%s: err = %v, want a Prohibited rejection", dest, err)
		}
	}
	if n := h.dials.Load(); n != 0 {
		t.Fatalf("%d dials happened for rejected destinations", n)
	}
}

func TestRejectsOtherChannelsAndRequests(t *testing.T) {
	h := startServer(t, Config{Dial: loopbackDial})
	for _, typ := range []string{"session", "x11", "forwarded-tcpip", "direct-streamlocal@openssh.com", "auth-agent@openssh.com"} {
		ch, reqs, err := h.client.OpenChannel(typ, nil)
		if err == nil {
			ch.Close()
			go ssh.DiscardRequests(reqs)
			t.Errorf("channel type %s was accepted", typ)
		}
	}
	for _, typ := range []string{"tcpip-forward", "cancel-tcpip-forward", "streamlocal-forward@openssh.com", "no-more-sessions@openssh.com"} {
		ok, _, err := h.client.SendRequest(typ, true, ssh.Marshal(struct {
			Addr string
			Port uint32
		}{"127.0.0.1", 9000}))
		if err != nil {
			t.Fatal(err)
		}
		if ok {
			t.Errorf("global request %s was accepted", typ)
		}
	}
	if _, err := h.client.Listen("tcp", "127.0.0.1:9000"); err == nil {
		t.Error("remote listen (-R) was accepted")
	}
	if n := h.dials.Load(); n != 0 {
		t.Fatalf("%d dials", n)
	}
}

func TestClosedPortFailsChannelAndConnectionSurvives(t *testing.T) {
	port := upperServer(t)
	closed, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closedPort := closed.Addr().(*net.TCPAddr).Port
	closed.Close()
	h := startServer(t, Config{Dial: loopbackDial})
	_, err = h.client.Dial("tcp", fmt.Sprintf("localhost:%d", closedPort))
	var openErr *ssh.OpenChannelError
	if !errors.As(err, &openErr) || openErr.Reason != ssh.ConnectionFailed {
		t.Fatalf("err = %v, want ConnectionFailed", err)
	}
	conn, err := h.client.Dial("tcp", fmt.Sprintf("localhost:%d", port))
	if err != nil {
		t.Fatalf("later open failed: %v", err)
	}
	conn.Close()
}

func TestListRequest(t *testing.T) {
	h := startServer(t, Config{
		Dial: loopbackDial,
		List: func() ([]Listener, error) { return []Listener{{8000, "127.0.0.1"}, {3000, "::"}}, nil },
	})
	ok, payload, err := h.client.SendRequest(ListRequest, true, nil)
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if string(payload) != "8000 127.0.0.1\n3000 ::\n" {
		t.Fatalf("payload = %q", payload)
	}
}

func TestListRequestWithoutHandler(t *testing.T) {
	h := startServer(t, Config{Dial: loopbackDial})
	ok, _, err := h.client.SendRequest(ListRequest, true, nil)
	if err != nil || ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
}

func TestSandboxLoopback(t *testing.T) {
	for host, want := range map[string]bool{
		"localhost": true, "127.0.0.1": true, "::1": true, "[::1]": true,
		"": false, "LOCALHOST": false, "127.0.0.2": false, "localhost.": false, "10.77.0.2": false,
	} {
		if got := SandboxLoopback(host); got != want {
			t.Errorf("SandboxLoopback(%q) = %v, want %v", host, got, want)
		}
	}
}

func TestFormatParseListRoundTrip(t *testing.T) {
	in := []Listener{{8000, "127.0.0.1"}, {3000, "::"}}
	got := ParseList(FormatList(in))
	if len(got) != 2 || got[0] != in[0] || got[1] != in[1] {
		t.Fatalf("%+v", got)
	}
	if n := len(ParseList([]byte("junk\n0 x\n99999 y\n8 \n"))); n != 0 {
		t.Fatalf("parsed %d bad lines", n)
	}
}

type spawnRecord struct {
	mu       sync.Mutex
	commands []string
	ptys     []bool
	terms    []string
}

func (r *spawnRecord) record(req SpawnRequest) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.commands = append(r.commands, req.Command)
	r.ptys = append(r.ptys, req.Pty)
	r.terms = append(r.terms, req.Term)
}

func testSpawn(rec *spawnRecord) func(SpawnRequest) (SpawnResult, error) {
	return func(req SpawnRequest) (SpawnResult, error) {
		if rec != nil {
			rec.record(req)
		}
		cmd := exec.Command("/bin/sh", "-c", req.Command)
		if req.Command == "" {
			cmd = exec.Command("/bin/sh")
		}
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		if req.Pty {
			cmd.Stdin = req.Slave
			cmd.Stdout = req.Slave
			cmd.Stderr = req.Slave
		} else {
			cmd.Stdin = req.Stdin
			cmd.Stdout = req.Stdout
			cmd.Stderr = req.Stderr
		}
		if err := cmd.Start(); err != nil {
			return SpawnResult{}, err
		}
		pid := cmd.Process.Pid
		return SpawnResult{
			Signal: func(sig syscall.Signal) error { return syscall.Kill(-pid, sig) },
			Wait:   func() int { return ExitCode(cmd.Wait()) },
		}, nil
	}
}

func TestSessionExecOutputAndExitStatus(t *testing.T) {
	rec := &spawnRecord{}
	h := startServer(t, Config{Dial: loopbackDial, Spawn: testSpawn(rec)})
	ses, err := h.client.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	defer ses.Close()
	var out bytes.Buffer
	ses.Stdout = &out
	var errOut bytes.Buffer
	ses.Stderr = &errOut
	err = ses.Run("printf hi-stdout; printf hi-stderr >&2; exit 7")
	var exitErr *ssh.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitStatus() != 7 {
		t.Fatalf("err = %v, want exit status 7", err)
	}
	if out.String() != "hi-stdout" {
		t.Fatalf("stdout = %q", out.String())
	}
	if errOut.String() != "hi-stderr" {
		t.Fatalf("stderr = %q", errOut.String())
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.commands) != 1 || rec.commands[0] != "printf hi-stdout; printf hi-stderr >&2; exit 7" {
		t.Fatalf("commands = %v", rec.commands)
	}
}

func TestSessionPtyRunsOnTerminal(t *testing.T) {
	if master, slave, err := openPty(); err != nil {
		t.Skipf("pty allocation is unavailable here: %v", err)
	} else {
		master.Close()
		slave.Close()
	}
	rec := &spawnRecord{}
	h := startServer(t, Config{Dial: loopbackDial, Spawn: testSpawn(rec)})
	ses, err := h.client.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	defer ses.Close()
	if err := ses.RequestPty("xterm-256color", 24, 80, ssh.TerminalModes{}); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	ses.Stdout = &out
	if err := ses.Run("stty size"); err != nil {
		t.Fatalf("run: %v (%q)", err, out.String())
	}
	if !strings.HasPrefix(out.String(), "24 80") {
		t.Fatalf("pty output = %q, want the requested winsize", out.String())
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.ptys) != 1 || !rec.ptys[0] {
		t.Fatalf("ptys = %v", rec.ptys)
	}
	if len(rec.terms) != 1 || rec.terms[0] != "xterm-256color" {
		t.Fatalf("terms = %v", rec.terms)
	}
}

func TestSessionSignal(t *testing.T) {
	h := startServer(t, Config{Dial: loopbackDial, Spawn: testSpawn(nil)})
	ses, err := h.client.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	defer ses.Close()
	if err := ses.Start("sleep 30"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	if err := ses.Signal(ssh.SIGTERM); err != nil {
		t.Fatal(err)
	}
	err = ses.Wait()
	var exitErr *ssh.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitStatus() != 128+15 {
		t.Fatalf("err = %v, want exit status 143", err)
	}
}

func TestSessionRejectedWithoutSpawn(t *testing.T) {
	h := startServer(t, Config{Dial: loopbackDial})
	ses, err := h.client.NewSession()
	if err == nil {
		ses.Close()
		t.Fatal("session channel was accepted without a Spawn handler")
	}
}

func TestSessionPipesGiveStdinEOF(t *testing.T) {
	h := startServer(t, Config{Dial: loopbackDial, Spawn: testSpawn(nil)})
	ses, err := h.client.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	defer ses.Close()
	ses.Stdin = strings.NewReader("echo piped-input")
	var out bytes.Buffer
	ses.Stdout = &out
	if err := ses.Run("read line; echo got:$line"); err != nil {
		t.Fatalf("run: %v", err)
	}
	if out.String() != "got:echo piped-input\n" {
		t.Fatalf("output = %q", out.String())
	}
}
