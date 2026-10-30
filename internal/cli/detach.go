package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/peterbourgon/ff/v4"

	"hsh/internal/state"
)

var (
	activeClient atomic.Pointer[os.Process]
	detachReason atomic.Pointer[string]
	exitReason   atomic.Pointer[string]
)

func requestDetach(reason string) error {
	p := activeClient.Load()
	if p == nil {
		return errors.New("this terminal is not attached to a session")
	}
	detachReason.Store(&reason)
	return p.Signal(syscall.SIGTERM)
}

func watchDetachSignal() {
	ch := make(chan os.Signal, 1)
	notifyDetach(ch)
	go func() {
		for range ch {
			_ = requestDetach("hsh detach or --take-over")
		}
	}()
}

func exitSession(id string) error {
	pod := "hsh-" + id
	if !podExists(pod) {
		return fmt.Errorf("no session %s (see: hsh ls)", id)
	}
	_ = os.Remove(filepath.Join(state.SessionDir(id), "secrets.json"))
	if out, err := podman("pod", "stop", "--time", "2", pod); err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(out))
	}
	return nil
}

func readClientPID(sessionDir string) int {
	data, err := os.ReadFile(clientPIDPath(sessionDir))
	if err != nil {
		return 0
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		return 0
	}
	return pid
}

func isHshClient(pid int) bool {
	data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "cmdline"))
	if err != nil {
		return false
	}
	return isHshClientArgs(strings.Split(strings.TrimRight(string(data), "\x00"), "\x00"))
}

func isHshClientArgs(args []string) bool {
	if len(args) == 0 || !strings.HasPrefix(filepath.Base(args[0]), "hsh") {
		return false
	}
	for _, a := range args[1:] {
		if a == "run" || a == "resume" {
			return true
		}
	}
	return false
}

func detachClient(sessionDir string, wait time.Duration) (bool, error) {
	pid := readClientPID(sessionDir)
	if pid == 0 || syscall.Kill(pid, 0) != nil {
		return false, nil
	}
	if !isHshClient(pid) {
		_ = os.Remove(clientPIDPath(sessionDir))
		return false, nil
	}
	if err := syscall.Kill(pid, syscall.SIGUSR1); err != nil {
		return false, fmt.Errorf("signalling hsh client (pid %d): %w", pid, err)
	}
	deadline := time.Now().Add(wait)
	for time.Now().Before(deadline) {
		if syscall.Kill(pid, 0) != nil || !fileExists(clientPIDPath(sessionDir)) {
			return true, nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return true, fmt.Errorf("hsh client (pid %d) did not detach within %s", pid, wait)
}

func resolveSessionArg(args []string) (string, error) {
	if len(args) == 1 {
		return strings.TrimPrefix(args[0], "hsh-"), nil
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	pods, err := listSandboxes("label=hsh.workspace=" + cwd)
	if err != nil {
		return "", err
	}
	var attached []string
	for _, p := range sandboxPods(pods) {
		if sessionStatus(p) == "Attached" {
			attached = append(attached, p.Session())
		}
	}
	switch {
	case len(attached) == 1:
		return attached[0], nil
	case len(attached) > 1:
		return "", fmt.Errorf("several attached sessions for this workspace:\n  %s\ndetach one with: hsh detach <session id>", strings.Join(attached, "\n  "))
	case len(pods) == 1:
		return pods[0].Session(), nil
	}
	return "", fmt.Errorf("no attached session for this workspace; name one with: hsh detach <session id>")
}

func detachCommand() *ff.Command {
	return newCommand("detach", "hsh detach [SESSION]", "detach the terminal attached to a session; the session keeps running", nil, func(ctx context.Context, args []string) error {
		if len(args) > 1 {
			return fmt.Errorf("detach takes at most one session id")
		}
		id, err := resolveSessionArg(args)
		if err != nil {
			return err
		}
		if !podExists("hsh-" + id) {
			return fmt.Errorf("no session %s (see: hsh ls)", id)
		}
		attached, err := detachClient(state.SessionDir(id), 10*time.Second)
		if err != nil {
			return err
		}
		if !attached {
			fmt.Printf("session %s is not attached\n", id)
			return nil
		}
		fmt.Printf("detached session %s; re-enter with: hsh resume %s\n", id, id)
		return nil
	})
}
