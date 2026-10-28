package relay

import (
	"bytes"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
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

func (s itemStatus) String() string {
	return [...]string{"pending", "approved", "denied"}[s]
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
	err   string
}

func (r *Relay) pendingLocked() int {
	n := 0
	for _, it := range r.review.items {
		if it.status == statusPending {
			n++
		}
	}
	return n
}

func (r *Relay) pollLocked() {
	r.lastPoll = time.Now()
	r.pollNow = false
	rows, err := r.cfg.Source.Pending()
	if err != nil {
		return
	}
	seen := map[string]bool{}
	for _, row := range rows {
		seen[row.Host] = true
		found := false
		for _, it := range r.review.items {
			if it.host == row.Host && it.status == statusPending {
				if it.count != row.Count {
					it.count, it.last = row.Count, row.Last
					r.dirty = true
				}
				found = true
			}
		}
		if !found {
			r.review.items = append(r.review.items, &reviewItem{host: row.Host, count: row.Count, last: row.Last})
			r.dirty = true
		}
	}
	kept := r.review.items[:0]
	for _, it := range r.review.items {
		if it.status != statusPending || seen[it.host] {
			kept = append(kept, it)
		} else {
			r.dirty = true
		}
	}
	r.review.items = kept
	if r.review.sel >= len(r.review.items) {
		r.review.sel = max(0, len(r.review.items)-1)
	}
}

func (r *Relay) openReviewLocked() {
	r.pollLocked()
	r.review.open = true
	r.review.err = ""
	r.review.sel = 0
	r.selectNextPendingLocked(-1)
	r.paintLocked()
}

func (r *Relay) closeReviewLocked() {
	r.review.open = false
	kept := r.review.items[:0]
	for _, it := range r.review.items {
		if it.status == statusPending {
			kept = append(kept, it)
		}
	}
	r.review.items = kept
	r.paintLocked()
}

func (r *Relay) selectNextPendingLocked(from int) {
	items := r.review.items
	for i := 1; i <= len(items); i++ {
		j := (from + i + len(items)) % len(items)
		if items[j].status == statusPending {
			r.review.sel = j
			return
		}
	}
}

func (r *Relay) decideLocked(it *reviewItem, approve bool) {
	if it.status != statusPending {
		return
	}
	var err error
	if approve {
		err = r.cfg.Source.Allow(it.host)
	} else {
		err = r.cfg.Source.Deny(it.host)
	}
	if err != nil {
		r.review.err = fmt.Sprintf("%s: %v", it.host, err)
		return
	}
	r.review.err = ""
	it.status = statusDenied
	if approve {
		it.status = statusApproved
	}
}

func (r *Relay) moveLocked(d int) {
	if n := len(r.review.items); n > 0 {
		r.review.sel = (r.review.sel + d + n) % n
	}
}

func (r *Relay) reviewInputLocked(p []byte) {
	st := &r.review
	for i := 0; i < len(p); i++ {
		if idx, _ := r.cfg.Key.find(p[i:]); idx == 0 {
			r.closeReviewLocked()
			return
		}
		b := p[i]
		switch {
		case b == 0x1b:
			if i+2 < len(p) && (p[i+1] == '[' || p[i+1] == 'O') {
				switch p[i+2] {
				case 'A':
					r.moveLocked(-1)
				case 'B':
					r.moveLocked(1)
				}
				i += 2
				continue
			}
			if i == len(p)-1 {
				r.closeReviewLocked()
				return
			}
		case b == 'k':
			r.moveLocked(-1)
		case b == 'j':
			r.moveLocked(1)
		case b == 'a' || b == 'd':
			if st.sel < len(st.items) {
				r.decideLocked(st.items[st.sel], b == 'a')
				r.selectNextPendingLocked(st.sel)
			}
		case b == 'A' || b == 'D':
			for _, it := range st.items {
				r.decideLocked(it, b == 'A')
			}
		}
	}
	r.paintLocked()
}

func padTo(text string, width int) string {
	n := utf8.RuneCountInString(text)
	if n > width {
		return string([]rune(text)[:width])
	}
	return text + strings.Repeat(" ", width-n)
}

func (r *Relay) statusLineLocked() string {
	var sgr, text string
	name := r.cfg.Name
	if name == "" {
		name = "session"
	}
	switch {
	case r.review.open:
		sgr = sgrRed
		text = " hsh | REVIEW | up/down select | a approve | d deny | A all | D deny all | Esc close"
	default:
		sgr = sgrGreen
		switch n := r.pendingLocked(); {
		case n == 0:
			text = " hsh | " + name + " | proxy: nothing to review"
		case n == 1:
			text = " hsh | " + name + " | * 1 proxy request needs review | Ctrl+Space to review"
		default:
			text = fmt.Sprintf(" hsh | %s | * %d proxy requests need review | Ctrl+Space to review", name, n)
		}
	}
	return sgr + padTo(text, r.cols) + "\x1b[0m"
}

func (r *Relay) dialogLocked() (string, int, int) {
	st := &r.review
	width := min(r.cols-4, 72)
	if width < 30 || r.rows < 5 {
		return "", 0, 0
	}
	lines := []string{"Proxy requests awaiting review", ""}
	if len(st.items) == 0 {
		lines = append(lines, "Nothing to review.")
	}
	for i, it := range st.items {
		mark := "  "
		if i == st.sel {
			mark = "> "
		}
		age := time.Since(it.last).Round(time.Second)
		lines = append(lines, fmt.Sprintf("%s%-34s x%-3d %6s ago  %s", mark, it.host, it.count, age, it.status))
	}
	hint := "a approve  d deny  A all  D deny all  Esc close"
	if st.err != "" {
		hint = "error: " + st.err
	}
	lines = append(lines, "", hint)
	maxBody := r.rows - 2
	if len(lines) > maxBody {
		keep := maxBody - 2
		lines = append(lines[:keep:keep], "", hint)
	}
	height := len(lines) + 2
	top := (r.rows-height)/2 + 1
	left := (r.cols-width)/2 + 1
	var b bytes.Buffer
	b.WriteString("\x1b[?25l")
	fmt.Fprintf(&b, "\x1b[%d;%dH%s+%s+", top, left, sgrBox, strings.Repeat("-", width-2))
	for i, ln := range lines {
		sgr := sgrBox
		idx := i - 2
		if idx >= 0 && idx < len(st.items) {
			if idx == st.sel {
				sgr = sgrSel
			} else if st.items[idx].status != statusPending {
				sgr = sgrDim
			}
		}
		fmt.Fprintf(&b, "\x1b[%d;%dH%s|%s|", top+1+i, left, sgr, padTo(" "+ln, width-2))
	}
	fmt.Fprintf(&b, "\x1b[%d;%dH%s+%s+\x1b[0m", top+1+len(lines), left, sgrBox, strings.Repeat("-", width-2))
	return b.String(), top, height
}

func (r *Relay) cursorModeLocked() string {
	if r.cursorHidden {
		return "\x1b[?25l"
	}
	return "\x1b[?25h"
}

func (r *Relay) repaintRowsLocked(b *bytes.Buffer, top, n int) {
	for y := top; y < top+n && y <= r.rows; y++ {
		if y < 1 {
			continue
		}
		row, _ := r.term.Row(y - 1)
		fmt.Fprintf(b, "\x1b[%d;1H\x1b[0m%s\x1b[0m\x1b[K", y, row)
	}
}

func (r *Relay) paintLocked() {
	want := r.review.open
	ground := r.term.Ground()
	if want && !r.paused {
		if !ground {
			r.dirty = true
			return
		}
		r.paused = true
	}
	if !want && r.paused {
		var b bytes.Buffer
		b.WriteString("\x1b7")
		r.repaintRowsLocked(&b, r.covTop, r.covRows)
		b.WriteString("\x1b8")
		b.WriteString(r.cursorModeLocked())
		r.covTop, r.covRows = 0, 0
		r.writeOut(b.Bytes())
		r.resumeLocked()
		return
	}
	if !ground {
		r.dirty = true
		return
	}
	r.dirty = false
	var b bytes.Buffer
	b.WriteString("\x1b7\x1b[0m")
	if r.paused {
		overlay, top, n := r.dialogLocked()
		r.covTop, r.covRows = top, n
		b.WriteString(overlay)
	}
	if r.statusRows > 0 {
		fmt.Fprintf(&b, "\x1b[%d;1H%s", r.rows+1, r.statusLineLocked())
	}
	b.WriteString("\x1b8")
	if r.paused {
		b.WriteString("\x1b[?25l")
	}
	r.writeOut(b.Bytes())
}
