package main

import (
	"bytes"
	"flag"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"code.selman.me/hauntty/libghostty"
)

const (
	sgrGreen = "\x1b[0;30;42m"
	sgrRed   = "\x1b[0;97;41m"
	sgrBox   = "\x1b[0;30;47m"
	sgrSel   = "\x1b[0;97;44m"
	sgrDim   = "\x1b[0;90;47m"
)

type itemStatus int

const (
	statusPending itemStatus = iota
	statusApproved
	statusDenied
)

func (st itemStatus) String() string {
	return [...]string{"pending", "approved", "denied"}[st]
}

type reviewItem struct {
	host   string
	count  int
	last   time.Time
	status itemStatus
}

type reviewState struct {
	items []*reviewItem
	open  bool
	sel   int
}

func (s *session) pendingLocked() int {
	n := 0
	for _, it := range s.review.items {
		if it.status == statusPending {
			n++
		}
	}
	return n
}

func (s *session) addDenialLocked(host string) {
	host = strings.TrimSpace(host)
	if host == "" {
		return
	}
	for _, it := range s.review.items {
		if it.host == host && it.status == statusPending {
			it.count++
			it.last = time.Now()
			s.renderLocked()
			return
		}
	}
	s.review.items = append(s.review.items, &reviewItem{host: host, count: 1, last: time.Now()})
	s.renderLocked()
}

func (s *session) openReviewLocked() {
	s.review.open = true
	s.review.sel = 0
	s.selectNextPendingLocked(-1)
	s.renderLocked()
}

func (s *session) closeReviewLocked() {
	s.review.open = false
	kept := s.review.items[:0]
	for _, it := range s.review.items {
		if it.status == statusPending {
			kept = append(kept, it)
		}
	}
	s.review.items = kept
	s.renderLocked()
	s.showNextLocked()
}

func (s *session) selectNextPendingLocked(from int) {
	items := s.review.items
	for i := 1; i <= len(items); i++ {
		j := (from + i + len(items)) % len(items)
		if items[j].status == statusPending {
			s.review.sel = j
			return
		}
	}
}

func (s *session) decideLocked(it *reviewItem, ok bool) {
	if it.status != statusPending {
		return
	}
	it.status = statusDenied
	if ok {
		it.status = statusApproved
	}
	s.logDecision(ok, it.host)
}

func (s *session) reviewInputLocked(p []byte) {
	r := &s.review
	for i := 0; i < len(p); i++ {
		b := p[i]
		switch {
		case b == 0x00:
			s.closeReviewLocked()
			return
		case b == 0x1b:
			if i+2 < len(p) && (p[i+1] == '[' || p[i+1] == 'O') {
				switch p[i+2] {
				case 'A':
					s.moveLocked(-1)
				case 'B':
					s.moveLocked(1)
				}
				i += 2
				continue
			}
			if i == len(p)-1 {
				s.closeReviewLocked()
				return
			}
		case b == 'k':
			s.moveLocked(-1)
		case b == 'j':
			s.moveLocked(1)
		case b == 'a' || b == 'd':
			if r.sel < len(r.items) {
				s.decideLocked(r.items[r.sel], b == 'a')
				s.selectNextPendingLocked(r.sel)
			}
		case b == 'A' || b == 'D':
			for _, it := range r.items {
				s.decideLocked(it, b == 'A')
			}
		}
	}
	s.renderLocked()
}

func (s *session) moveLocked(d int) {
	if n := len(s.review.items); n > 0 {
		s.review.sel = (s.review.sel + d + n) % n
	}
}

func (s *session) statusSummaryLocked() string {
	color := "green"
	if s.review.open || s.active != nil {
		color = "red"
	}
	mode := "normal"
	switch {
	case s.review.open:
		mode = "review"
	case s.active != nil:
		mode = "prompt"
	}
	return fmt.Sprintf("mode=%s color=%s pending=%d", mode, color, s.pendingLocked())
}

func padTo(text string, width int) string {
	n := utf8.RuneCountInString(text)
	if n > width {
		r := []rune(text)
		return string(r[:width])
	}
	return text + strings.Repeat(" ", width-n)
}

func (s *session) statusLineLocked() string {
	cols := int(s.cols)
	var sgr, text string
	switch {
	case s.review.open:
		sgr = sgrRed
		text = " hsh | REVIEW | up/down select | a approve | d deny | A all | D deny all | Esc close"
	case s.active != nil:
		sgr = sgrRed
		text = " hsh | APPROVAL REQUESTED | answer in the bar above"
	default:
		sgr = sgrGreen
		switch n := s.pendingLocked(); {
		case n == 0:
			text = " hsh | " + s.name + " | proxy: nothing to review"
		case n == 1:
			text = " hsh | " + s.name + " | * 1 proxy request needs review | Ctrl+Space to review"
		default:
			text = fmt.Sprintf(" hsh | %s | * %d proxy requests need review | Ctrl+Space to review", s.name, n)
		}
	}
	return sgr + padTo(text, cols) + "\x1b[0m"
}

func (s *session) dialogLocked() string {
	cols, rows := int(s.cols), int(s.rows)
	r := &s.review
	width := min(cols-4, 72)
	if width < 30 {
		return ""
	}
	lines := []string{"Proxy requests awaiting review", ""}
	if len(r.items) == 0 {
		lines = append(lines, "Nothing to review.")
	}
	for i, it := range r.items {
		mark := "  "
		if i == r.sel {
			mark = "> "
		}
		age := time.Since(it.last).Round(time.Second)
		lines = append(lines, fmt.Sprintf("%s%-34s x%-3d %6s ago  %s", mark, it.host, it.count, age, it.status))
	}
	maxBody := rows - 2
	if len(lines) > maxBody {
		lines = lines[:maxBody]
	}
	height := len(lines) + 2
	top := (rows-height)/2 + 1
	left := (cols-width)/2 + 1
	var b bytes.Buffer
	b.WriteString("\x1b[?25l")
	fmt.Fprintf(&b, "\x1b[%d;%dH%s+%s+", top, left, sgrBox, strings.Repeat("-", width-2))
	for i, ln := range lines {
		sgr := sgrBox
		if i >= 2 && i-2 < len(r.items) && i-2 == r.sel {
			sgr = sgrSel
		} else if i >= 2 && i-2 < len(r.items) && r.items[i-2].status != statusPending {
			sgr = sgrDim
		}
		fmt.Fprintf(&b, "\x1b[%d;%dH%s|%s|", top+1+i, left, sgr, padTo(" "+ln, width-2))
	}
	fmt.Fprintf(&b, "\x1b[%d;%dH%s+%s+", top+1+len(lines), left, sgrBox, strings.Repeat("-", width-2))
	return b.String()
}

func (s *session) promptBarLocked() string {
	cols := int(s.cols)
	text := " " + s.active.prompt + "   [y] approve   [n] deny "
	blank := strings.Repeat(" ", cols)
	row := int(s.rows) - 2
	if row < 1 {
		row = 1
	}
	return fmt.Sprintf("\x1b[?25l\x1b[%d;1H\x1b[0;7m%s\x1b[%d;1H%s\x1b[%d;1H%s\x1b[0m",
		row, blank, row+1, padTo(text, cols), row+2, blank)
}

func (s *session) renderLocked() {
	s.dirty = false
	if s.client == nil {
		return
	}
	f, err := libghostty.NewFormatter(s.term, libghostty.WithFormatterFormat(libghostty.FormatterFormatVT), libghostty.WithFormatterExtraCursor(true))
	if err != nil {
		return
	}
	screen, err := f.Format()
	f.Close()
	if err != nil {
		return
	}
	var b bytes.Buffer
	b.WriteString("\x1b[?2026h\x1b[?25l\x1b[0m\x1b[H\x1b[2J")
	b.Write(screen)
	b.WriteString("\x1b7\x1b[0m")
	switch {
	case s.review.open:
		b.WriteString(s.dialogLocked())
	case s.active != nil:
		b.WriteString(s.promptBarLocked())
	}
	fmt.Fprintf(&b, "\x1b[%d;1H%s", int(s.rows)+1, s.statusLineLocked())
	b.WriteString("\x1b8")
	if !s.review.open && s.active == nil {
		b.WriteString("\x1b[?25h")
	}
	b.WriteString("\x1b[?2026l")
	writeFrame(s.client, frameOutput, b.Bytes())
}

func (s *session) fakeDenials() {
	hosts := []string{"api.openai.com", "registry.npmjs.org", "pypi.org", "files.pythonhosted.org", "github.com", "raw.githubusercontent.com", "example.com"}
	time.Sleep(3 * time.Second)
	for i := 0; ; i++ {
		s.mu.Lock()
		s.addDenialLocked(hosts[i%len(hosts)])
		s.mu.Unlock()
		time.Sleep(4 * time.Second)
	}
}

func parseServeFlags(name string, args []string) (serveOptions, error) {
	fs := flag.NewFlagSet(name, flag.ExitOnError)
	o := serveOptions{}
	fs.StringVar(&o.sock, "sock", "", "unix socket path")
	fs.StringVar(&o.logPath, "log", "", "file that records approval decisions")
	fs.BoolVar(&o.fake, "fake", false, "raise a fake proxy denial every few seconds")
	fs.StringVar(&o.name, "name", "session", "name shown in the status line")
	fs.Parse(args)
	o.args = fs.Args()
	if name == "serve" && o.sock == "" {
		return o, fmt.Errorf("serve --sock S [--log FILE] [--fake] -- CMD [ARG...]")
	}
	if len(o.args) == 0 {
		return o, fmt.Errorf("a command to run is required")
	}
	return o, nil
}
