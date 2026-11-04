package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"syscall"
	"time"

	"hsh/internal/forward"
	"hsh/internal/sshd"
	"hsh/internal/state"
	"hsh/internal/tui/palette"
)

var (
	forwardSpawn          = spawnManagedForward
	forwardSessionRunning = containerRunning
)

type forwardRegistry struct {
	dir string
}

type forwardMeta struct {
	ID      string    `json:"id"`
	Session string    `json:"session"`
	Bind    string    `json:"bind"`
	Local   int       `json:"local"`
	Remote  int       `json:"remote"`
	PID     int       `json:"pid"`
	Started time.Time `json:"started"`
}

func forwardsDir() string {
	return filepath.Join(state.Dir(), "forwards")
}

func forwardID(session string, local int) string {
	return session + "-" + strconv.Itoa(local)
}

func monitorForwardOps() *palette.ForwardOps {
	r := &forwardRegistry{dir: forwardsDir()}
	return &palette.ForwardOps{
		List:      r.list,
		Start:     r.start,
		Stop:      r.stop,
		Listening: forwardListening,
	}
}

func forwardListening(session string) ([]sshd.Listener, error) {
	client, err := connectSession(session)
	if err != nil {
		return nil, err
	}
	defer client.Close()
	return forward.ListPorts(client)
}

func spawnManagedForward(exe string, args []string, logPath string) (int, error) {
	log, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return 0, err
	}
	defer log.Close()
	cmd := exec.Command(exe, args...)
	cmd.Args[0] = "hsh-forward"
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Stdout = log
	cmd.Stderr = log
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	pid := cmd.Process.Pid
	go func() { _ = cmd.Wait() }()
	return pid, nil
}

func pidDead(pid int) bool {
	if pid <= 0 {
		return true
	}
	return errors.Is(syscall.Kill(pid, 0), syscall.ESRCH)
}

func (r *forwardRegistry) list() ([]palette.Managed, error) {
	entries, err := os.ReadDir(r.dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []palette.Managed
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		m, ok, err := r.entry(e.Name())
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, m)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Started.Before(out[j].Started) })
	return out, nil
}

func (r *forwardRegistry) entry(id string) (palette.Managed, bool, error) {
	path := filepath.Join(r.dir, id, "meta.json")
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		os.RemoveAll(filepath.Join(r.dir, id))
		return palette.Managed{}, false, nil
	}
	if err != nil {
		return palette.Managed{}, false, err
	}
	var meta forwardMeta
	if err := json.Unmarshal(data, &meta); err != nil {
		os.RemoveAll(filepath.Join(r.dir, id))
		return palette.Managed{}, false, nil
	}
	if pidDead(meta.PID) {
		os.RemoveAll(filepath.Join(r.dir, id))
		return palette.Managed{}, false, nil
	}
	return palette.Managed(meta), true, nil
}

func (r *forwardRegistry) start(session string, remote int) (palette.Managed, error) {
	id := forwardID(session, remote)
	if _, ok, err := r.entry(id); err != nil {
		return palette.Managed{}, err
	} else if ok {
		return palette.Managed{}, fmt.Errorf("already forwarding session %s's port %d", session, remote)
	}
	if !forwardSessionRunning("hsh-" + session + "-ws") {
		return palette.Managed{}, fmt.Errorf("session %s has no running workspace container; start it with: hsh resume %s", session, session)
	}
	if err := forward.CheckLocal("127.0.0.1", []forward.Spec{{Local: remote, Remote: remote}}); err != nil {
		return palette.Managed{}, fmt.Errorf("local port %d is already in use; forward it under a different local port with: hsh forward <other>:%d", remote, remote)
	}
	exe, err := os.Executable()
	if err != nil {
		return palette.Managed{}, err
	}
	dir := filepath.Join(r.dir, id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return palette.Managed{}, err
	}
	args := []string{"forward", "--managed", "--session", session, "--bind", "127.0.0.1", fmt.Sprintf("%d:%d", remote, remote)}
	pid, err := forwardSpawn(exe, args, filepath.Join(dir, "log"))
	if err != nil {
		os.RemoveAll(dir)
		return palette.Managed{}, err
	}
	meta := forwardMeta{ID: id, Session: session, Bind: "127.0.0.1", Local: remote, Remote: remote, PID: pid, Started: time.Now()}
	data, err := json.Marshal(meta)
	if err != nil {
		os.RemoveAll(dir)
		return palette.Managed{}, err
	}
	if err := os.WriteFile(filepath.Join(dir, "meta.json"), data, 0o600); err != nil {
		os.RemoveAll(dir)
		return palette.Managed{}, err
	}
	return palette.Managed(meta), nil
}

func (r *forwardRegistry) stop(id string) (string, error) {
	m, ok, err := r.entry(id)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", fmt.Errorf("no active forward %s", id)
	}
	if err := syscall.Kill(m.PID, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
		return "", err
	}
	for i := 0; i < 100 && !pidDead(m.PID); i++ {
		time.Sleep(50 * time.Millisecond)
	}
	if !pidDead(m.PID) {
		_ = syscall.Kill(m.PID, syscall.SIGKILL)
	}
	if err := os.RemoveAll(filepath.Join(r.dir, id)); err != nil {
		return "", err
	}
	return fmt.Sprintf("stopped forwarding session %s's port %d (local %s)", m.Session, m.Remote, net.JoinHostPort(m.Bind, strconv.Itoa(m.Local))), nil
}

func removeManagedForward(session string, local int) {
	os.RemoveAll(filepath.Join(forwardsDir(), forwardID(session, local)))
}
