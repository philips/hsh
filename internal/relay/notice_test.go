package relay

import (
	"strings"
	"testing"
)

func TestStatusLineShowsTheSessionNotice(t *testing.T) {
	notice, pending := "", 0
	r := &Relay{cols: 160, cfg: Config{
		Name:   "unit",
		Count:  func() (int, int, error) { return pending, 0, nil },
		Notice: func() string { return notice },
	}}
	r.pollLocked()
	if got := r.statusLineLocked(); !strings.HasPrefix(got, sgrGreen) || !strings.Contains(got, "nothing to review") {
		t.Fatalf("idle: %q", got)
	}
	notice = "login a failed: \x1b]0;evil\x07log in again"
	r.pollLocked()
	got := r.statusLineLocked()
	if !strings.HasPrefix(got, sgrRed) || !strings.Contains(got, " hsh unit | login a failed: ]0;evillog in again") {
		t.Fatalf("notice: %q", got)
	}
	if strings.Count(got, "\x1b") != 2 {
		t.Fatalf("the notice let an escape sequence through: %q", got)
	}
	pending = 2
	r.pollLocked()
	if got := r.statusLineLocked(); !strings.Contains(got, "login a failed") || !strings.Contains(got, "* 2 to review | Ctrl+Space") {
		t.Fatalf("notice with pending requests: %q", got)
	}
	notice = ""
	r.pollLocked()
	if got := r.statusLineLocked(); !strings.HasPrefix(got, sgrBlue) || strings.Contains(got, "login") {
		t.Fatalf("after the notice cleared: %q", got)
	}
}

func TestStatusLineShowsTheSuspendNote(t *testing.T) {
	suspended, pending := "", 1
	r := &Relay{cols: 160, cfg: Config{
		Name:        "unit",
		Count:       func() (int, int, error) { return pending, 1, nil },
		SuspendNote: func() string { return suspended },
	}}
	r.pollLocked()
	if got := r.statusLineLocked(); !strings.Contains(got, "needs review") {
		t.Fatalf("before the suspend: %q", got)
	}
	suspended = "SUSPENDED: proxy approval"
	r.pollLocked()
	got := r.statusLineLocked()
	if !strings.HasPrefix(got, sgrYellow) || !strings.Contains(got, " hsh unit | SUSPENDED: proxy approval | * 1 to review (1 held) | Ctrl+Space") {
		t.Fatalf("frozen: %q", got)
	}
	suspended = ""
	pending = 0
	r.pollLocked()
	if got := r.statusLineLocked(); !strings.Contains(got, "nothing to review") || strings.Contains(got, "frozen") {
		t.Fatalf("after the resume: %q", got)
	}
}
