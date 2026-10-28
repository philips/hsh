package vt

import (
	"strings"
	"testing"
)

func TestWithoutBackendReportsUnavailable(t *testing.T) {
	if Available() {
		t.Skip("emulator backend present")
	}
	if _, err := New(Options{Cols: 80, Rows: 24}); err != ErrUnavailable {
		t.Fatalf("got %v", err)
	}
}

func newEmu(t *testing.T, cols, rows int) Emulator {
	t.Helper()
	if !Available() {
		t.Skip("built without GOEXPERIMENT=simd")
	}
	e, err := New(Options{Cols: cols, Rows: rows, Scrollback: 1000})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(e.Close)
	return e
}

func TestWriteAndPlain(t *testing.T) {
	e := newEmu(t, 40, 5)
	e.Write([]byte("hello \x1b[1;32mgreen\x1b[0m\r\nline2"))
	got, err := e.Plain()
	if err != nil {
		t.Fatal(err)
	}
	if got != "hello green\nline2" {
		t.Fatalf("got %q", got)
	}
	if x, y := e.Cursor(); x != 5 || y != 1 {
		t.Fatalf("cursor %d,%d", x, y)
	}
}

func TestGroundTracksSplitSequences(t *testing.T) {
	e := newEmu(t, 40, 5)
	if !e.Ground() {
		t.Fatal("fresh terminal not at ground")
	}
	e.Write([]byte("abc\x1b[3"))
	if e.Ground() {
		t.Fatal("mid-CSI reported as ground")
	}
	e.Write([]byte("1mred"))
	if !e.Ground() {
		t.Fatal("completed sequence not at ground")
	}
}

func TestReplayIncludesHistory(t *testing.T) {
	e := newEmu(t, 40, 5)
	for i := 1; i <= 30; i++ {
		e.Write([]byte("line " + string(rune('A'+i%26)) + "\r\n"))
	}
	replay, err := e.Replay()
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(replay), "\n"); n < 25 {
		t.Fatalf("replay has %d line breaks; scrollback missing", n)
	}
}

func TestRowIsOneVisibleRow(t *testing.T) {
	e := newEmu(t, 40, 5)
	e.Write([]byte("first\r\n\x1b[31msecond\x1b[0m\r\nthird"))
	row, err := e.Row(1)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(row, "second") || strings.Contains(row, "first") || strings.Contains(row, "third") {
		t.Fatalf("row 1 = %q", row)
	}
	if _, err := e.Row(5); err == nil {
		t.Fatal("out-of-range row accepted")
	}
}

func TestResize(t *testing.T) {
	e := newEmu(t, 40, 5)
	e.Write([]byte("abc"))
	if err := e.Resize(20, 10); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Row(9); err != nil {
		t.Fatalf("row 9 after resize: %v", err)
	}
}
