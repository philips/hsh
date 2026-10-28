package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
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

func attachContainer(name string, flags []string, running bool) (int, error) {
	args := []string{"start", "-ai"}
	if running {
		args = []string{"attach"}
	}
	args = append(args, flags...)
	args = append(args, name)
	cmd := exec.Command("podman", args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return exitErr.ExitCode(), nil
		}
		return 0, err
	}
	out, err := podman("inspect", "--format", "{{.State.ExitCode}}", name)
	if err != nil {
		return 0, nil
	}
	code := 0
	_, _ = fmt.Sscanf(strings.TrimSpace(out), "%d", &code)
	return code, nil
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
