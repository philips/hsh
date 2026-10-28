package main

import (
	"bytes"
	"fmt"

	"code.selman.me/hauntty/libghostty"
)

func (s *session) rowVTLocked(y int) string {
	a, err := s.term.GridRef(libghostty.Point{Tag: libghostty.PointTagActive, X: 0, Y: uint32(y)})
	if err != nil {
		return ""
	}
	z, err := s.term.GridRef(libghostty.Point{Tag: libghostty.PointTagActive, X: s.cols - 1, Y: uint32(y)})
	if err != nil {
		return ""
	}
	f, err := libghostty.NewFormatter(s.term, libghostty.WithFormatterFormat(libghostty.FormatterFormatVT),
		libghostty.WithFormatterSelection(&libghostty.Selection{Start: *a, End: *z}))
	if err != nil {
		return ""
	}
	defer f.Close()
	out, err := f.FormatString()
	if err != nil {
		return ""
	}
	return string(bytes.TrimRight([]byte(out), "\r\n"))
}

func (s *session) repaintRowsLocked(b *bytes.Buffer, top, n int) {
	for y := top; y < top+n && y <= int(s.rows); y++ {
		if y < 1 {
			continue
		}
		fmt.Fprintf(b, "\x1b[%d;1H\x1b[0m%s\x1b[0m\x1b[K", y, s.rowVTLocked(y-1))
	}
}

func (s *session) cursorModeLocked() string {
	if s.cursorHidden {
		return "\x1b[?25l"
	}
	return "\x1b[?25h"
}

func (s *session) attachPassLocked() {
	if s.client == nil {
		return
	}
	var b bytes.Buffer
	fmt.Fprintf(&b, "\x1b[0m\x1b[2J\x1b[H\x1b[1;%dr", s.rows)
	f, err := libghostty.NewFormatter(s.term, libghostty.WithFormatterFormat(libghostty.FormatterFormatVT), libghostty.WithFormatterExtraCursor(true))
	if err == nil {
		if replay, err := f.Format(); err == nil {
			b.Write(replay)
		}
		f.Close()
	}
	b.WriteString("\x1b7")
	s.repaintRowsLocked(&b, 1, int(s.rows))
	b.WriteString("\x1b8")
	b.WriteString(s.cursorModeLocked())
	writeFrame(s.client, frameOutput, b.Bytes())
	s.dirty = true
}

func (s *session) paintPassLocked() {
	if s.client == nil {
		s.dirty = false
		return
	}
	want := s.review.open || s.active != nil
	ground, _ := s.term.VTGround()
	if want && !s.paused {
		if !ground {
			return
		}
		s.paused = true
	}
	if !want && s.paused {
		s.closeOverlayLocked()
		return
	}
	if !ground {
		return
	}
	s.dirty = false
	var b bytes.Buffer
	b.WriteString("\x1b7\x1b[0m")
	if s.paused {
		var overlay string
		var top, n int
		if s.review.open {
			overlay, top, n = s.dialogLocked()
		} else {
			overlay, top, n = s.promptBarLocked()
		}
		s.covTop, s.covRows = top, n
		b.WriteString(overlay)
	}
	fmt.Fprintf(&b, "\x1b[%d;1H%s", int(s.rows)+1, s.statusLineLocked())
	b.WriteString("\x1b8")
	if s.paused {
		b.WriteString("\x1b[?25l")
	}
	writeFrame(s.client, frameOutput, b.Bytes())
}

func (s *session) closeOverlayLocked() {
	var b bytes.Buffer
	b.WriteString("\x1b7")
	s.repaintRowsLocked(&b, s.covTop, s.covRows)
	b.WriteString("\x1b8")
	b.WriteString(s.cursorModeLocked())
	s.covTop, s.covRows = 0, 0
	writeFrame(s.client, frameOutput, b.Bytes())
	s.resumeLocked()
}

func (s *session) resumeLocked() {
	s.paused = false
	s.wake.Broadcast()
	held := s.held
	s.held = nil
	if len(held) > 0 {
		s.processOutputLocked(held)
	}
	s.dirty = true
}

func (s *session) releaseOverlaysLocked() {
	s.review.open = false
	if s.active != nil {
		s.queue = append([]*overlay{s.active}, s.queue...)
		s.active = nil
	}
	if s.paused {
		s.resumeLocked()
	}
}
