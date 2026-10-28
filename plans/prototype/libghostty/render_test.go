package main

import (
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"code.selman.me/hauntty/libghostty"
)

type fakeTerminal struct {
	mu   sync.Mutex
	term *libghostty.Terminal
}

func newFakeTerminal(t *testing.T, conn net.Conn, cols, rows uint16) *fakeTerminal {
	term, err := libghostty.NewTerminal(libghostty.WithSize(cols, rows))
	if err != nil {
		t.Fatal(err)
	}
	ft := &fakeTerminal{term: term}
	go func() {
		for {
			kind, payload, err := readFrame(conn)
			if err != nil {
				return
			}
			if kind == frameOutput {
				ft.mu.Lock()
				ft.term.VTWrite(payload)
				ft.mu.Unlock()
			}
		}
	}()
	return ft
}

func (ft *fakeTerminal) screen(t *testing.T) []string {
	time.Sleep(50 * time.Millisecond)
	ft.mu.Lock()
	defer ft.mu.Unlock()
	f, err := libghostty.NewFormatter(ft.term, libghostty.WithFormatterFormat(libghostty.FormatterFormatPlain))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	text, err := f.FormatString()
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(text, "\n")
}

func TestStatusLineAndDialogOnTheClientScreen(t *testing.T) {
	s, err := newSession(serveOptions{name: "unit", args: []string{"cat"}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.shutdown()
	go s.pumpOutput()

	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()
	ft := newFakeTerminal(t, client, 100, 30)

	s.mu.Lock()
	s.client = server
	s.resizeLocked(100, 30)
	s.term.VTWrite([]byte("line one\r\nline two\r\n"))
	s.renderLocked()
	s.mu.Unlock()

	rows := ft.screen(t)
	if !strings.Contains(rows[0], "line one") || !strings.Contains(rows[1], "line two") {
		t.Fatalf("session content missing: %q", rows[:3])
	}
	if last := rows[29]; !strings.Contains(last, "nothing to review") {
		t.Fatalf("idle status line: %q", last)
	}

	s.mu.Lock()
	s.addDenialLocked("a.example")
	s.addDenialLocked("b.example")
	s.addDenialLocked("b.example")
	s.mu.Unlock()
	rows = ft.screen(t)
	if last := rows[29]; !strings.Contains(last, "2 proxy requests need review") {
		t.Fatalf("notification status line: %q", last)
	}
	for _, r := range rows[:29] {
		if strings.Contains(r, "a.example") {
			t.Fatalf("dialog visible before Ctrl+Space: %q", r)
		}
	}

	s.mu.Lock()
	s.inputLocked([]byte{0})
	s.mu.Unlock()
	rows = ft.screen(t)
	t.Log("\n" + strings.Join(rows, "\n"))
	joined := strings.Join(rows[:29], "\n")
	for _, want := range []string{"Proxy requests awaiting review", "> a.example", "b.example", "x2"} {
		if !strings.Contains(joined, want) {
			t.Errorf("dialog missing %q", want)
		}
	}
	if !strings.Contains(rows[0], "line one") {
		t.Errorf("content outside the dialog was not preserved: %q", rows[0])
	}
	if last := rows[29]; !strings.Contains(last, "REVIEW") {
		t.Errorf("review status line: %q", last)
	}

	s.mu.Lock()
	s.inputLocked([]byte{0})
	s.mu.Unlock()
	rows = ft.screen(t)
	if strings.Contains(strings.Join(rows[:29], "\n"), "awaiting review") {
		t.Errorf("dialog still on screen after closing")
	}
	if !strings.Contains(rows[1], "line two") {
		t.Errorf("screen not restored under the dialog: %q", rows[1])
	}
	if last := rows[29]; !strings.Contains(last, "2 proxy requests need review") {
		t.Errorf("status line after close: %q", last)
	}
}
