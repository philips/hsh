//go:build goexperiment.simd

package vt

import (
	"bytes"
	"fmt"

	"code.selman.me/hauntty/libghostty"
)

const Backend = "libghostty-vt"

func Available() bool { return true }

type ghostty struct {
	term *libghostty.Terminal
	cols int
	rows int
}

func New(o Options) (Emulator, error) {
	if o.Cols <= 0 || o.Rows <= 0 {
		return nil, fmt.Errorf("invalid size %dx%d", o.Cols, o.Rows)
	}
	opts := []libghostty.TerminalOption{libghostty.WithSize(uint16(o.Cols), uint16(o.Rows))}
	if o.Scrollback > 0 {
		opts = append(opts, libghostty.WithMaxScrollbackLines(uint(o.Scrollback)))
	}
	t, err := libghostty.NewTerminal(opts...)
	if err != nil {
		return nil, err
	}
	return &ghostty{term: t, cols: o.Cols, rows: o.Rows}, nil
}

func (g *ghostty) Write(p []byte) { g.term.VTWrite(p) }

func (g *ghostty) Resize(cols, rows int) error {
	if err := g.term.Resize(uint16(cols), uint16(rows), 0, 0); err != nil {
		return err
	}
	g.cols, g.rows = cols, rows
	return nil
}

func (g *ghostty) Ground() bool {
	ok, err := g.term.VTGround()
	return err == nil && ok
}

func (g *ghostty) Cursor() (int, int) {
	x, _ := g.term.CursorX()
	y, _ := g.term.CursorY()
	return int(x), int(y)
}

func (g *ghostty) format(opts ...libghostty.FormatterOption) ([]byte, error) {
	f, err := libghostty.NewFormatter(g.term, opts...)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return f.Format()
}

func (g *ghostty) Plain() (string, error) {
	b, err := g.format(libghostty.WithFormatterFormat(libghostty.FormatterFormatPlain), libghostty.WithFormatterTrim(true))
	return string(b), err
}

func (g *ghostty) Replay() ([]byte, error) {
	return g.format(libghostty.WithFormatterFormat(libghostty.FormatterFormatVT), libghostty.WithFormatterExtraCursor(true))
}

func (g *ghostty) Row(y int) (string, error) {
	if y < 0 || y >= g.rows {
		return "", fmt.Errorf("row %d out of range", y)
	}
	start, err := g.term.GridRef(libghostty.Point{Tag: libghostty.PointTagActive, X: 0, Y: uint32(y)})
	if err != nil {
		return "", err
	}
	end, err := g.term.GridRef(libghostty.Point{Tag: libghostty.PointTagActive, X: uint16(g.cols - 1), Y: uint32(y)})
	if err != nil {
		return "", err
	}
	b, err := g.format(libghostty.WithFormatterFormat(libghostty.FormatterFormatVT),
		libghostty.WithFormatterSelection(&libghostty.Selection{Start: *start, End: *end}))
	return string(bytes.TrimRight(b, "\r\n")), err
}

func (g *ghostty) Close() { g.term.Close() }
