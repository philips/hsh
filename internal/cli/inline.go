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
	"time"

	"github.com/creack/pty"
	"golang.org/x/term"

	"hsh/internal/config"
	"hsh/internal/denieddb"
	"hsh/internal/relay"
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
	return &inlineMonitor{session: session, dbPath: dbPath, configPath: configPath, monitor: cfg.Monitor}, ""
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

func (s *sessionDenials) Pending() ([]relay.Item, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	db, err := s.open()
	if err != nil || db == nil {
		return nil, err
	}
	rows, err := denieddb.Pending(db, denieddb.Filter{Session: s.session})
	if err != nil {
		return nil, err
	}
	items := make([]relay.Item, 0, len(rows))
	for _, r := range rows {
		last, _ := time.Parse(time.RFC3339, r.LastSeen)
		items = append(items, relay.Item{Host: r.Host, Count: r.Count, Last: last})
	}
	return items, nil
}

func (s *sessionDenials) Allow(host string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	db, err := s.open()
	if err != nil {
		return err
	}
	if db == nil {
		return fmt.Errorf("no denial database")
	}
	_, _, err = applyAllow(db, s.session, s.cfgPath, []string{host})
	return err
}

func (s *sessionDenials) Deny(host string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	db, err := s.open()
	if err != nil {
		return err
	}
	if db == nil {
		return fmt.Errorf("no denial database")
	}
	return denieddb.MarkIgnored(db, s.session, []string{host})
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
		StatusLine: im.monitor.StatusLine, Key: key, Source: source, Name: im.session,
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

	if err := cmd.Start(); err != nil {
		slave.Close()
		return 0, err
	}
	slave.Close()

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
	_ = r.Run()
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
