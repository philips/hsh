package crashlog

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func readLog(t *testing.T, dir string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "hsh", "crash.log"))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestRecordAppends(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)
	if got := Record("first", "one", []byte("stack-1")); got != filepath.Join(dir, "hsh", "crash.log") {
		t.Fatalf("path %q", got)
	}
	Record("second", "two", []byte("stack-2"))
	log := readLog(t, dir)
	for _, want := range []string{"panic in first (recovered): one", "stack-1", "panic in second (recovered): two", "stack-2", "version:", "args:"} {
		if !strings.Contains(log, want) {
			t.Errorf("missing %q in\n%s", want, log)
		}
	}
	if fi, _ := os.Stat(filepath.Join(dir, "hsh", "crash.log")); fi.Mode().Perm() != 0o600 {
		t.Errorf("mode %v", fi.Mode().Perm())
	}
}

type panicky struct{ where string }

func (p panicky) Init() tea.Cmd {
	if p.where == "init" {
		panic("init-boom")
	}
	return nil
}

func (p panicky) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch p.where {
	case "update":
		panic("update-boom")
	case "cmd":
		return p, func() tea.Msg { panic("cmd-boom") }
	case "batch":
		return p, tea.Batch(func() tea.Msg { return nil }, func() tea.Msg { panic("batch-boom") })
	}
	return p, nil
}

func (p panicky) View() string {
	if p.where == "view" {
		panic("view-boom")
	}
	return ""
}

func mustPanic(t *testing.T, f func()) {
	t.Helper()
	defer func() {
		if recover() == nil {
			t.Fatal("the panic was swallowed")
		}
	}()
	f()
}

func TestGuardRecordsAndRepanics(t *testing.T) {
	for _, where := range []string{"init", "update", "view", "cmd", "batch"} {
		t.Run(where, func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("XDG_STATE_HOME", dir)
			g := Guard("hsh monitor", panicky{where: where})
			mustPanic(t, func() {
				switch where {
				case "init":
					g.Init()
				case "view":
					g.View()
				case "update":
					g.Update(nil)
				case "cmd":
					_, cmd := g.Update(nil)
					cmd()
				case "batch":
					_, cmd := g.Update(nil)
					for _, c := range cmd().(tea.BatchMsg) {
						if c != nil {
							c()
						}
					}
				}
			})
			if log := readLog(t, dir); !strings.Contains(log, "panic in hsh monitor (recovered): "+where+"-boom") || !strings.Contains(log, "crashlog_test.go") {
				t.Fatalf("log:\n%s", log)
			}
		})
	}
}

func TestGuardPassesQuitThrough(t *testing.T) {
	g := Guard("x", quitter{})
	_, cmd := g.Update(nil)
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("tea.Quit no longer quits")
	}
}

type quitter struct{}

func (quitter) Init() tea.Cmd                       { return nil }
func (quitter) Update(tea.Msg) (tea.Model, tea.Cmd) { return quitter{}, tea.Quit }
func (quitter) View() string                        { return "" }

func TestInstallCapturesAFatalPanicInAnyGoroutine(t *testing.T) {
	if os.Getenv("CRASHLOG_CHILD") == "1" {
		Install()
		done := make(chan struct{})
		go func() {
			defer close(done)
			var rows []int
			_ = rows[-len(os.Args)]
		}()
		<-done
		return
	}
	dir := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=^TestInstallCapturesAFatalPanicInAnyGoroutine$")
	cmd.Env = append(os.Environ(), "CRASHLOG_CHILD=1", "XDG_STATE_HOME="+dir)
	if out, err := cmd.CombinedOutput(); err == nil {
		t.Fatalf("the child did not crash:\n%s", out)
	}
	log := readLog(t, dir)
	if !strings.Contains(log, "panic: runtime error: index out of range") || !strings.Contains(log, "crashlog_test.go") {
		t.Fatalf("crash.log:\n%s", log)
	}
}
