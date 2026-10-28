package main

import (
	"fmt"
	"net"
	"strings"
	"testing"
	"time"
)

func trimLines(text string) []string {
	var out []string
	for _, l := range strings.Split(text, "\n") {
		if strings.Contains(l, " hsh |") {
			continue
		}
		out = append(out, strings.TrimRight(l, " \r"))
	}
	for len(out) > 0 && out[len(out)-1] == "" {
		out = out[:len(out)-1]
	}
	return out
}

func TestPassthroughPauseLeavesTheClientTerminalIdenticalToTheEmulator(t *testing.T) {
	s, err := newSession(serveOptions{name: "unit", pass: true, args: []string{"cat"}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.shutdown()
	go s.pumpOutput()
	go s.renderLoop()

	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()
	ft := newFakeTerminal(t, client, 100, 30)

	s.mu.Lock()
	s.client = server
	s.resizeLocked(100, 30)
	s.attachPassLocked()
	s.mu.Unlock()

	feed := func(prefix string, n int) {
		for i := 1; i <= n; i++ {
			s.ptmx.WriteString(fmt.Sprintf("%s %d\n", prefix, i))
		}
		time.Sleep(400 * time.Millisecond)
	}
	feed("before", 40)

	s.mu.Lock()
	s.addDenialLocked("a.example")
	s.inputLocked([]byte{0})
	paused := s.paused
	s.mu.Unlock()
	time.Sleep(200 * time.Millisecond)
	if !paused {
		t.Fatal("opening the dialog did not pause the write path")
	}
	rows := ft.screen(t)
	if !strings.Contains(strings.Join(rows, "\n"), "Proxy requests awaiting review") {
		t.Fatalf("dialog not drawn on the client terminal")
	}
	if !strings.Contains(rows[len(rows)-1], "REVIEW") {
		t.Fatalf("status line did not switch to review: %q", rows[len(rows)-1])
	}

	feed("while-paused", 30)
	s.mu.Lock()
	plain, _ := s.plainLocked()
	s.mu.Unlock()
	if strings.Contains(plain, "while-paused 30") {
		t.Fatal("the emulator advanced while the write path was paused")
	}
	if strings.Contains(strings.Join(ft.screen(t), "\n"), "while-paused 30") {
		t.Fatal("output reached the client terminal while paused")
	}

	s.mu.Lock()
	s.inputLocked([]byte{0x1b})
	s.mu.Unlock()
	time.Sleep(600 * time.Millisecond)

	s.mu.Lock()
	plain, _ = s.plainLocked()
	stillPaused := s.paused
	s.mu.Unlock()
	if stillPaused {
		t.Fatal("closing the dialog did not resume")
	}
	want := trimLines(plain)
	got := trimLines(strings.Join(ft.screen(t), "\n"))
	if !strings.Contains(strings.Join(want, "\n"), "while-paused 30") {
		t.Fatal("output produced while paused was lost")
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("client terminal differs from the emulator after close\nclient (%d lines):\n%s\n\nemulator (%d lines):\n%s",
			len(got), strings.Join(got, "\n"), len(want), strings.Join(want, "\n"))
	}
	if !strings.Contains(strings.Join(want, "\n"), "before 1\n") {
		t.Fatal("scrollback lost: the first line is gone")
	}
}

func TestPassthroughStatusRowSurvivesScrollClearAndAltScreen(t *testing.T) {
	s, err := newSession(serveOptions{name: "unit", pass: true, args: []string{"bash", "-c", `stty -echo; while IFS= read -r l; do eval "$l"; done`}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.shutdown()
	go s.pumpOutput()
	go s.renderLoop()

	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()
	ft := newFakeTerminal(t, client, 100, 30)
	s.mu.Lock()
	s.client = server
	s.resizeLocked(100, 30)
	s.attachPassLocked()
	s.mu.Unlock()

	run := func(cmd string) []string {
		s.ptmx.WriteString(cmd + "\n")
		time.Sleep(500 * time.Millisecond)
		return ft.screen(t)
	}
	statusOK := func(stage string, rows []string) {
		t.Helper()
		if len(rows) < 30 || !strings.Contains(rows[len(rows)-1], "nothing to review") {
			t.Errorf("%s: status row missing or overwritten: %q", stage, rows[len(rows)-1])
		}
	}

	rows := run("seq 1 100")
	statusOK("after scrolling output", rows)
	if rows[len(rows)-3] != "100" {
		t.Errorf("last output line is not directly above the cursor row and status row: %q", rows[len(rows)-3])
	}
	if !strings.Contains("\n"+strings.Join(rows, "\n")+"\n", "\n1\n") {
		t.Errorf("scrollback lost under a reserved row")
	}

	rows = run("clear; echo after-clear")
	statusOK("after clear", rows)
	if !strings.Contains(strings.Join(rows, "\n"), "after-clear") {
		t.Errorf("output after clear missing")
	}

	rows = run(`printf '\033[?1049h\033[H\033[2Jalt-app'`)
	statusOK("in the alternate screen", rows)
	if !strings.Contains(strings.Join(rows, "\n"), "alt-app") {
		t.Errorf("alternate screen content missing")
	}

	rows = run(`printf '\033[?1049l'; echo back`)
	statusOK("after leaving the alternate screen", rows)

	rows = run(`printf '\033[r'; seq 1 60 >/dev/null; printf 'x\n%.0s' $(seq 1 40); echo end-of-burst`)
	statusOK("after a child reset of the scroll region", rows)
	if !strings.Contains(rows[len(rows)-3], "end-of-burst") {
		t.Errorf("child scroll-region reset let output reach the status row: %q", rows[len(rows)-3])
	}
}
