package relay

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/creack/pty"

	"hsh/internal/vt"
)

type fakeModel struct {
	mu    *sync.Mutex
	log   *[]string
	title string
}

func (m fakeModel) note(s string) {
	m.mu.Lock()
	*m.log = append(*m.log, s)
	m.mu.Unlock()
}

func (m fakeModel) Init() tea.Cmd {
	return func() tea.Msg {
		time.Sleep(100 * time.Millisecond)
		return fakeTick{}
	}
}

type fakeTick struct{}

func (m fakeModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.note(fmt.Sprintf("size %dx%d", msg.Width, msg.Height))
	case fakeTick:
		m.note("tick")
	case tea.KeyMsg:
		m.note("key " + msg.String())
		if s := msg.String(); s == "q" || s == "esc" {
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m fakeModel) View() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return "FAKE REVIEW SCREEN\n" + strings.Join(*m.log, "|")
}

func (h *harness) modelLog() string {
	h.logMu.Lock()
	defer h.logMu.Unlock()
	return strings.Join(h.log, "|")
}

type userTerminal struct {
	mu   sync.Mutex
	term vt.Emulator
	raw  []byte
}

func (u *userTerminal) Write(p []byte) (int, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.term.Write(p)
	u.raw = append(u.raw, p...)
	return len(p), nil
}

func (u *userTerminal) lastPasteMode() string {
	u.mu.Lock()
	defer u.mu.Unlock()
	on := strings.LastIndex(string(u.raw), "\x1b[?2004h")
	off := strings.LastIndex(string(u.raw), "\x1b[?2004l")
	switch {
	case on > off:
		return "on"
	case off > on:
		return "off"
	}
	return "unset"
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

var csiRE = regexp.MustCompile("\x1b\\[[0-9;?:]*[A-Za-z]")

func (u *userTerminal) screen() []string {
	u.mu.Lock()
	defer u.mu.Unlock()
	var out []string
	for y := 0; y < userRows; y++ {
		row, _ := u.term.Row(y)
		out = append(out, strings.TrimRight(csiRE.ReplaceAllString(row, ""), " \r"))
	}
	return out
}

func (u *userTerminal) text() string { return strings.Join(u.lines(), "\n") }

type harness struct {
	t       *testing.T
	relay   *Relay
	master  *os.File
	user    *userTerminal
	keys    *io.PipeWriter
	done    chan struct{}
	logMu   sync.Mutex
	log     []string
	pending atomic.Int64
	held    atomic.Int64
	polls   atomic.Int64
}

const (
	userCols = 100
	userRows = 30
)

func newHarness(t *testing.T, status bool, pollEvery time.Duration) *harness {
	t.Helper()
	return newHarnessWith(t, status, pollEvery, `stty -echo; while IFS= read -r l; do eval "$l"; done`, "")
}

func newHarnessWith(t *testing.T, status bool, pollEvery time.Duration, script, detachKeys string) *harness {
	t.Helper()
	if !vt.Available() {
		t.Skip("built without GOEXPERIMENT=simd")
	}
	rows := userRows
	if status {
		rows--
	}
	cmd := exec.Command("bash", "-c", script)
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
	key, _ := ParseKey("ctrl-space")
	h := &harness{t: t, master: master, user: user, keys: pw, done: make(chan struct{})}
	r, err := New(Config{In: pr, Out: user, Pty: master, Cols: userCols, Rows: userRows,
		StatusLine: status, Key: key, DetachKeys: detachKeys, Name: "unit", PollEvery: pollEvery,
		Count: func() (int, int, error) {
			h.polls.Add(1)
			return int(h.pending.Load()), int(h.held.Load()), nil
		},
		Overlay: func(cols, rows int) tea.Model {
			return fakeModel{mu: &h.logMu, log: &h.log}
		}})
	if err != nil {
		t.Fatal(err)
	}
	h.relay = r
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
		t.Fatal("opening the overlay did not pause the output")
	}
	if !strings.Contains(h.user.text(), "FAKE REVIEW SCREEN") {
		t.Fatal("overlay not drawn")
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

	h.press("q")
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

func TestOverlayTakesOverTheWholeScreenReceivesKeysAndRestores(t *testing.T) {
	h := newHarness(t, true, 40*time.Millisecond)
	h.run("seq 1 60")
	h.run("echo visible-before-overlay")
	h.pending.Store(2)
	h.waitFor("the notification", func() bool { return strings.Contains(h.statusRow(), "2 proxy requests need review") })

	h.press("\x00")
	lines := h.user.screen()
	if lines[0] != "FAKE REVIEW SCREEN" {
		t.Fatalf("the overlay does not start at the top of the screen: %q", lines[0])
	}
	for _, l := range lines[:len(lines)-1] {
		if strings.Contains(l, "visible-before-overlay") || strings.TrimSpace(l) == "60" {
			t.Fatalf("session text still visible under the full-screen overlay: %q", l)
		}
	}
	if !strings.Contains(h.statusRow(), "REVIEW") {
		t.Fatalf("status row: %q", h.statusRow())
	}

	h.press("j")
	h.press("\x1b[B")
	h.press(" ")
	h.press("\r")
	h.waitFor("the model to have seen the keys", func() bool {
		return strings.Contains(h.modelLog(), "key j|key down|key  |key enter")
	})
	h.waitFor("a command's message to arrive", func() bool { return strings.Contains(h.modelLog(), "tick") })
	if !strings.Contains(h.user.text(), "tick") {
		t.Error("the view was not redrawn after a command's message")
	}
	if !strings.Contains(h.modelLog(), "size 100x29") {
		t.Errorf("the model never got the screen size: %s", h.modelLog())
	}

	h.press("q")
	h.waitFor("the overlay to close", func() bool { return !strings.Contains(h.user.text(), "FAKE REVIEW SCREEN") })
	h.waitFor("the screen to be restored", func() bool { return strings.Contains(h.user.text(), "visible-before-overlay") })
	h.waitFor("the status line to count again", func() bool { return strings.Contains(h.statusRow(), "2 proxy requests need review") })
}

func TestTheKeyAndEscapeAlsoCloseTheOverlayAndTrailingBytesGoToTheSession(t *testing.T) {
	h := newHarness(t, true, time.Hour)
	h.run("echo hi")
	h.press("\x00")
	if !strings.Contains(h.user.text(), "FAKE REVIEW SCREEN") {
		t.Fatal("not open")
	}
	h.press("\x00")
	h.waitFor("the key to close the overlay", func() bool { return !strings.Contains(h.user.text(), "FAKE REVIEW SCREEN") })

	h.press("\x00")
	h.waitFor("reopen", func() bool { return strings.Contains(h.user.text(), "FAKE REVIEW SCREEN") })
	h.press("\x1b")
	h.waitFor("Esc to close", func() bool { return !strings.Contains(h.user.text(), "FAKE REVIEW SCREEN") })

	h.press("\x00\x00echo after-$((6*7))\r")
	h.waitFor("typing after the key to reach the session", func() bool { return strings.Contains(h.user.text(), "after-42") })
}

func TestInBandHintTriggersAPollAndIsStripped(t *testing.T) {
	h := newHarness(t, true, time.Hour)
	h.run("echo first")
	if !strings.Contains(h.statusRow(), "nothing to review") {
		t.Fatalf("status row: %q", h.statusRow())
	}
	before := h.polls.Load()
	h.pending.Store(1)
	time.Sleep(300 * time.Millisecond)
	if h.polls.Load() != before || strings.Contains(h.statusRow(), "needs review") {
		t.Fatal("poll interval was not respected")
	}
	h.run(`printf '\033]%s;look;ignored-payload\a' 7777`)
	h.waitFor("the hint to poll", func() bool { return strings.Contains(h.statusRow(), "1 proxy request needs review") })
	if strings.Contains(h.user.text(), "ignored-payload") {
		t.Fatal("the hint reached the user's terminal")
	}
}

func TestKittyEncodedKeyOpensTheOverlay(t *testing.T) {
	h := newHarness(t, true, time.Hour)
	h.run("echo hi")
	h.press("\x1b[32;5u")
	if !strings.Contains(h.user.text(), "FAKE REVIEW SCREEN") {
		t.Fatal("the kitty-encoded Ctrl+Space did not open the overlay")
	}
	h.press("\x1b[27u")
	h.waitFor("the kitty-encoded Esc to close it", func() bool { return !strings.Contains(h.user.text(), "FAKE REVIEW SCREEN") })
}

func TestOverlayWithoutAStatusRow(t *testing.T) {
	h := newHarness(t, false, time.Hour)
	h.run("seq 1 40")
	h.press("\x00")
	lines := h.user.screen()
	if lines[0] != "FAKE REVIEW SCREEN" {
		t.Fatalf("overlay not drawn: %q", lines[0])
	}
	h.press("q")
	h.waitFor("the overlay to close", func() bool { return !strings.Contains(h.user.text(), "FAKE REVIEW SCREEN") })
	if !strings.Contains(strings.Join(h.user.lines(), "\n"), "40") {
		t.Fatal("screen not restored")
	}
}

func TestResizeResizesThePtyEmulatorAndTheOpenOverlay(t *testing.T) {
	h := newHarness(t, true, time.Hour)
	h.press("\x00")
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
	h.waitFor("the model to learn the new size", func() bool { return strings.Contains(h.modelLog(), "size 80x19") })
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

func TestParseInput(t *testing.T) {
	cases := []struct{ in, want string }{
		{"j", "j"},
		{"\x1b[A\x1b[B\x1b[C\x1b[D", "up down right left"},
		{"\x1bOA\x1bOB", "up down"},
		{"\r", "enter"},
		{"\t", "tab"},
		{" ", " "},
		{"\x7f", "backspace"},
		{"\x03", "ctrl+c"},
		{"\x1b", "esc"},
		{"\x1b[27u", "esc"},
		{"\x1b[13u", "enter"},
		{"\x1b[113u", "q"},
		{"\x1b[113;2u", "Q"},
		{"\x1b[99;5u", "ctrl+c"},
		{"\x1b[27;5;99~", ""},
		{"\x1b[3~", "delete"},
		{"\x1b[5~\x1b[6~", "pgup pgdown"},
		{"\x1b[200~hi\x1b[201~", "h i"},
		{"héllo", "h é l l o"},
		{"\x1b[93;5:3u", ""},
	}
	for _, c := range cases {
		var got []string
		for _, m := range parseInput([]byte(c.in)) {
			got = append(got, m.String())
		}
		if strings.Join(got, " ") != c.want {
			t.Errorf("%q: got %q want %q", c.in, strings.Join(got, " "), c.want)
		}
	}
}

func TestEnhancedKeyEncodingsAreRecognised(t *testing.T) {
	cases := []struct {
		in      string
		code    rune
		width   int
		release bool
		ok      bool
	}{
		{"\x1b[93;5u", ']', 7, false, true},
		{"\x1b[93;5:1u", ']', 9, false, true},
		{"\x1b[93;5:3u", ']', 9, true, true},
		{"\x1b[93;133u", ']', 9, false, true},
		{"\x1b[93:93;5u", ']', 10, false, true},
		{"\x1b[27;5;93~", ']', 10, false, true},
		{"\x1b[32;5u", ' ', 7, false, true},
		{"\x1b[93;1u", 0, 0, false, false},
		{"\x1b[93;6u", 0, 0, false, false},
		{"\x1b[93;7u", 0, 0, false, false},
		{"\x1b[A", 0, 0, false, false},
		{"\x1b[93u", 0, 0, false, false},
	}
	for _, c := range cases {
		ev, ok := decodeCtrl([]byte(c.in))
		if ok != c.ok || (ok && (ev.code != c.code || ev.width != c.width || ev.release != c.release)) {
			t.Errorf("%q: got %+v ok=%v", c.in, ev, ok)
		}
	}
}

func TestTranslateKeysRewritesOnlyTheConfiguredKeys(t *testing.T) {
	keys := ParseDetachKeys("ctrl-],ctrl-a,d")
	if len(keys) != 2 {
		t.Fatalf("parsed %d keys from the detach spec", len(keys))
	}
	cases := []struct{ in, want string }{
		{"ab", "ab"},
		{"x\x1b[93;5uy", "x\x1dy"},
		{"\x1b[27;5;93~", "\x1d"},
		{"\x1b[93;5:3u", ""},
		{"\x1b[97;5u", "\x01"},
		{"\x1b[98;5u", "\x1b[98;5u"},
		{"\x1b[A\x1b[93;5u", "\x1b[A\x1d"},
	}
	for _, c := range cases {
		if got := string(translateKeys([]byte(c.in), keys)); got != c.want {
			t.Errorf("%q: got %q want %q", c.in, got, c.want)
		}
	}
	if got := string(translateKeys([]byte("\x1b[93;5u"), nil)); got != "\x1b[93;5u" {
		t.Errorf("no keys configured changed the input: %q", got)
	}
}

func TestDetachKeyInKittyEncodingReachesTheChildAsTheLegacyByte(t *testing.T) {
	file := t.TempDir() + "/got"
	h := newHarnessWith(t, true, time.Hour, "stty raw -echo; head -c 3 > "+file, "ctrl-]")
	time.Sleep(300 * time.Millisecond)
	h.press("\x1b[93;5uxy")
	h.waitFor("the child to read three bytes", func() bool {
		b, _ := os.ReadFile(file)
		return len(b) == 3
	})
	b, _ := os.ReadFile(file)
	if string(b) != "\x1dxy" {
		t.Fatalf("child received %q, want the legacy Ctrl+] byte then xy", b)
	}
}

func TestInputParserBracketedPasteAcrossReads(t *testing.T) {
	var ip inputParser
	feed := func(parts ...string) []string {
		var got []string
		for _, part := range parts {
			for _, m := range ip.feed([]byte(part)) {
				got = append(got, m.String())
			}
		}
		return got
	}
	if got := feed("\x1b[200~hello\x1b[201~"); strings.Join(got, "|") != "[hello]" {
		t.Errorf("single read: %q", got)
	}
	if got := feed("a\x1b[200~q\x1b[201~b"); strings.Join(got, "|") != "a|[q]|b" {
		t.Errorf("surrounded: %q", got)
	}
	if got := feed("\x1b[20", "0~ab", "cd\x1b[2", "01~j"); strings.Join(got, "|") != "[abcd]|j" {
		t.Errorf("markers and content split across reads: %q", got)
	}
	if got := feed("\x1b[200~line1\r\nline2\rline3\x1b[201~"); strings.Join(got, "|") != "[line1\nline2\nline3]" {
		t.Errorf("newlines: %q", got)
	}
	if got := feed("\x1b[200~", "x", "\x1b[201~"); strings.Join(got, "|") != "[x]" {
		t.Errorf("start and end in separate reads: %q", got)
	}
	if got := feed("\x1b[", "A"); strings.Join(got, "|") != "up" {
		t.Errorf("an arrow split after the CSI introducer: %q", got)
	}
	if got := feed("\x1b"); strings.Join(got, "|") != "esc" {
		t.Errorf("a lone Esc: %q", got)
	}
	if got := feed("\x1b[200~unterminated"); len(got) != 0 {
		t.Errorf("an unterminated paste produced keys: %q", got)
	}
}

func TestInputParserIgnoresMouseAndFocusReports(t *testing.T) {
	cases := []string{"\x1b[<0;10;5M", "\x1b[<0;10;5m", "\x1b[M !!", "\x1b[32;10;5M", "\x1b[I", "\x1b[O"}
	for _, in := range cases {
		if got := parseInput([]byte(in)); len(got) != 0 {
			t.Errorf("%q produced keys %v", in, got)
		}
	}
	got := parseInput([]byte("\x1b[M !!j"))
	if len(got) != 1 || got[0].String() != "j" {
		t.Errorf("X10 mouse report was not consumed whole: %v", got)
	}
}

func TestPastedTextInTheOverlayIsDataNotCommands(t *testing.T) {
	h := newHarness(t, true, time.Hour)
	h.run("echo hi")
	h.press("\x00")
	if !strings.Contains(h.user.text(), "FAKE REVIEW SCREEN") {
		t.Fatal("not open")
	}
	h.press("\x1b[200~qqq\x1b[201~")
	if !strings.Contains(h.user.text(), "FAKE REVIEW SCREEN") {
		t.Fatal("pasted q characters closed the overlay")
	}
	if !strings.Contains(h.modelLog(), "key [qqq]") {
		t.Fatalf("the paste did not reach the model as one pasted value: %s", h.modelLog())
	}
	big := strings.Repeat("x", 10000)
	h.press("\x1b[200~" + big + "\x1b[201~")
	h.waitFor("the large paste to arrive whole", func() bool { return strings.Contains(h.modelLog(), "key ["+big+"]") })
	h.press("q")
	h.waitFor("a typed q to still close the overlay", func() bool { return !strings.Contains(h.user.text(), "FAKE REVIEW SCREEN") })
}

func TestMouseAndFocusReportsDoNotReachTheOverlayModel(t *testing.T) {
	h := newHarness(t, true, time.Hour)
	h.press("\x00")
	h.press("\x1b[<0;10;5M\x1b[<0;10;5m\x1b[M !!\x1b[I\x1b[O")
	h.press("j")
	h.waitFor("the typed key after the reports", func() bool { return strings.Contains(h.modelLog(), "key j") })
	log := h.modelLog()
	if strings.Count(log, "key ") != 1 {
		t.Fatalf("mouse or focus reports became keys: %s", log)
	}
	if !strings.Contains(h.user.text(), "FAKE REVIEW SCREEN") {
		t.Fatal("a report closed the overlay")
	}
}

func TestBracketedPasteIsEnabledForTheOverlayAndRestoredAfterwards(t *testing.T) {
	h := newHarness(t, true, time.Hour)
	h.run("echo hi")
	h.press("\x00")
	if got := h.user.lastPasteMode(); got != "on" {
		t.Fatalf("paste mode while the overlay is open: %s", got)
	}
	h.press("q")
	h.waitFor("the overlay to close", func() bool { return h.user.lastPasteMode() == "off" })

	h.run(`printf '\033[?2004h'`)
	h.press("\x00")
	h.press("q")
	h.waitFor("the overlay to close", func() bool { return !strings.Contains(h.user.text(), "FAKE REVIEW SCREEN") })
	time.Sleep(200 * time.Millisecond)
	if got := h.user.lastPasteMode(); got != "on" {
		t.Fatalf("the session's own bracketed paste mode was not restored: %s", got)
	}
	h.run(`printf '\033[?2004l'`)
	h.press("\x00")
	h.press("q")
	h.waitFor("the overlay to close", func() bool { return h.user.lastPasteMode() == "off" })
}

func TestStatusLineShowsHowManyRequestsAreHeld(t *testing.T) {
	h := newHarness(t, true, 40*time.Millisecond)
	h.pending.Store(3)
	h.held.Store(2)
	h.waitFor("the held count", func() bool {
		return strings.Contains(h.statusRow(), "3 proxy requests need review (2 held)")
	})
	h.held.Store(0)
	h.waitFor("the held note to go away", func() bool {
		r := h.statusRow()
		return strings.Contains(r, "3 proxy requests need review") && !strings.Contains(r, "held")
	})
}
