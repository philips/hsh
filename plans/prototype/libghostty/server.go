package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"code.selman.me/hauntty/libghostty"
	"github.com/creack/pty"
)

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
	term    *libghostty.Terminal
	ptmx    *os.File
	filter  oscFilter
	client  net.Conn
	cols    uint16
	rows    uint16
	active  *overlay
	queue   []*overlay
	logPath string
}

func cmdServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	sock := fs.String("sock", "", "unix socket path")
	logPath := fs.String("log", "", "file that records decisions made on in-band approval requests")
	fs.Parse(args)
	cmdArgs := fs.Args()
	if *sock == "" || len(cmdArgs) == 0 {
		return fmt.Errorf("serve --sock S [--log FILE] -- CMD [ARG...]")
	}
	s := &session{cols: 80, rows: 24, logPath: *logPath}
	var err error
	s.term, err = libghostty.NewTerminal(libghostty.WithSize(s.cols, s.rows), libghostty.WithMaxScrollbackLines(10000))
	if err != nil {
		return err
	}
	cmd := exec.Command(cmdArgs[0], cmdArgs[1:]...)
	cmd.Env = append(os.Environ(), "TERM=xterm-256color")
	s.ptmx, err = pty.StartWithSize(cmd, &pty.Winsize{Cols: s.cols, Rows: s.rows})
	if err != nil {
		return err
	}
	os.Remove(*sock)
	l, err := net.Listen("unix", *sock)
	if err != nil {
		return err
	}
	os.Chmod(*sock, 0o600)
	defer os.Remove(*sock)

	go s.pumpOutput()
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go s.handle(c)
		}
	}()
	fmt.Println("ptymux: ready")
	cmd.Wait()
	s.mu.Lock()
	if s.client != nil {
		writeFrame(s.client, frameEnd, nil)
		s.client.Close()
	}
	s.mu.Unlock()
	return nil
}

func (s *session) pumpOutput() {
	buf := make([]byte, 32*1024)
	for {
		n, err := s.ptmx.Read(buf)
		if n > 0 {
			s.mu.Lock()
			clean, events := s.filter.feed(buf[:n])
			s.term.VTWrite(clean)
			if s.client != nil && s.active == nil {
				writeFrame(s.client, frameOutput, clean)
			}
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
		verdict := "denied"
		if ok {
			verdict = "approved"
		}
		if s.logPath != "" {
			if f, err := os.OpenFile(s.logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600); err == nil {
				fmt.Fprintf(f, "%s %s\n", verdict, host)
				f.Close()
			}
		}
	}})
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

func (s *session) redrawLocked() {
	if s.client == nil {
		return
	}
	f, err := libghostty.NewFormatter(s.term, libghostty.WithFormatterFormat(libghostty.FormatterFormatVT), libghostty.WithFormatterExtraCursor(true))
	if err != nil {
		return
	}
	defer f.Close()
	screen, err := f.Format()
	if err != nil {
		return
	}
	out := append([]byte("\x1b[?25l\x1b[0m\x1b[H\x1b[2J"), screen...)
	out = append(out, "\x1b[?25h"...)
	writeFrame(s.client, frameOutput, out)
}

func (s *session) drawOverlayLocked() {
	if s.client == nil || s.active == nil {
		return
	}
	cols := int(s.cols)
	text := " " + s.active.prompt + "   [y] approve   [n] deny "
	if len(text) > cols {
		text = text[:cols]
	}
	blank := strings.Repeat(" ", cols)
	row := int(s.rows) - 2
	if row < 1 {
		row = 1
	}
	var b strings.Builder
	b.WriteString("\x1b[?25l\x1b[0m")
	fmt.Fprintf(&b, "\x1b[%d;1H\x1b[7m%s", row, blank)
	fmt.Fprintf(&b, "\x1b[%d;1H%s%s", row+1, text, blank[len(text):])
	fmt.Fprintf(&b, "\x1b[%d;1H%s\x1b[0m", row+2, blank)
	writeFrame(s.client, frameOutput, []byte(b.String()))
}

func (s *session) enqueueLocked(o *overlay) {
	s.queue = append(s.queue, o)
	s.showNextLocked()
}

func (s *session) showNextLocked() {
	if s.active != nil || s.client == nil || len(s.queue) == 0 {
		return
	}
	s.active = s.queue[0]
	s.queue = s.queue[1:]
	s.drawOverlayLocked()
}

func (s *session) resolveLocked(approved bool) {
	o := s.active
	s.active = nil
	o.done(approved)
	s.redrawLocked()
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
			s.redrawLocked()
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
	s.redrawLocked()
	if s.active != nil {
		s.drawOverlayLocked()
	}
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
				s.redrawLocked()
				if s.active != nil {
					s.drawOverlayLocked()
				}
			}
		case frameInput:
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
			} else {
				s.ptmx.Write(payload)
			}
		}
		s.mu.Unlock()
	}
}

func (s *session) resizeLocked(cols, rows uint16) {
	if cols == 0 || rows == 0 || (cols == s.cols && rows == s.rows) {
		return
	}
	s.cols, s.rows = cols, rows
	s.term.Resize(cols, rows, 0, 0)
	pty.Setsize(s.ptmx, &pty.Winsize{Cols: cols, Rows: rows})
}
