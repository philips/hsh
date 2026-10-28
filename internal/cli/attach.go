package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/term"
)

func attachFlags(interactive, detachable bool, detachKeys string) []string {
	switch {
	case !interactive:
		return nil
	case detachable:
		return []string{"--sig-proxy=false", "--detach-keys=" + detachKeys}
	default:
		return []string{"--detach-keys="}
	}
}

func withRunFlags(wsArgs, flags []string) []string {
	out := []string{"run"}
	out = append(out, flags...)
	return append(out, wsArgs[1:]...)
}

func attachArgs(name string, flags []string, running bool) []string {
	args := []string{"start", "-ai"}
	if running {
		args = []string{"attach"}
	}
	args = append(args, flags...)
	return append(args, name)
}

func attachContainer(name string, flags []string, running bool, im *inlineMonitor) (int, error) {
	if im != nil {
		return attachWithRelay(name, flags, running, im)
	}
	cmd := exec.Command("podman", attachArgs(name, flags, running)...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	var saved *term.State
	if term.IsTerminal(int(os.Stdin.Fd())) {
		saved, _ = term.GetState(int(os.Stdin.Fd()))
	}
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	activeClient.Store(cmd.Process)
	waitErr := cmd.Wait()
	activeClient.Store(nil)
	if saved != nil {
		_ = term.Restore(int(os.Stdin.Fd()), saved)
	}
	return containerExitCode(name, waitErr)
}

func clientPIDPath(sessionDir string) string {
	return filepath.Join(sessionDir, "client.pid")
}

func markAttached(sessionDir string) func() {
	path := clientPIDPath(sessionDir)
	_ = os.WriteFile(path, []byte(strconv.Itoa(os.Getpid())+"\n"), 0o600)
	return func() { _ = os.Remove(path) }
}

func clientAttached(sessionDir string) bool {
	data, err := os.ReadFile(clientPIDPath(sessionDir))
	if err != nil {
		return false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		return false
	}
	return syscall.Kill(pid, 0) == nil
}

func (p podInfo) workspaceContainerStatus() (string, bool) {
	for _, c := range p.Containers {
		if strings.HasSuffix(c.Names, "-ws") {
			return strings.ToLower(c.Status), true
		}
	}
	return "", false
}

func sessionStatus(p podInfo) string {
	ws, ok := p.workspaceContainerStatus()
	if !ok {
		return p.Status
	}
	switch ws {
	case "running":
		if clientAttached(sessionDirFor(p.Name)) {
			return "Attached"
		}
		return "Detached"
	case "exited", "stopped":
		if strings.EqualFold(p.Status, "running") || strings.EqualFold(p.Status, "degraded") {
			return "Shell exited"
		}
	}
	return p.Status
}
