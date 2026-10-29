package forward

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"

	"hsh/internal/sshd"
)

type Spec struct {
	Local  int
	Remote int
}

func ParseSpec(s string) (Spec, error) {
	localStr, remoteStr, hasLocal := strings.Cut(s, ":")
	if !hasLocal {
		remoteStr = localStr
	}
	remote, err := parsePort(remoteStr)
	if err != nil {
		return Spec{}, fmt.Errorf("invalid forward %q: %w", s, err)
	}
	local := remote
	if hasLocal {
		if local, err = parsePort(localStr); err != nil {
			return Spec{}, fmt.Errorf("invalid forward %q: %w", s, err)
		}
	}
	return Spec{Local: local, Remote: remote}, nil
}

func ParseSpecs(args []string) ([]Spec, error) {
	var specs []Spec
	seen := map[int]bool{}
	for _, a := range args {
		spec, err := ParseSpec(a)
		if err != nil {
			return nil, err
		}
		if seen[spec.Local] {
			return nil, fmt.Errorf("local port %d is given twice", spec.Local)
		}
		seen[spec.Local] = true
		specs = append(specs, spec)
	}
	return specs, nil
}

func parsePort(s string) (int, error) {
	port, err := strconv.Atoi(s)
	if err != nil || port < 1 || port > 65535 {
		return 0, fmt.Errorf("%q is not a port between 1 and 65535", s)
	}
	return port, nil
}

func CheckLocal(bind string, specs []Spec) error {
	if bind == "" {
		bind = "127.0.0.1"
	}
	for _, spec := range specs {
		addr := net.JoinHostPort(bind, strconv.Itoa(spec.Local))
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			return fmt.Errorf("cannot listen on %s: %w (pick another local port with LOCAL:%d)", addr, err, spec.Remote)
		}
		ln.Close()
	}
	return nil
}

type Options struct {
	Bind      string
	Specs     []Spec
	Auto      bool
	PollEvery time.Duration
	OnForward func(bind string, spec Spec, auto bool)
	OnRelease func(bind string, spec Spec)
	Logf      func(format string, args ...any)
}

type forwarder struct {
	client *ssh.Client
	opt    Options
	mu     sync.Mutex
	active map[int]*listening
	busy   map[int]bool
	dialed map[int]time.Time
	wg     sync.WaitGroup
}

type listening struct {
	spec Spec
	ln   net.Listener
	auto bool
}

func Run(ctx context.Context, client *ssh.Client, opt Options) error {
	if opt.Bind == "" {
		opt.Bind = "127.0.0.1"
	}
	if opt.PollEvery <= 0 {
		opt.PollEvery = 2 * time.Second
	}
	if opt.Logf == nil {
		opt.Logf = func(string, ...any) {}
	}
	f := &forwarder{client: client, opt: opt, active: map[int]*listening{}, busy: map[int]bool{}, dialed: map[int]time.Time{}}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	for _, spec := range opt.Specs {
		if err := f.listen(spec, false); err != nil {
			f.closeAll()
			return err
		}
	}
	connClosed := make(chan error, 1)
	go func() { connClosed <- client.Wait() }()
	var pollC <-chan time.Time
	if opt.Auto {
		f.poll()
		ticker := time.NewTicker(opt.PollEvery)
		defer ticker.Stop()
		pollC = ticker.C
	}
	for {
		select {
		case <-ctx.Done():
			f.closeAll()
			f.wg.Wait()
			return nil
		case <-connClosed:
			f.closeAll()
			f.wg.Wait()
			if ctx.Err() != nil {
				return nil
			}
			return errors.New("the session's connection closed (did the session stop?)")
		case <-pollC:
			f.poll()
		}
	}
}

func (f *forwarder) listen(spec Spec, auto bool) error {
	addr := net.JoinHostPort(f.opt.Bind, strconv.Itoa(spec.Local))
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("cannot listen on %s: %w (pick another local port with LOCAL:%d)", addr, err, spec.Remote)
	}
	f.mu.Lock()
	f.active[spec.Local] = &listening{spec: spec, ln: ln, auto: auto}
	f.mu.Unlock()
	if f.opt.OnForward != nil {
		f.opt.OnForward(f.opt.Bind, spec, auto)
	}
	f.wg.Add(1)
	go func() {
		defer f.wg.Done()
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			f.wg.Add(1)
			go func() {
				defer f.wg.Done()
				f.handle(conn, spec.Remote)
			}()
		}
	}()
	return nil
}

func (f *forwarder) closeAll() {
	f.mu.Lock()
	defer f.mu.Unlock()
	for local, l := range f.active {
		l.ln.Close()
		delete(f.active, local)
	}
}

func (f *forwarder) poll() {
	listeners, err := ListPorts(f.client)
	if err != nil {
		return
	}
	want := map[int]bool{}
	for _, l := range listeners {
		want[l.Port] = true
	}
	f.mu.Lock()
	var gone []*listening
	for local, l := range f.active {
		if l.auto && !want[l.spec.Remote] {
			gone = append(gone, l)
			delete(f.active, local)
		}
	}
	explicitRemote := map[int]bool{}
	for _, l := range f.active {
		explicitRemote[l.spec.Remote] = true
	}
	for remote := range f.busy {
		if !want[remote] {
			delete(f.busy, remote)
		}
	}
	f.mu.Unlock()
	for _, l := range gone {
		l.ln.Close()
		if f.opt.OnRelease != nil {
			f.opt.OnRelease(f.opt.Bind, l.spec)
		}
	}
	ports := make([]int, 0, len(want))
	for p := range want {
		ports = append(ports, p)
	}
	sort.Ints(ports)
	for _, p := range ports {
		f.mu.Lock()
		_, taken := f.active[p]
		warned := f.busy[p]
		f.mu.Unlock()
		if taken || explicitRemote[p] || warned {
			continue
		}
		if err := f.listen(Spec{Local: p, Remote: p}, true); err != nil {
			f.mu.Lock()
			f.busy[p] = true
			f.mu.Unlock()
			f.opt.Logf("hsh: the session listens on port %d but %v\n", p, err)
		}
	}
}

type closeWriter interface {
	CloseWrite() error
}

func (f *forwarder) handle(local net.Conn, remote int) {
	defer local.Close()
	ch, err := f.client.Dial("tcp", net.JoinHostPort("localhost", strconv.Itoa(remote)))
	if err != nil {
		f.warnDial(remote, err)
		return
	}
	defer ch.Close()
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _ = io.Copy(local, ch)
		if cw, ok := local.(closeWriter); ok {
			_ = cw.CloseWrite()
		}
	}()
	go func() {
		defer wg.Done()
		_, _ = io.Copy(ch, local)
		if cw, ok := ch.(closeWriter); ok {
			_ = cw.CloseWrite()
		}
	}()
	wg.Wait()
}

func (f *forwarder) warnDial(remote int, err error) {
	var openErr *ssh.OpenChannelError
	msg := err.Error()
	if errors.As(err, &openErr) {
		msg = openErr.Message
	}
	f.mu.Lock()
	last, ok := f.dialed[remote]
	if ok && time.Since(last) < 5*time.Second {
		f.mu.Unlock()
		return
	}
	f.dialed[remote] = time.Now()
	f.mu.Unlock()
	f.opt.Logf("hsh: port %d: %s\n", remote, msg)
}

func ListPorts(client *ssh.Client) ([]sshd.Listener, error) {
	ok, payload, err := client.SendRequest(sshd.ListRequest, true, nil)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("the session could not list its listening ports: %s", strings.TrimSpace(string(payload)))
	}
	return sshd.ParseList(payload), nil
}

type pipeConn struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.ReadCloser
	once   sync.Once
}

func (p *pipeConn) Read(b []byte) (int, error)  { return p.stdout.Read(b) }
func (p *pipeConn) Write(b []byte) (int, error) { return p.stdin.Write(b) }
func (p *pipeConn) Close() error {
	p.once.Do(func() {
		_ = p.stdin.Close()
		done := make(chan struct{})
		go func() {
			_ = p.cmd.Wait()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			_ = p.cmd.Process.Kill()
			<-done
		}
	})
	return nil
}

type pipeAddr struct{}

func (pipeAddr) Network() string { return "exec" }
func (pipeAddr) String() string  { return "exec" }

func (p *pipeConn) LocalAddr() net.Addr                { return pipeAddr{} }
func (p *pipeConn) RemoteAddr() net.Addr               { return pipeAddr{} }
func (p *pipeConn) SetDeadline(t time.Time) error      { return nil }
func (p *pipeConn) SetReadDeadline(t time.Time) error  { return nil }
func (p *pipeConn) SetWriteDeadline(t time.Time) error { return nil }

func DialCommand(cmd *exec.Cmd) (*ssh.Client, error) {
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return NewClient(&pipeConn{cmd: cmd, stdin: stdin, stdout: stdout})
}

func NewClient(conn net.Conn) (*ssh.Client, error) {
	cc, chans, reqs, err := ssh.NewClientConn(conn, "hsh", &ssh.ClientConfig{
		User:            "hsh",
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         30 * time.Second,
	})
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("ssh handshake with the session: %w", err)
	}
	return ssh.NewClient(cc, chans, reqs), nil
}
