package crashlog

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"hsh/internal/state"
	"hsh/internal/version"
)

var mu sync.Mutex

func Path() string {
	return filepath.Join(state.Dir(), "crash.log")
}

func open() (*os.File, error) {
	path := Path()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	return os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
}

func header(what string) string {
	return fmt.Sprintf("=== %s %s pid %d: %s\nversion: %s\nargs: %s\n",
		time.Now().UTC().Format(time.RFC3339), filepath.Base(os.Args[0]), os.Getpid(), what,
		version.Get().String(), strings.Join(os.Args, " "))
}

func Install() {
	f, err := open()
	if err != nil {
		return
	}
	_ = debug.SetCrashOutput(f, debug.CrashOptions{})
	f.Close()
}

func Record(where string, r any, stack []byte) string {
	mu.Lock()
	defer mu.Unlock()
	f, err := open()
	if err != nil {
		return ""
	}
	defer f.Close()
	fmt.Fprintf(f, "%spanic in %s (recovered): %v\n\n%s\n", header("recovered panic"), where, r, stack)
	return Path()
}

type guarded struct {
	where string
	model tea.Model
}

func Guard(where string, m tea.Model) tea.Model {
	return guarded{where: where, model: m}
}

func (g guarded) recordAndRepanic() {
	if p := recover(); p != nil {
		Record(g.where, p, debug.Stack())
		panic(p)
	}
}

func (g guarded) Init() tea.Cmd {
	defer g.recordAndRepanic()
	return g.cmd(g.model.Init())
}

func (g guarded) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	defer g.recordAndRepanic()
	m, cmd := g.model.Update(msg)
	return guarded{where: g.where, model: m}, g.cmd(cmd)
}

func (g guarded) View() string {
	defer g.recordAndRepanic()
	return g.model.View()
}

func (g guarded) cmd(cmd tea.Cmd) tea.Cmd {
	if cmd == nil {
		return nil
	}
	return func() tea.Msg {
		defer g.recordAndRepanic()
		msg := cmd()
		if batch, ok := msg.(tea.BatchMsg); ok {
			out := make(tea.BatchMsg, len(batch))
			for i, c := range batch {
				out[i] = g.cmd(c)
			}
			return out
		}
		return msg
	}
}
