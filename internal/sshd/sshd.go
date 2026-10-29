package sshd

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

const ListRequest = "hsh-list@ifup.org"

type Listener struct {
	Port int
	Addr string
}

type Config struct {
	Dial func(port int) (net.Conn, error)
	List func() ([]Listener, error)
}

type directTCPIP struct {
	Host     string
	Port     uint32
	OrigHost string
	OrigPort uint32
}

func Serve(conn net.Conn, cfg Config) error {
	_, signer, err := newHostKey()
	if err != nil {
		return err
	}
	sc := &ssh.ServerConfig{NoClientAuth: true}
	sc.AddHostKey(signer)
	server, chans, reqs, err := ssh.NewServerConn(conn, sc)
	if err != nil {
		return fmt.Errorf("ssh handshake: %w", err)
	}
	defer server.Close()
	go handleGlobalRequests(reqs, cfg)
	for newCh := range chans {
		go handleChannel(newCh, cfg)
	}
	return nil
}

func newHostKey() (ed25519.PublicKey, ssh.Signer, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		return nil, nil, err
	}
	return pub, signer, nil
}

func handleGlobalRequests(reqs <-chan *ssh.Request, cfg Config) {
	for req := range reqs {
		if req.Type != ListRequest || cfg.List == nil {
			if req.WantReply {
				_ = req.Reply(false, nil)
			}
			continue
		}
		listeners, err := cfg.List()
		if err != nil {
			if req.WantReply {
				_ = req.Reply(false, []byte(err.Error()))
			}
			continue
		}
		if req.WantReply {
			_ = req.Reply(true, FormatList(listeners))
		}
	}
}

func FormatList(listeners []Listener) []byte {
	var b strings.Builder
	for _, l := range listeners {
		fmt.Fprintf(&b, "%d %s\n", l.Port, l.Addr)
	}
	return []byte(b.String())
}

func ParseList(payload []byte) []Listener {
	var out []Listener
	for _, line := range strings.Split(string(payload), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		port, err := strconv.Atoi(fields[0])
		if err != nil || port < 1 || port > 65535 {
			continue
		}
		out = append(out, Listener{Port: port, Addr: fields[1]})
	}
	return out
}

func SandboxLoopback(host string) bool {
	switch host {
	case "localhost", "127.0.0.1", "::1", "[::1]":
		return true
	}
	return false
}

func handleChannel(newCh ssh.NewChannel, cfg Config) {
	if newCh.ChannelType() != "direct-tcpip" {
		_ = newCh.Reject(ssh.UnknownChannelType, "only port forwards to the session's loopback are supported")
		return
	}
	var d directTCPIP
	if err := ssh.Unmarshal(newCh.ExtraData(), &d); err != nil {
		_ = newCh.Reject(ssh.ConnectionFailed, "malformed direct-tcpip request")
		return
	}
	if !SandboxLoopback(d.Host) {
		_ = newCh.Reject(ssh.Prohibited, fmt.Sprintf("only the session's loopback can be reached (localhost, 127.0.0.1, ::1), not %q", d.Host))
		return
	}
	if d.Port < 1 || d.Port > 65535 {
		_ = newCh.Reject(ssh.ConnectionFailed, "invalid port")
		return
	}
	target, err := cfg.Dial(int(d.Port))
	if err != nil {
		_ = newCh.Reject(ssh.ConnectionFailed, err.Error())
		return
	}
	defer target.Close()
	ch, reqs, err := newCh.Accept()
	if err != nil {
		return
	}
	defer ch.Close()
	go ssh.DiscardRequests(reqs)
	splice(ch, target)
}

type closeWriter interface {
	CloseWrite() error
}

func splice(ch ssh.Channel, target net.Conn) {
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _ = io.Copy(ch, target)
		_ = ch.CloseWrite()
	}()
	go func() {
		defer wg.Done()
		_, _ = io.Copy(target, ch)
		if cw, ok := target.(closeWriter); ok {
			_ = cw.CloseWrite()
		}
	}()
	wg.Wait()
}

type stdioConn struct {
	r io.Reader
	w io.Writer
	c []io.Closer
}

func Stdio() net.Conn {
	return &stdioConn{r: os.Stdin, w: os.Stdout, c: []io.Closer{os.Stdin, os.Stdout}}
}

func (s *stdioConn) Read(p []byte) (int, error)  { return s.r.Read(p) }
func (s *stdioConn) Write(p []byte) (int, error) { return s.w.Write(p) }
func (s *stdioConn) Close() error {
	var first error
	for _, c := range s.c {
		if err := c.Close(); err != nil && first == nil && !errors.Is(err, os.ErrClosed) {
			first = err
		}
	}
	return first
}

type stdioAddr struct{}

func (stdioAddr) Network() string { return "stdio" }
func (stdioAddr) String() string  { return "stdio" }

func (s *stdioConn) LocalAddr() net.Addr                { return stdioAddr{} }
func (s *stdioConn) RemoteAddr() net.Addr               { return stdioAddr{} }
func (s *stdioConn) SetDeadline(t time.Time) error      { return nil }
func (s *stdioConn) SetReadDeadline(t time.Time) error  { return nil }
func (s *stdioConn) SetWriteDeadline(t time.Time) error { return nil }
