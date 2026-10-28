package cli

import (
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/creack/pty"
	"golang.org/x/term"

	"hsh/internal/attend"
	"hsh/internal/config"
	"hsh/internal/denieddb"
	"hsh/internal/relay"
	"hsh/internal/tui/proxywatch"
	"hsh/internal/vt"
)

var inlineShutdown atomic.Pointer[func()]

func shutdownInline() {
	if f := inlineShutdown.Load(); f != nil {
		(*f)()
	}
}

type inlineMonitor struct {
	session    string
	dbPath     string
	configPath string
	monitor    config.Monitor
	detachKeys string
}

func newInlineMonitor(cfg *config.Config, session, dbPath, configPath string, interactive bool) (*inlineMonitor, string) {
	switch {
	case !cfg.Monitor.Enabled:
		return nil, ""
	case !interactive || !term.IsTerminal(int(os.Stdout.Fd())):
		return nil, ""
	case !vt.Available():
		return nil, "hsh: built without GOEXPERIMENT=simd; the inline proxy review (status line and Ctrl+Space dialog) is disabled"
	}
	if _, err := relay.ParseKey(cfg.Monitor.Key); err != nil {
		return nil, "hsh: " + err.Error() + "; the inline proxy review is disabled"
	}
	return &inlineMonitor{session: session, dbPath: dbPath, configPath: configPath, monitor: cfg.Monitor, detachKeys: cfg.DetachKeys}, ""
}

type sessionDenials struct {
	dbPath  string
	session string
	cfgPath string

	mu sync.Mutex
	db *sql.DB
}

func (s *sessionDenials) open() (*sql.DB, error) {
	if s.db != nil {
		return s.db, nil
	}
	if !denieddb.Exists(s.dbPath) {
		return nil, nil
	}
	db, err := denieddb.Open(s.dbPath)
	if err != nil {
		return nil, err
	}
	s.db = db
	return db, nil
}

func (s *sessionDenials) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db != nil {
		s.db.Close()
		s.db = nil
	}
}

func (s *sessionDenials) Count() (int, int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	db, err := s.open()
	if err != nil || db == nil {
		return 0, 0, err
	}
	rows, err := denieddb.Pending(db, denieddb.Filter{Session: s.session})
	held := 0
	for _, r := range rows {
		if r.Holding > 0 {
			held++
		}
	}
	return len(rows), held, err
}

func (s *sessionDenials) Overlay(cols, rows int) tea.Model {
	s.mu.Lock()
	defer s.mu.Unlock()
	db, err := denieddb.Open(s.dbPath)
	if err != nil {
		return errorModel{err: err}
	}
	if s.db != nil {
		s.db.Close()
	}
	s.db = db
	filter := denieddb.Filter{Session: s.session}
	pending, err := denieddb.Pending(db, filter)
	if err != nil {
		return errorModel{err: err}
	}
	ignored, err := denieddb.Ignored(db, filter)
	if err != nil {
		return errorModel{err: err}
	}
	return proxywatch.New(db, filter, pending, ignored, false)
}

type errorModel struct{ err error }

func (m errorModel) Init() tea.Cmd { return nil }
func (m errorModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if _, ok := msg.(tea.KeyMsg); ok {
		return m, tea.Quit
	}
	return m, nil
}
func (m errorModel) View() string {
	return fmt.Sprintf("cannot open the denial database: %v\n\npress any key to close", m.err)
}

func attachWithRelay(name string, flags []string, running bool, im *inlineMonitor) (int, error) {
	stdin, stdout := int(os.Stdin.Fd()), int(os.Stdout.Fd())
	cols, rows, err := term.GetSize(stdout)
	if err != nil {
		return attachContainer(name, flags, running, nil)
	}
	key, err := relay.ParseKey(im.monitor.Key)
	if err != nil {
		return 0, err
	}
	source := &sessionDenials{dbPath: im.dbPath, session: im.session, cfgPath: im.configPath}
	defer source.Close()

	master, slave, err := pty.Open()
	if err != nil {
		return attachContainer(name, flags, running, nil)
	}
	defer master.Close()
	r, err := relay.New(relay.Config{
		In: os.Stdin, Out: os.Stdout, Pty: master, Cols: cols, Rows: rows,
		StatusLine: im.monitor.StatusLine, Key: key, DetachKeys: im.detachKeys,
		Count: source.Count, Overlay: source.Overlay, Name: im.session,
	})
	if err != nil {
		slave.Close()
		fmt.Fprintf(os.Stderr, "hsh: inline proxy review unavailable: %v\n", err)
		return attachContainer(name, flags, running, nil)
	}

	cmd := exec.Command("podman", attachArgs(name, flags, running)...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = slave, slave, slave
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	oldState, err := term.MakeRaw(stdin)
	if err != nil {
		slave.Close()
		return attachContainer(name, flags, running, nil)
	}
	var once sync.Once
	restore := func() {
		once.Do(func() {
			r.Close()
			_ = term.Restore(stdin, oldState)
		})
	}
	defer restore()
	inlineShutdown.Store(&restore)
	defer inlineShutdown.Store(nil)

	_ = attend.Touch(attend.Path(denieddb.SessionDir(im.session)))
	if err := cmd.Start(); err != nil {
		slave.Close()
		return 0, err
	}
	slave.Close()
	activeClient.Store(cmd.Process)
	defer activeClient.Store(nil)

	winch := make(chan os.Signal, 1)
	signal.Notify(winch, syscall.SIGWINCH)
	defer signal.Stop(winch)
	go func() {
		for range winch {
			if c, rw, err := term.GetSize(stdout); err == nil {
				r.Resize(c, rw)
			}
		}
	}()
	if running {
		r.Nudge()
	}
	stopAttend := make(chan struct{})
	attendDone := make(chan struct{})
	go func() {
		attend.Run(attend.Path(denieddb.SessionDir(im.session)), stopAttend)
		close(attendDone)
	}()
	_ = r.Run()
	close(stopAttend)
	<-attendDone
	waitErr := cmd.Wait()
	restore()
	return containerExitCode(name, waitErr)
}

func containerExitCode(name string, waitErr error) (int, error) {
	if waitErr != nil {
		if exitErr, ok := waitErr.(*exec.ExitError); ok {
			return exitErr.ExitCode(), nil
		}
		return 0, waitErr
	}
	out, err := podman("inspect", "--format", "{{.State.ExitCode}}", name)
	if err != nil {
		return 0, nil
	}
	code := 0
	_, _ = fmt.Sscanf(strings.TrimSpace(out), "%d", &code)
	return code, nil
}
