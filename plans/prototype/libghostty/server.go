package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"code.selman.me/hauntty/libghostty"
	"github.com/creack/pty"
)

const renderInterval = 25 * time.Millisecond

type header struct {
	Op      string  `json:"op"`
	Cols    uint16  `json:"cols,omitempty"`
	Rows    uint16  `json:"rows,omitempty"`
	Text    string  `json:"text,omitempty"`
	Prompt  string  `json:"prompt,omitempty"`
	Timeout float64 `json:"timeout,omitempty"`
}

type overlay struct {
	prompt string
	done   func(approved bool)
}

type session struct {
	mu      sync.Mutex
	name    string
	term    *libghostty.Terminal
	ptmx    *os.File
	cmd     *exec.Cmd
	filter  oscFilter
	client  net.Conn
	cols    uint16
	rows    uint16
	dirty   bool
	active  *overlay
	queue   []*overlay
	review  reviewState
	logPath string
}

type serveOptions struct {
	sock    string
	logPath string
	fake    bool
	name    string
	args    []string
}

func newSession(o serveOptions) (*session, error) {
	s := &session{cols: 80, rows: 23, logPath: o.logPath, name: o.name}
	var err error
	s.term, err = libghostty.NewTerminal(libghostty.WithSize(s.cols, s.rows), libghostty.WithMaxScrollbackLines(10000))
	if err != nil {
		return nil, err
	}
	s.cmd = exec.Command(o.args[0], o.args[1:]...)
	s.cmd.Env = append(os.Environ(), "TERM=xterm-256color")
	s.ptmx, err = pty.StartWithSize(s.cmd, &pty.Winsize{Cols: s.cols, Rows: s.rows})
	return s, err
}

func (s *session) serve(o serveOptions, ready func()) error {
	os.Remove(o.sock)
	l, err := net.Listen("unix", o.sock)
	if err != nil {
		return err
	}
	os.Chmod(o.sock, 0o600)
	defer os.Remove(o.sock)

	go s.pumpOutput()
	go s.renderLoop()
	if o.fake {
		go s.fakeDenials()
	}
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go s.handle(c)
		}
	}()
	if ready != nil {
		ready()
	}
	s.cmd.Wait()
	s.mu.Lock()
	if s.client != nil {
		writeFrame(s.client, frameEnd, nil)
		s.client.Close()
	}
	s.mu.Unlock()
	return nil
}

func (s *session) shutdown() {
	if s.cmd.Process != nil {
		syscall.Kill(-s.cmd.Process.Pid, syscall.SIGHUP)
		s.cmd.Process.Kill()
	}
}

func cmdServe(args []string) error {
	o, err := parseServeFlags("serve", args)
	if err != nil {
		return err
	}
	s, err := newSession(o)
	if err != nil {
		return err
	}
	return s.serve(o, func() { fmt.Println("ptymux: ready") })
}

func (s *session) pumpOutput() {
	buf := make([]byte, 32*1024)
	for {
		n, err := s.ptmx.Read(buf)
		if n > 0 {
			s.mu.Lock()
			clean, events := s.filter.feed(buf[:n])
			s.term.VTWrite(clean)
			s.dirty = true
			for _, ev := range events {
				s.inBandRequest(ev)
			}
			s.mu.Unlock()
		}
		if err != nil {
			return
		}
	}
}

func (s *session) renderLoop() {
	for range time.Tick(renderInterval) {
		s.mu.Lock()
		if s.dirty {
			s.renderLocked()
		}
		s.mu.Unlock()
	}
}

func (s *session) inBandRequest(ev string) {
	verb, host, _ := strings.Cut(ev, ";")
	if verb != "approve" || host == "" || len(host) > 253 {
		return
	}
	host = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, host)
	s.enqueueLocked(&overlay{prompt: "Approve " + host + "?", done: func(ok bool) {
		s.logDecision(ok, host)
	}})
}

func (s *session) logDecision(ok bool, host string) {
	if s.logPath == "" {
		return
	}
	verdict := "denied"
	if ok {
		verdict = "approved"
	}
	if f, err := os.OpenFile(s.logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600); err == nil {
		fmt.Fprintf(f, "%s %s\n", verdict, host)
		f.Close()
	}
}

func (s *session) handle(c net.Conn) {
	r := bufio.NewReader(c)
	line, err := r.ReadBytes('\n')
	if err != nil {
		c.Close()
		return
	}
	var h header
	if json.Unmarshal(line, &h) != nil {
		c.Close()
		return
	}
	switch h.Op {
	case "attach":
		s.serveClient(c, r, h)
	case "dump":
		defer c.Close()
		s.mu.Lock()
		text, err := s.plainLocked()
		s.mu.Unlock()
		if err != nil {
			fmt.Fprintf(c, "error: %v\n", err)
			return
		}
		fmt.Fprint(c, text)
	case "send":
		defer c.Close()
		s.ptmx.WriteString(h.Text)
	case "deny":
		defer c.Close()
		s.mu.Lock()
		s.addDenialLocked(h.Text)
		s.mu.Unlock()
	case "status":
		defer c.Close()
		s.mu.Lock()
		fmt.Fprintln(c, s.statusSummaryLocked())
		s.mu.Unlock()
	case "notify":
		defer c.Close()
		fmt.Fprintln(c, s.notify(h.Prompt, time.Duration(h.Timeout*float64(time.Second))))
	default:
		c.Close()
	}
}

func (s *session) plainLocked() (string, error) {
	f, err := libghostty.NewFormatter(s.term, libghostty.WithFormatterFormat(libghostty.FormatterFormatPlain), libghostty.WithFormatterTrim(true))
	if err != nil {
		return "", err
	}
	defer f.Close()
	return f.FormatString()
}

func (s *session) enqueueLocked(o *overlay) {
	s.queue = append(s.queue, o)
	s.showNextLocked()
}

func (s *session) showNextLocked() {
	if s.active != nil || s.review.open || len(s.queue) == 0 {
		return
	}
	s.active = s.queue[0]
	s.queue = s.queue[1:]
	s.renderLocked()
}

func (s *session) resolveLocked(approved bool) {
	o := s.active
	s.active = nil
	o.done(approved)
	s.renderLocked()
	s.showNextLocked()
}

func (s *session) notify(prompt string, timeout time.Duration) string {
	result := make(chan bool, 1)
	o := &overlay{prompt: prompt, done: func(ok bool) { result <- ok }}
	s.mu.Lock()
	s.enqueueLocked(o)
	s.mu.Unlock()
	var timer <-chan time.Time
	if timeout > 0 {
		timer = time.After(timeout)
	}
	select {
	case ok := <-result:
		if ok {
			return "approved"
		}
		return "denied"
	case <-timer:
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.active == o {
			s.active = nil
			s.renderLocked()
			s.showNextLocked()
		} else {
			for i, q := range s.queue {
				if q == o {
					s.queue = append(s.queue[:i], s.queue[i+1:]...)
					break
				}
			}
		}
		return "timeout"
	}
}

func (s *session) serveClient(c net.Conn, r *bufio.Reader, h header) {
	s.mu.Lock()
	if s.client != nil {
		writeFrame(s.client, frameEnd, nil)
		s.client.Close()
	}
	s.client = c
	s.resizeLocked(h.Cols, h.Rows)
	s.renderLocked()
	s.showNextLocked()
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		if s.client == c {
			s.client = nil
		}
		s.mu.Unlock()
		c.Close()
	}()
	for {
		kind, payload, err := readFrame(r)
		if err != nil {
			return
		}
		s.mu.Lock()
		switch kind {
		case frameResize:
			if len(payload) == 4 {
				s.resizeLocked(uint16(payload[0])<<8|uint16(payload[1]), uint16(payload[2])<<8|uint16(payload[3]))
				s.renderLocked()
			}
		case frameInput:
			s.inputLocked(payload)
		}
		s.mu.Unlock()
	}
}

func (s *session) inputLocked(payload []byte) {
	if s.active != nil {
		for _, b := range payload {
			switch b {
			case 'y', 'Y':
				s.resolveLocked(true)
			case 'n', 'N', 0x1b:
				s.resolveLocked(false)
			}
			if s.active == nil {
				break
			}
		}
		return
	}
	for len(payload) > 0 {
		if s.review.open {
			s.reviewInputLocked(payload)
			return
		}
		i := indexByte(payload, 0x00)
		if i < 0 {
			s.ptmx.Write(payload)
			return
		}
		if i > 0 {
			s.ptmx.Write(payload[:i])
		}
		payload = payload[i+1:]
		s.openReviewLocked()
	}
}

func indexByte(b []byte, c byte) int {
	for i, x := range b {
		if x == c {
			return i
		}
	}
	return -1
}

func (s *session) resizeLocked(clientCols, clientRows uint16) {
	if clientCols == 0 || clientRows < 2 {
		return
	}
	cols, rows := clientCols, clientRows-1
	if cols == s.cols && rows == s.rows {
		return
	}
	s.cols, s.rows = cols, rows
	s.term.Resize(cols, rows, 0, 0)
	pty.Setsize(s.ptmx, &pty.Winsize{Cols: cols, Rows: rows})
}
