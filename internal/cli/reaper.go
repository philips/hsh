package cli

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const reaperStartTimeout = 60 * time.Second

func stopSessionPod(podName, sessionDir string) {
	_ = os.Remove(filepath.Join(sessionDir, "secrets.json"))
	if out, err := podman("pod", "stop", "--time", "2", podName); err != nil && podExists(podName) {
		fmt.Fprintf(os.Stderr, "hsh: stopping session pod: %v\n%s", err, out)
	}
}

func spawnReaper(exe, podName, wsName, sessionDir string) error {
	cmd := exec.Command(exe, "--pod", podName, "--ws", wsName, "--session-dir", sessionDir)
	cmd.Args[0] = "hsh-reaper"
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

func Reaper(args []string) error {
	fs := flag.NewFlagSet("hsh-reaper", flag.ContinueOnError)
	podName := fs.String("pod", "", "session pod")
	wsName := fs.String("ws", "", "workspace container")
	sessionDir := fs.String("session-dir", "", "session state directory")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *podName == "" || *wsName == "" || *sessionDir == "" {
		return fmt.Errorf("hsh-reaper needs --pod, --ws and --session-dir")
	}
	lock, err := os.OpenFile(filepath.Join(*sessionDir, "reaper.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return nil
	}
	return reap(*podName, *wsName, *sessionDir)
}

func containerStatus(name string) string {
	out, err := podman("inspect", "--format", "{{.State.Status}}", name)
	if err != nil {
		return "missing"
	}
	return strings.TrimSpace(out)
}

func reap(podName, wsName, sessionDir string) error {
	deadline := time.Now().Add(reaperStartTimeout)
	for containerStatus(wsName) == "created" && time.Now().Before(deadline) {
		time.Sleep(200 * time.Millisecond)
	}
	if containerStatus(wsName) == "running" {
		_, _ = podman("wait", wsName)
	}
	if containerStatus(wsName) == "missing" {
		return nil
	}
	stopSessionPod(podName, sessionDir)
	return nil
}
