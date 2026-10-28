package relay

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/creack/pty"

	"hsh/internal/vt"
)

type fakeSource struct {
	mu      sync.Mutex
	pending []Item
	allowed []string
	denied  []string
	failOn  string
}

func (s *fakeSource) Pending() ([]Item, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Item(nil), s.pending...), nil
}

func (s *fakeSource) drop(host string) {
	kept := s.pending[:0]
	for _, it := range s.pending {
		if it.Host != host {
			kept = append(kept, it)
		}
	}
	s.pending = kept
}

func (s *fakeSource) Allow(host string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if host == s.failOn {
		return fmt.Errorf("config is read-only")
	}
	s.allowed = append(s.allowed, host)
	s.drop(host)
	return nil
}

func (s *fakeSource) Deny(host string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.denied = append(s.denied, host)
	s.drop(host)
	return nil
}

func (s *fakeSource) set(items ...Item) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pending = items
}

func (s *fakeSource) results() (allowed, denied []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.allowed...), append([]string(nil), s.denied...)
}

type userTerminal struct {
	mu   sync.Mutex
	term vt.Emulator
}

func (u *userTerminal) Write(p []byte) (int, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.term.Write(p)
	return len(p), nil
}

func (u *userTerminal) lines() []string {
	u.mu.Lock()
	defer u.mu.Unlock()
	text, _ := u.term.Plain()
	var out []string
	for _, l := range strings.Split(text, "\n") {
		out = append(out, strings.TrimRight(l, " \r"))
	}
	return out
}

func (u *userTerminal) text() string { return strings.Join(u.lines(), "\n") }

type harness struct {
	t      *testing.T
	relay  *Relay
	master *os.File
	user   *userTerminal
	keys   *io.PipeWriter
	src    *fakeSource
	done   chan struct{}
}

const (
	userCols = 100
	userRows = 30
)

func newHarness(t *testing.T, status bool, pollEvery time.Duration) *harness {
	t.Helper()
	if !vt.Available() {
		t.Skip("built without GOEXPERIMENT=simd")
	}
	rows := userRows
	if status {
		rows--
	}
	cmd := exec.Command("bash", "-c", `stty -echo; while IFS= read -r l; do eval "$l"; done`)
	master, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: userCols, Rows: uint16(rows)})
	if err != nil {
		t.Fatal(err)
	}
	ut, err := vt.New(vt.Options{Cols: userCols, Rows: userRows, Scrollback: 1000})
	if err != nil {
		t.Fatal(err)
	}
	user := &userTerminal{term: ut}
	pr, pw := io.Pipe()
	src := &fakeSource{}
	key, _ := ParseKey("ctrl-space")
	r, err := New(Config{In: pr, Out: user, Pty: master, Cols: userCols, Rows: userRows,
		StatusLine: status, Key: key, Source: src, Name: "unit", PollEvery: pollEvery})
	if err != nil {
		t.Fatal(err)
	}
	h := &harness{t: t, relay: r, master: master, user: user, keys: pw, src: src, done: make(chan struct{})}
	go func() {
		r.Run()
		close(h.done)
	}()
	t.Cleanup(func() {
		pw.Close()
		_ = cmd.Process.Kill()
		select {
		case <-h.done:
		case <-time.After(3 * time.Second):
		}
		master.Close()
	})
	return h
}

func (h *harness) run(cmd string) {
	h.master.WriteString(cmd + "\n")
	time.Sleep(450 * time.Millisecond)
}

func (h *harness) press(s string) {
	h.keys.Write([]byte(s))
	time.Sleep(250 * time.Millisecond)
}

func (h *harness) waitFor(what string, cond func() bool) {
	h.t.Helper()
	end := time.Now().Add(5 * time.Second)
	for time.Now().Before(end) {
		if cond() {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	h.t.Fatalf("timed out waiting for %s; screen:\n%s", what, h.user.text())
}

func (h *harness) statusRow() string {
	l := h.user.lines()
	return l[len(l)-1]
}

func (h *harness) emulatorLines() []string {
	h.relay.mu.Lock()
	defer h.relay.mu.Unlock()
	text, _ := h.relay.term.Plain()
	var out []string
	for _, l := range strings.Split(text, "\n") {
		out = append(out, strings.TrimRight(l, " \r"))
	}
	return out
}

func withoutStatus(lines []string) []string {
	var out []string
	for _, l := range lines {
		if strings.Contains(l, " hsh |") {
			continue
		}
		out = append(out, l)
	}
	for len(out) > 0 && out[len(out)-1] == "" {
		out = out[:len(out)-1]
	}
	for len(out) > 0 && out[0] == "" {
		out = out[1:]
	}
	return out
}

func TestPauseKeepsTheUserTerminalIdenticalToTheEmulator(t *testing.T) {
	h := newHarness(t, true, time.Hour)
	for i := 1; i <= 40; i++ {
		h.master.WriteString(fmt.Sprintf("echo before-%d\n", i))
	}
	time.Sleep(500 * time.Millisecond)

	h.press("\x00")
	h.relay.mu.Lock()
	paused := h.relay.paused
	h.relay.mu.Unlock()
	if !paused {
		t.Fatal("opening the dialog did not pause the output")
	}
	if !strings.Contains(h.user.text(), "Proxy requests awaiting review") {
		t.Fatal("dialog not drawn")
	}
	if !strings.Contains(h.statusRow(), "REVIEW") {
		t.Fatalf("status row: %q", h.statusRow())
	}
	for i := 1; i <= 30; i++ {
		h.master.WriteString(fmt.Sprintf("echo while-paused-%d\n", i))
	}
	time.Sleep(500 * time.Millisecond)
	if strings.Contains(h.user.text(), "while-paused-30") || strings.Contains(strings.Join(h.emulatorLines(), "\n"), "while-paused-30") {
		t.Fatal("output advanced while paused")
	}

	h.press("\x1b")
	h.waitFor("held output to be delivered", func() bool { return strings.Contains(h.user.text(), "while-paused-30") })
	time.Sleep(200 * time.Millisecond)
	want := withoutStatus(h.emulatorLines())
	got := withoutStatus(h.user.lines())
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("user terminal differs from the emulator\nuser (%d):\n%s\n\nemulator (%d):\n%s", len(got), strings.Join(got, "\n"), len(want), strings.Join(want, "\n"))
	}
	if !strings.Contains(strings.Join(want, "\n"), "before-1\n") {
		t.Fatal("scrollback lost")
	}
}

func TestStatusRowSurvivesScrollClearAltScreenAndRegionReset(t *testing.T) {
	h := newHarness(t, true, time.Hour)
	ok := func(stage string) {
		t.Helper()
		if !strings.Contains(h.statusRow(), "nothing to review") {
			t.Errorf("%s: status row overwritten: %q", stage, h.statusRow())
		}
	}
	h.run("seq 1 100")
	ok("after scrolling output")
	l := h.user.lines()
	if l[len(l)-3] != "100" {
		t.Errorf("last output line is not above the cursor row: %q", l[len(l)-3])
	}
	h.run("clear; echo after-clear")
	ok("after clear")
	h.run(`printf '\033[?1049h\033[H\033[2Jalt-app'`)
	ok("in the alternate screen")
	if !strings.Contains(h.user.text(), "alt-app") {
		t.Error("alternate screen content missing")
	}
	h.run(`printf '\033[?1049l'; echo back`)
	ok("after leaving the alternate screen")
	h.run(`printf '\033[r'; printf 'x\n%.0s' $(seq 1 40); echo end-of-burst`)
	ok("after the child reset the scroll region")
	l = h.user.lines()
	if !strings.Contains(l[len(l)-3], "end-of-burst") {
		t.Errorf("scroll region reset let output reach the status row: %q", l[len(l)-3])
	}
}

func TestReviewDialogAppliesDecisionsThroughTheSource(t *testing.T) {
	h := newHarness(t, true, 40*time.Millisecond)
	h.run("echo hello")
	h.src.set(Item{Host: "a.example", Count: 2, Last: time.Now()}, Item{Host: "b.example", Count: 1, Last: time.Now()})
	h.waitFor("the notification", func() bool { return strings.Contains(h.statusRow(), "2 proxy requests need review") })

	h.press("\x00")
	text := h.user.text()
	for _, want := range []string{"Proxy requests awaiting review", "> a.example", "b.example", "x2"} {
		if !strings.Contains(text, want) {
			t.Errorf("dialog missing %q:\n%s", want, text)
		}
	}
	h.press("\x1b[B")
	h.press("a")
	h.press("d")
	allowed, denied := h.src.results()
	if fmt.Sprint(allowed) != "[b.example]" || fmt.Sprint(denied) != "[a.example]" {
		t.Fatalf("allowed=%v denied=%v", allowed, denied)
	}
	h.press("\x1b")
	h.waitFor("the dialog to close", func() bool { return !strings.Contains(h.user.text(), "awaiting review") })
	h.waitFor("an idle status row", func() bool { return strings.Contains(h.statusRow(), "nothing to review") })
	if !strings.Contains(h.user.text(), "hello") {
		t.Error("screen not restored under the dialog")
	}
}

func TestSourceErrorsAreShownAndTheItemStaysPending(t *testing.T) {
	h := newHarness(t, true, 40*time.Millisecond)
	h.src.failOn = "a.example"
	h.src.set(Item{Host: "a.example", Count: 1, Last: time.Now()})
	h.waitFor("the notification", func() bool { return strings.Contains(h.statusRow(), "1 proxy request needs review") })
	h.press("\x00")
	h.press("a")
	if !strings.Contains(h.user.text(), "error: a.example: config is read-only") {
		t.Fatalf("error not shown:\n%s", h.user.text())
	}
	allowed, _ := h.src.results()
	if len(allowed) != 0 {
		t.Fatalf("allowed despite the error: %v", allowed)
	}
	h.press("\x00")
	h.waitFor("the dialog to close", func() bool { return !strings.Contains(h.user.text(), "awaiting review") })
	if !strings.Contains(h.statusRow(), "1 proxy request needs review") {
		t.Fatalf("item should still be pending: %q", h.statusRow())
	}
}

func TestInBandHintTriggersAPollAndIsStripped(t *testing.T) {
	h := newHarness(t, true, time.Hour)
	h.run("echo first")
	if !strings.Contains(h.statusRow(), "nothing to review") {
		t.Fatalf("status row: %q", h.statusRow())
	}
	h.src.set(Item{Host: "late.example", Count: 1, Last: time.Now()})
	time.Sleep(300 * time.Millisecond)
	if strings.Contains(h.statusRow(), "needs review") {
		t.Fatal("poll interval was not respected")
	}
	h.run(`printf '\033]%s;look;ignored-payload\a' 7777`)
	h.waitFor("the hint to poll", func() bool { return strings.Contains(h.statusRow(), "1 proxy request needs review") })
	if strings.Contains(h.user.text(), "ignored-payload") {
		t.Fatal("the hint reached the user's terminal")
	}
}

func TestKittyEncodedKeyOpensTheDialog(t *testing.T) {
	h := newHarness(t, true, time.Hour)
	h.run("echo hi")
	h.press("\x1b[32;5u")
	if !strings.Contains(h.user.text(), "Proxy requests awaiting review") {
		t.Fatal("the kitty-encoded Ctrl+Space did not open the dialog")
	}
}

func TestDialogWithoutAStatusRow(t *testing.T) {
	h := newHarness(t, false, time.Hour)
	h.run("seq 1 40")
	h.press("\x00")
	if !strings.Contains(h.user.text(), "Proxy requests awaiting review") {
		t.Fatal("dialog not drawn without a status row")
	}
	h.press("\x1b")
	h.waitFor("the dialog to close", func() bool { return !strings.Contains(h.user.text(), "awaiting review") })
	l := h.user.lines()
	if !strings.Contains(strings.Join(l, "\n"), "40") {
		t.Fatal("screen not restored")
	}
}

func TestResizeResizesThePtyAndEmulator(t *testing.T) {
	h := newHarness(t, true, time.Hour)
	h.relay.Resize(80, 20)
	rows, cols, err := pty.Getsize(h.master)
	if err != nil {
		t.Fatal(err)
	}
	if cols != 80 || rows != 19 {
		t.Fatalf("pty size %dx%d, want 80x19", cols, rows)
	}
	if c, r := h.relay.ContentSize(); c != 80 || r != 19 {
		t.Fatalf("content size %dx%d", c, r)
	}
}

func TestParseKey(t *testing.T) {
	cases := map[string]byte{"ctrl-space": 0, "": 0, "ctrl-a": 1, "ctrl-z": 26, "ctrl-\\": 0x1c, "ctrl-]": 0x1d}
	for name, want := range cases {
		k, err := ParseKey(name)
		if err != nil || k.Byte != want {
			t.Errorf("%q: %v %v", name, k, err)
		}
	}
	for _, bad := range []string{"a", "ctrl-1", "ctrl-i", "ctrl-m", "ctrl-h", "alt-x", "ctrl-spacebar"} {
		if _, err := ParseKey(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestFilters(t *testing.T) {
	var hf hintFilter
	var got []byte
	hints := 0
	for _, in := range []string{"a\x1b]77", "77;look;x\x07b\x1b]0;title\x07"} {
		c, n := hf.feed([]byte(in))
		got = append(got, c...)
		hints += n
	}
	if string(got) != "ab\x1b]0;title\x07" || hints != 1 {
		t.Errorf("hint filter: %q %d", got, hints)
	}
	var rf regionFilter
	cases := []struct{ in, want string }{
		{"a\x1b[rb", "a\x1b[1;23rb"},
		{"\x1b[5r", "\x1b[5;23r"},
		{"\x1b[2;10r", "\x1b[2;10r"},
		{"\x1b[31m", "\x1b[31m"},
		{"\x1b[?1049r", "\x1b[?1049r"},
	}
	for _, c := range cases {
		if out := string(rf.feed([]byte(c.in), 23)); out != c.want {
			t.Errorf("region %q: got %q want %q", c.in, out, c.want)
		}
	}
	rf = regionFilter{}
	out := string(rf.feed([]byte("x\x1b["), 23)) + string(rf.feed([]byte("2;"), 23)) + string(rf.feed([]byte("10r"), 23))
	if out != "x\x1b[2;10r" {
		t.Errorf("split region: %q", out)
	}
}
