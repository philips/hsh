package relay

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/creack/pty"

	"hsh/internal/vt"
)

const (
	paintInterval = 25 * time.Millisecond
	scrollback    = 1000
)

type Config struct {
	In         io.Reader
	Out        io.Writer
	Pty        *os.File
	Cols, Rows int
	StatusLine bool
	Key        Key
	DetachKeys string
	Count      func() (int, error)
	Overlay    func(cols, rows int) tea.Model
	Name       string
	PollEvery  time.Duration
}

type Relay struct {
	cfg Config

	mu           sync.Mutex
	wake         *sync.Cond
	term         vt.Emulator
	cols, rows   int
	statusRows   int
	paused       bool
	held         []byte
	dirty        bool
	closed       bool
	detach       []Key
	cursorHidden bool
	hints        hintFilter
	region       regionFilter
	review       reviewState
	pending      int
	covTop       int
	covRows      int
	lastPoll     time.Time
	pollNow      bool
	done         chan struct{}
}

func New(cfg Config) (*Relay, error) {
	if cfg.Count == nil || cfg.Overlay == nil {
		return nil, fmt.Errorf("relay needs a pending counter and an overlay")
	}
	if cfg.PollEvery <= 0 {
		cfg.PollEvery = 2 * time.Second
	}
	r := &Relay{cfg: cfg, done: make(chan struct{}), detach: ParseDetachKeys(cfg.DetachKeys)}
	r.wake = sync.NewCond(&r.mu)
	if cfg.StatusLine {
		r.statusRows = 1
	}
	r.cols, r.rows = cfg.Cols, cfg.Rows-r.statusRows
	if r.rows < 1 || r.cols < 1 {
		return nil, fmt.Errorf("terminal too small (%dx%d)", cfg.Cols, cfg.Rows)
	}
	term, err := vt.New(vt.Options{Cols: r.cols, Rows: r.rows, Scrollback: scrollback})
	if err != nil {
		return nil, err
	}
	r.term = term
	return r, nil
}

func (r *Relay) ContentSize() (cols, rows int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.cols, r.rows
}

func (r *Relay) Start() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := pty.Setsize(r.cfg.Pty, &pty.Winsize{Cols: uint16(r.cols), Rows: uint16(r.rows)}); err != nil {
		return err
	}
	if r.statusRows > 0 {
		var b bytes.Buffer
		b.WriteString("\x1b[0m")
		for i := 0; i < r.rows+r.statusRows; i++ {
			b.WriteString("\r\n")
		}
		fmt.Fprintf(&b, "\x1b[1;%dr", r.rows)
		r.writeOut(b.Bytes())
	}
	r.dirty = true
	return nil
}

func (r *Relay) Run() error {
	if err := r.Start(); err != nil {
		return err
	}
	go r.paintLoop()
	go r.inputLoop()
	r.pump()
	r.Close()
	return nil
}

func (r *Relay) Nudge() {
	go func() {
		time.Sleep(300 * time.Millisecond)
		r.mu.Lock()
		cols, rows := r.cols, r.rows
		r.mu.Unlock()
		if cols > 1 {
			_ = pty.Setsize(r.cfg.Pty, &pty.Winsize{Cols: uint16(cols - 1), Rows: uint16(rows)})
			time.Sleep(100 * time.Millisecond)
		}
		_ = pty.Setsize(r.cfg.Pty, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)})
	}()
}

func (r *Relay) Resize(cols, rows int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cfg.Cols, r.cfg.Rows = cols, rows
	r.cols, r.rows = cols, rows-r.statusRows
	if r.cols < 1 || r.rows < 1 {
		return
	}
	_ = r.term.Resize(r.cols, r.rows)
	if r.review.open {
		r.updateOverlayLocked(tea.WindowSizeMsg{Width: r.cols, Height: r.rows})
	}
	_ = pty.Setsize(r.cfg.Pty, &pty.Winsize{Cols: uint16(r.cols), Rows: uint16(r.rows)})
	if r.statusRows > 0 {
		r.writeOut([]byte(fmt.Sprintf("\x1b7\x1b[1;%dr\x1b8", r.rows)))
	}
	r.dirty = true
}

func (r *Relay) Close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return
	}
	r.closed = true
	close(r.done)
	if r.paused {
		r.resumeLocked()
	}
	var b bytes.Buffer
	b.WriteString("\x1b[0m\x1b[r\x1b[?25h")
	if r.statusRows > 0 {
		fmt.Fprintf(&b, "\x1b[%d;1H\x1b[2K\x1b[%d;1H\r\n", r.rows+r.statusRows, r.rows)
	}
	r.writeOut(b.Bytes())
	r.term.Close()
	r.wake.Broadcast()
}

func (r *Relay) writeOut(p []byte) {
	if len(p) > 0 {
		_, _ = r.cfg.Out.Write(p)
	}
}

func (r *Relay) pump() {
	buf := make([]byte, 32*1024)
	for {
		r.mu.Lock()
		for r.paused && !r.closed {
			r.wake.Wait()
		}
		closed := r.closed
		r.mu.Unlock()
		if closed {
			return
		}
		n, err := r.cfg.Pty.Read(buf)
		if n > 0 {
			r.mu.Lock()
			if r.closed {
				r.mu.Unlock()
				return
			}
			if r.paused {
				r.held = append(r.held, buf[:n]...)
			} else {
				r.processLocked(buf[:n])
			}
			r.mu.Unlock()
		}
		if err != nil {
			return
		}
	}
}

func (r *Relay) processLocked(data []byte) {
	clean, hints := r.hints.feed(data)
	if hints > 0 {
		r.pollNow = true
	}
	if r.statusRows > 0 {
		clean = r.region.feed(clean, r.rows)
	}
	hide := bytes.LastIndex(clean, []byte("\x1b[?25l"))
	show := bytes.LastIndex(clean, []byte("\x1b[?25h"))
	if hide > show {
		r.cursorHidden = true
	} else if show > hide {
		r.cursorHidden = false
	}
	r.term.Write(clean)
	r.writeOut(clean)
	r.dirty = true
}

func (r *Relay) paintLoop() {
	t := time.NewTicker(paintInterval)
	defer t.Stop()
	for {
		select {
		case <-r.done:
			return
		case <-t.C:
		}
		r.mu.Lock()
		if time.Since(r.lastPoll) >= r.cfg.PollEvery || r.pollNow {
			r.pollLocked()
		}
		if r.dirty {
			r.paintLocked()
		}
		r.mu.Unlock()
	}
}

func (r *Relay) inputLoop() {
	buf := make([]byte, 4096)
	for {
		n, err := r.cfg.In.Read(buf)
		if n > 0 {
			r.mu.Lock()
			if r.closed {
				r.mu.Unlock()
				return
			}
			r.inputLocked(buf[:n])
			r.mu.Unlock()
		}
		if err != nil {
			return
		}
	}
}

func (r *Relay) inputLocked(p []byte) {
	for len(p) > 0 {
		if r.review.open {
			p = r.reviewInputLocked(p)
			continue
		}
		idx, width := r.cfg.Key.find(p)
		if idx < 0 {
			_, _ = r.cfg.Pty.Write(translateKeys(p, r.detach))
			return
		}
		if idx > 0 {
			_, _ = r.cfg.Pty.Write(translateKeys(p[:idx], r.detach))
		}
		p = p[idx+width:]
		r.openReviewLocked()
	}
}

func (r *Relay) resumeLocked() {
	r.paused = false
	r.wake.Broadcast()
	held := r.held
	r.held = nil
	if len(held) > 0 {
		r.processLocked(held)
	}
	r.dirty = true
}
