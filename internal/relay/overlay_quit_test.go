package relay

import (
	"io"
	"os/exec"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/creack/pty"

	"hsh/internal/monitordb"
	"hsh/internal/tui/proxywatch"
	"hsh/internal/vt"
)

func newHarnessRealModel(t *testing.T, dbPath string) *harness {
	t.Helper()
	key, _ := ParseKey("ctrl-space")
	cmd := exec.Command("bash", "-c", `stty -echo; while IFS= read -r l; do eval "$l"; done`)
	master, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: userCols, Rows: uint16(userRows - 1)})
	if err != nil {
		t.Fatal(err)
	}
	ut, err := vt.New(vt.Options{Cols: userCols, Rows: userRows, Scrollback: 1000})
	if err != nil {
		t.Fatal(err)
	}
	user := &userTerminal{term: ut}
	pr, pw := io.Pipe()
	h := &harness{t: t, master: master, user: user, keys: pw, done: make(chan struct{})}
	count := func() (int, int, error) {
		db, err := monitordb.Open(dbPath)
		if err != nil {
			return 0, 0, err
		}
		defer db.Close()
		pending, err := monitordb.Pending(db, monitordb.Filter{Session: "unit"})
		if err != nil {
			return 0, 0, err
		}
		return len(pending), 0, nil
	}
	r, err := New(Config{In: pr, Out: user, Pty: master, Cols: userCols, Rows: userRows,
		StatusLine: true, Key: key, Name: "unit", PollEvery: time.Hour,
		Count: count,
		Overlay: func(cols, rows int) tea.Model {
			db, _ := monitordb.Open(dbPath)
			pending, _ := monitordb.Pending(db, monitordb.Filter{Session: "unit"})
			return proxywatch.New(db, monitordb.Filter{Session: "unit"}, pending, nil, false)
		}})
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		r.Run()
		close(h.done)
	}()
	t.Cleanup(func() {
		pw.Close()
		_ = cmd.Process.Kill()
		_ = master.Close()
		<-h.done
	})
	return h
}

func TestQuitKeyClosesTheOverlayAndTrailingBytesGoToTheSession(t *testing.T) {
	if !vt.Available() {
		t.Skip("built without GOEXPERIMENT=simd")
	}
	dir := t.TempDir()
	dbPath := dir + "/monitor.db"
	db, err := monitordb.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := monitordb.Record(db, "/ws", "unit", "blocked.example.test", "https://blocked.example.test"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	h := newHarnessRealModel(t, dbPath)
	h.press("\x00")
	h.waitFor("the overlay to open", func() bool {
		return strings.Contains(h.user.text(), "denied hosts awaiting review")
	})
	h.press("qecho after-$((6*7))\r")
	h.waitFor("q to close the overlay and the trailing typing to reach the session", func() bool {
		return !strings.Contains(h.user.text(), "denied hosts awaiting review") &&
			strings.Contains(h.user.text(), "after-42")
	})
	if strings.Contains(h.user.text(), "approved") {
		t.Fatal("the trailing enter approved the host instead of reaching the session")
	}
}
