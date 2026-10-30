package relay

import (
	"bytes"
	"fmt"
	"reflect"
	"strings"
	"time"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

const (
	sgrGreen = "\x1b[0;30;42m"
	sgrBlue  = "\x1b[0;97;44m"
	sgrRed   = "\x1b[0;97;41m"
)

type reviewState struct {
	open   bool
	model  tea.Model
	gen    int
	parser inputParser
}

func (r *Relay) pollLocked() {
	r.lastPoll = time.Now()
	r.pollNow = false
	n, heldN, err := r.cfg.Count()
	if err != nil || (n == r.pending && heldN == r.heldReqs) {
		return
	}
	r.pending, r.heldReqs = n, heldN
	r.dirty = true
}

func (r *Relay) openReviewLocked() {
	r.pollLocked()
	r.review.open = true
	r.review.gen++
	r.review.model = r.cfg.Overlay(r.cols, r.rows)
	r.review.parser = inputParser{}
	r.updateOverlayLocked(tea.WindowSizeMsg{Width: r.cols, Height: r.rows})
	r.runCmdLocked(r.review.model.Init(), r.review.gen)
	r.paintLocked()
}

func (r *Relay) closeReviewLocked() {
	r.review.open = false
	r.review.model = nil
	r.review.gen++
	r.pollLocked()
	r.paintLocked()
}

func (r *Relay) updateOverlayLocked(msg tea.Msg) {
	if !r.review.open {
		return
	}
	model, cmd := r.review.model.Update(msg)
	r.review.model = model
	if isQuitCmd(cmd) {
		r.closeReviewLocked()
		return
	}
	r.runCmdLocked(cmd, r.review.gen)
}

func isQuitCmd(cmd tea.Cmd) bool {
	return cmd != nil && reflect.ValueOf(cmd).Pointer() == reflect.ValueOf(tea.Quit).Pointer()
}

func (r *Relay) runCmdLocked(cmd tea.Cmd, gen int) {
	if cmd == nil {
		return
	}
	go func() {
		msg := cmd()
		r.mu.Lock()
		defer r.mu.Unlock()
		if r.closed || !r.review.open || r.review.gen != gen {
			return
		}
		r.handleMsgLocked(msg, gen)
		r.paintLocked()
	}()
}

func (r *Relay) handleMsgLocked(msg tea.Msg, gen int) {
	switch m := msg.(type) {
	case nil:
	case tea.QuitMsg:
		r.closeReviewLocked()
	case tea.BatchMsg:
		for _, c := range m {
			r.runCmdLocked(c, gen)
		}
	default:
		r.updateOverlayLocked(msg)
	}
}

func (r *Relay) reviewInputLocked(p []byte) []byte {
	rest := p
	for r.review.open && len(rest) > 0 {
		idx, width := r.cfg.Key.find(rest)
		chunk := rest
		if idx >= 0 {
			chunk = rest[:idx]
		}
		msgs, ends := r.review.parser.feedTracked(chunk)
		lastEnd := 0
		for i, km := range msgs {
			if !r.review.open {
				break
			}
			r.updateOverlayLocked(km)
			lastEnd = ends[i]
		}
		if !r.review.open {
			out := append([]byte(nil), chunk[lastEnd:]...)
			if idx >= 0 {
				out = append(out, rest[idx:]...)
			}
			rest = out
			break
		}
		if idx < 0 {
			rest = nil
			break
		}
		sent := append([]byte(nil), rest[idx:idx+width]...)
		rest = rest[idx+width:]
		r.closeReviewLocked()
		_, _ = r.cfg.Pty.Write(sent)
	}
	r.paintLocked()
	if r.review.open {
		return nil
	}
	return rest
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
		text = " hsh | REVIEW | same keys as `hsh monitor` | q or Esc closes"
	default:
		sgr = sgrGreen
		if r.pending > 0 {
			sgr = sgrBlue
		}
		heldNote := ""
		if r.heldReqs > 0 {
			heldNote = fmt.Sprintf(" (%d held)", r.heldReqs)
		}
		switch n := r.pending; {
		case n == 0:
			text = " hsh | " + name + " | proxy: nothing to review"
		case n == 1:
			text = " hsh | " + name + " | * 1 proxy request needs review" + heldNote + " | Ctrl+Space to review"
		default:
			text = fmt.Sprintf(" hsh | %s | * %d proxy requests need review%s | Ctrl+Space to review", name, n, heldNote)
		}
	}
	return sgr + padTo(text, r.cols) + "\x1b[0m"
}

func (r *Relay) pasteModeLocked() string {
	if r.bracketedPaste {
		return "\x1b[?2004h"
	}
	return "\x1b[?2004l"
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

func (r *Relay) overlayFrameLocked() string {
	view := ""
	if r.review.model != nil {
		view = r.review.model.View()
	}
	lines := strings.Split(view, "\n")
	var b strings.Builder
	b.WriteString("\x1b[?25l")
	for y := 0; y < r.rows; y++ {
		line := ""
		if y < len(lines) {
			line = ansi.Truncate(strings.TrimRight(lines[y], "\r"), r.cols, "")
		}
		fmt.Fprintf(&b, "\x1b[%d;1H\x1b[0m%s\x1b[0m\x1b[K", y+1, line)
	}
	return b.String()
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
		r.writeOut([]byte("\x1b[?2004h"))
	}
	if !want && r.paused {
		var b bytes.Buffer
		b.WriteString("\x1b7")
		r.repaintRowsLocked(&b, 1, r.rows)
		b.WriteString("\x1b8")
		b.WriteString(r.cursorModeLocked())
		b.WriteString(r.pasteModeLocked())
		if r.statusRows > 0 {
			fmt.Fprintf(&b, "\x1b7\x1b[%d;1H%s\x1b8", r.rows+1, r.statusLineLocked())
		}
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
		b.WriteString(r.overlayFrameLocked())
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
