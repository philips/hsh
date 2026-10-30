package relay

import (
	"strconv"
	"strings"
	"testing"
	"time"
)

func validMonitorKeys(t *testing.T) []Key {
	t.Helper()
	var names []string
	names = append(names, "ctrl-space")
	for c := 'a'; c <= 'z'; c++ {
		if c == 'h' || c == 'i' || c == 'j' || c == 'm' {
			continue
		}
		names = append(names, "ctrl-"+string(c))
	}
	names = append(names, `ctrl-\`, "ctrl-^", "ctrl-_")
	var keys []Key
	for _, n := range names {
		k, err := ParseKey(n)
		if err != nil {
			t.Fatalf("%s: %v", n, err)
		}
		keys = append(keys, k)
	}
	return keys
}

func genuineMatch(k Key, p []byte, idx int) bool {
	if p[idx] == k.Byte {
		return true
	}
	ev, ok := decodeCtrl(p[idx:])
	return ok && ev.code == k.Code && !ev.release
}

var terminalReplyCorpus = []string{
	"\x1b[1;1R",
	"\x1b[12;40R",
	"\x1b[0n",
	"\x1b[3n",
	"\x1b[?62;1;2;6;8;9;22c",
	"\x1b[?6c",
	"\x1b[>0;276;0c",
	"\x1b[?2004;2y",
	"\x1b[?2004;1y",
	"\x1b[?1049;2y",
	"\x1b[?1;0;2004u",
	"\x1b[?1u",
	"\x1b[I",
	"\x1b[O",
	"\x1b[<0;10;5M",
	"\x1b[<64;10;5m",
	"\x1b[M !!",
	"\x1b[32;10;5M",
	"\x1b[200~",
	"\x1b[201~",
	"\x1b]11;rgb:0000/0000/0000\x1b\\",
	"\x1bP1+r636f6c6f72\x1b\\",
	"\x1b[32;1u",
	"\x1b[32u",
	"\x1b[32;2u",
	"\x1b[32;9u",
	"\x1b[32;5:3u",
	"\x1b[113u",
	"\x1b[27u",
}

func TestTerminalRepliesNeverMatchAMonitorKey(t *testing.T) {
	keys := validMonitorKeys(t)
	for _, seq := range terminalReplyCorpus {
		p := []byte(seq)
		for _, k := range keys {
			idx, width := k.find(p)
			if idx < 0 {
				continue
			}
			if !genuineMatch(k, p, idx) {
				t.Errorf("key %q matched a terminal reply: %q at %d width %d", k.Byte, seq, idx, width)
			}
		}
	}
}

func TestGenuineKeyEncodingsStillMatchEveryMonitorKey(t *testing.T) {
	for _, k := range validMonitorKeys(t) {
		genuine := []string{
			string([]byte{k.Byte}),
			"\x1b[" + strconv.Itoa(int(k.Code)) + ";5u",
			"\x1b[27;5;" + strconv.Itoa(int(k.Code)) + "~",
		}
		for _, seq := range genuine {
			idx, width := k.find([]byte("x" + seq + "y"))
			if idx != 1 || width != len(seq) {
				t.Errorf("key byte %d: %q matched at %d width %d", k.Byte, seq, idx, width)
			}
		}
		for _, seq := range genuine {
			for _, other := range validMonitorKeys(t) {
				if other.Byte == k.Byte {
					continue
				}
				if idx, _ := other.find([]byte("x" + seq + "y")); idx >= 0 && !genuineMatch(other, []byte("x"+seq+"y"), idx) {
					t.Errorf("key byte %d falsely matched %q", other.Byte, seq)
				}
			}
		}
	}
}

func TestParserNeverEmitsAMonitorKeyEventForATerminalReply(t *testing.T) {
	for _, k := range validMonitorKeys(t) {
		var genuine []string
		for _, seq := range []string{string([]byte{k.Byte}), "\x1b[" + strconv.Itoa(int(k.Code)) + ";5u"} {
			var ip inputParser
			for _, m := range ip.feed([]byte(seq)) {
				genuine = append(genuine, m.String())
			}
		}
		if len(genuine) == 0 {
			t.Fatalf("key byte %d produced no event for its own encoding", k.Byte)
		}
		for _, seq := range terminalReplyCorpus {
			var ip inputParser
			for _, m := range ip.feed([]byte(seq)) {
				for _, g := range genuine {
					if m.String() == g {
						t.Errorf("reply %q produced the monitor key event %q", seq, g)
					}
				}
			}
		}
	}
}

func TestAgentQueriesAndRepliesCannotOpenTheOverlay(t *testing.T) {
	h := newHarness(t, true, time.Hour)
	h.run("echo hi")
	h.run(`printf '\033[6n\033[c\033[?u\033[=1;1u\033[>1u\033[<u\033[?2004h\033[?2004l\033[?1000h\033[?1006h\033[?6n\033[2J'`)
	joined := strings.Join(terminalReplyCorpus, "")
	h.press(joined)
	h.relay.mu.Lock()
	open, paused := h.relay.review.open, h.relay.paused
	h.relay.mu.Unlock()
	if open || paused {
		t.Fatalf("the overlay opened from agent-driven terminal chatter: open=%v paused=%v", open, paused)
	}
	if strings.Contains(h.user.text(), "FAKE REVIEW SCREEN") {
		t.Fatal("the overlay was drawn")
	}
	if !strings.Contains(h.statusRow(), "nothing to review") {
		t.Fatalf("status row changed: %q", h.statusRow())
	}

	h.press("\x00")
	if !strings.Contains(h.user.text(), "FAKE REVIEW SCREEN") {
		t.Fatal("isolation broke the feature: the real key no longer opens the overlay")
	}
}

func TestIsolationHoldsForOtherMonitorKeys(t *testing.T) {
	for _, name := range []string{"ctrl-a", "ctrl-\\"} {
		t.Run(name, func(t *testing.T) {
			key, err := ParseKey(name)
			if err != nil {
				t.Fatal(err)
			}
			script := `stty -echo; while IFS= read -r l; do eval "$l"; done`
			h := newHarnessKey(t, true, time.Hour, script, key)
			h.run("echo hi")
			h.press(strings.Join(terminalReplyCorpus, ""))
			h.relay.mu.Lock()
			open, paused := h.relay.review.open, h.relay.paused
			h.relay.mu.Unlock()
			if open || paused {
				t.Fatalf("terminal chatter opened the overlay for key %s", name)
			}
			h.press(string([]byte{key.Byte}))
			if !strings.Contains(h.user.text(), "FAKE REVIEW SCREEN") {
				t.Fatalf("the real key %s no longer opens the overlay", name)
			}
		})
	}
}

func TestAgentFloodWhileTheOverlayIsOpenIsHeldAndInert(t *testing.T) {
	h := newHarness(t, true, time.Hour)
	h.run("echo hi")
	before := len(h.user.rawText())
	h.press("\x00")
	h.run(`printf '\000\033[2J\033[32;5u\033]7777;FLOOD-HINT-42\007\033[1;999rFLOOD42'`)
	time.Sleep(500 * time.Millisecond)
	if got := h.user.rawText()[before:]; strings.Contains(got, "FLOOD42") {
		t.Fatal("agent output reached the user terminal while the overlay was open")
	}
	if !strings.Contains(h.user.text(), "FAKE REVIEW SCREEN") {
		t.Fatal("the agent flood closed the overlay")
	}
	h.press("q")
	h.waitFor("the held flood to be delivered", func() bool { return strings.Contains(h.user.text(), "FLOOD42") })
	if strings.Contains(h.user.text(), "FLOOD-HINT-42") {
		t.Error("a forged hint body reached the user terminal")
	}
}

func TestForgedHintsAreInert(t *testing.T) {
	h := newHarness(t, true, time.Hour)
	h.run("echo hi")
	before := h.polls.Load()
	h.run(`printf '\033]7777;ATTACK-TEXT-9999;fake.example.test\007'`)
	h.waitFor("the forged hint to cause a poll", func() bool { return h.polls.Load() > before })
	time.Sleep(300 * time.Millisecond)
	if strings.Contains(h.user.text(), "ATTACK-TEXT-9999") || strings.Contains(h.user.text(), "fake.example.test") {
		t.Fatal("a forged hint body reached the user terminal")
	}
	row := h.statusRow()
	if strings.Contains(row, "ATTACK") || strings.Contains(row, "fake.example.test") || !strings.Contains(row, "nothing to review") {
		t.Fatalf("a forged hint changed the status line: %q", row)
	}

	h.run(`printf '\033]7777'`)
	h.run(`printf ';split-half-42\007'`)
	h.waitFor("the split hint to poll", func() bool { return h.polls.Load() > before+1 })
	if strings.Contains(h.user.text(), "split-half-42") {
		t.Fatal("a hint split across reads leaked its body")
	}
}

func TestHintFilterGivesUpOnLongUninterminatedBodies(t *testing.T) {
	var f hintFilter
	body := strings.Repeat("A", 700)
	clean, hints := f.feed([]byte("\x1b]7777;" + body))
	if hints != 0 {
		t.Fatalf("an uninterminated body was counted as a hint")
	}
	if !strings.Contains(string(clean), body) {
		t.Fatal("a long unterminated hint body vanished instead of flowing through after the give-up bound")
	}
	if strings.Contains(string(clean), "\x1b") {
		t.Fatal("the give-up emitted the escape: a later chunk could reassemble a live OSC on the user terminal")
	}
	if len(f.held) != 0 {
		t.Fatalf("the filter is still holding %d bytes after giving up", len(f.held))
	}
	if _, hints := f.feed([]byte("\x1b]7777;real\007")); hints != 1 {
		t.Fatal("the filter did not recover and count a real hint after the give-up bound")
	}
}

func TestRegionFilterGiveUpCannotSmuggleARegionPastTheSplit(t *testing.T) {
	var f regionFilter
	out1 := f.feed([]byte("\x1b[1;1;1;1;1;1;1;1;1"), 29)
	out2 := f.feed([]byte("1;40r"), 29)
	joined := string(out1) + string(out2)
	if strings.Contains(joined, "\x1b") {
		t.Fatalf("the overlong-param give-up emitted an escape: the user terminal would reassemble %q into a live DECSTBM over the status row", joined)
	}
	var g regionFilter
	a := g.feed([]byte("\x1b[1;1;1;1;1;1;1;1;1"), 29)
	b := g.feed([]byte("1;40r"), 29)
	if got := string(append(a, b...)); strings.Contains(got, "\x1b[1;40r") || strings.Contains(got, "\x1b[") {
		t.Fatalf("smuggled region: %q", got)
	}
}

func TestRegionFilterRewritesResetsSmuggledBehindAnAbortedCSI(t *testing.T) {
	var f regionFilter
	if got := string(f.feed([]byte("\x1b[\x1b[r"), 29)); got != "\x1b[1;29r" {
		t.Fatalf("a region reset smuggled behind an aborted CSI reached the user terminal as %q", got)
	}
	var g regionFilter
	a := g.feed([]byte("\x1b[\x1b["), 29)
	b := g.feed([]byte("r"), 29)
	if got := string(append(a, b...)); got != "\x1b[1;29r" {
		t.Fatalf("the split smuggle reached the user terminal as %q", got)
	}
}

func TestRegionFilterNeverIncludesTheStatusRow(t *testing.T) {
	cases := map[string]string{
		"\x1b[1;50r":                  "\x1b[1;29r",
		"\x1b[r":                       "\x1b[1;29r",
		"\x1b[5;0r":                    "\x1b[5;29r",
		"\x1b[30;31r":                  "\x1b[1;29r",
		"\x1b[29;29r":                  "\x1b[1;29r",
		"\x1b[2;3r":                    "\x1b[2;3r",
		"\x1b[12345678901234567890;2r": "\x1b[1;2r",
		"\x1b[2;12345678901234567890r": "\x1b[2;29r",
	}
	for in, want := range cases {
		var f regionFilter
		if got := string(f.feed([]byte(in), 29)); got != want {
			t.Errorf("%q: got %q want %q", in, got, want)
		}
	}
	var f regionFilter
	out := f.feed([]byte("\x1b[1;50rplain text\x1b[999r"), 29)
	got := string(out)
	if !strings.Contains(got, "\x1b[1;29r") || !strings.Contains(got, "plain text") {
		t.Fatalf("plain text was not preserved: %q", got)
	}
}
