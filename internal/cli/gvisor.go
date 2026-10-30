package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const (
	runtimeDefault = ""
	runtimeGVisor  = "gvisor"
	netstackTarget = "/hsh-netstack"
)

type gvisorSession struct {
	wrapper string
	sockDir string
	sock    string
}

func validRuntime(name string) error {
	switch name {
	case runtimeDefault, runtimeGVisor:
		return nil
	}
	return fmt.Errorf("invalid --runtime value %q (want gvisor, or omit it for the default container runtime)", name)
}

func readSessionRuntime(sessionDir string) string {
	data, err := os.ReadFile(filepath.Join(sessionDir, "runtime"))
	if err != nil {
		return runtimeDefault
	}
	return strings.TrimSpace(string(data))
}

func writeSessionRuntime(sessionDir, name string) error {
	if name == runtimeDefault {
		return nil
	}
	return os.WriteFile(filepath.Join(sessionDir, "runtime"), []byte(name+"\n"), 0o644)
}

func findRunsc() (string, error) {
	if p := os.Getenv("HSH_RUNSC"); p != "" {
		return filepath.Abs(p)
	}
	p, err := exec.LookPath("runsc")
	if err != nil {
		return "", fmt.Errorf("--runtime gvisor needs runsc: install it from https://gvisor.dev/docs/user_guide/install/#install-latest (keep gvisor-bin/ next to runsc) and put it on PATH, or set HSH_RUNSC")
	}
	return filepath.Abs(p)
}

func prepareGVisor(sessionDir string) (*gvisorSession, error) {
	runsc, err := findRunsc()
	if err != nil {
		return nil, err
	}
	g := &gvisorSession{
		wrapper: filepath.Join(sessionDir, "runsc"),
		sockDir: filepath.Join(sessionDir, "netstack"),
	}
	g.sock = filepath.Join(g.sockDir, "sock")
	if len(g.sock) > 107 {
		return nil, fmt.Errorf("netstack socket path %s is longer than a unix socket address allows", g.sock)
	}
	if err := os.MkdirAll(g.sockDir, 0o700); err != nil {
		return nil, err
	}
	script := "#!/bin/sh\nexec " + shellQuote(runsc) + " --ignore-cgroups --overlay2=none --network-proxy-path=" + shellQuote(g.sock) + " \"$@\"\n"
	if err := os.WriteFile(g.wrapper, []byte(script), 0o755); err != nil {
		return nil, err
	}
	return g, nil
}

func (g *gvisorSession) clearSocket() {
	_ = os.Remove(g.sock)
}

func (g *gvisorSession) waitForSocket(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		if info, err := os.Stat(g.sock); err == nil && info.Mode()&os.ModeSocket != 0 {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("the proxy did not create its netstack socket %s within %s", g.sock, timeout)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
