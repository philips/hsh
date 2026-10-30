package vt

import (
	"strings"
	"testing"
)

func FuzzWrite(f *testing.F) {
	if !Available() {
		f.Skip("built without GOEXPERIMENT=simd")
	}
	f.Add([]byte("hello"))
	f.Add([]byte("\x1b[31mred\x1b[0m"))
	f.Add([]byte("\x1b]7777;hint\007"))
	f.Add([]byte("\x1b[1;50r\x1b[2J"))
	f.Add([]byte(strings.Repeat("A", 4096)))
	f.Add([]byte("\x1bP1|r\x1b\\"))
	f.Fuzz(func(t *testing.T, p []byte) {
		term, err := New(Options{Cols: 80, Rows: 24, Scrollback: 100})
		if err != nil {
			t.Skip(err)
		}
		defer term.Close()
		term.Write(p)
		if err := term.Resize(40, 10); err != nil {
			t.Fatalf("resize failed: %v", err)
		}
		term.Write(p)
		if _, err := term.Plain(); err != nil {
			t.Fatalf("plain dump failed: %v", err)
		}
		if _, err := term.Row(0); err != nil {
			t.Fatalf("row read failed: %v", err)
		}
	})
}
