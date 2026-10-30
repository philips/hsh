package workspace

import (
	"fmt"
	"os"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

type SSHSessionOptions struct {
	Pty     bool
	Term    string
	Command string
}

func RunSSHSession(opts SSHSessionOptions) error {
	runtime.LockOSThread()
	pid, err := innerNetnsPID()
	if err != nil {
		return err
	}
	return runSessionShell(pid, true, opts)
}

func RunDirectSSHSession(opts SSHSessionOptions) error {
	return runSessionShell(1, false, opts)
}

func runSessionShell(pid int, enterNetns bool, opts SSHSessionOptions) error {
	env, _ := os.ReadFile(fmt.Sprintf("/proc/%d/environ", pid))
	envv := parseEnviron(env)
	shell := envValue(envv, "HSH_SHELL")
	if shell == "" {
		shell = "/bin/sh"
	}
	if opts.Term != "" {
		envv = setEnv(envv, "TERM", opts.Term)
	}
	if enterNetns {
		ns, err := os.Open(fmt.Sprintf("/proc/%d/ns/net", pid))
		if err != nil {
			return fmt.Errorf("open the session's network namespace: %w", err)
		}
		defer ns.Close()
		if err := unix.Setns(int(ns.Fd()), unix.CLONE_NEWNET); err != nil {
			return fmt.Errorf("enter the session's network namespace: %w (is the session's shell still running?)", err)
		}
	}
	if err := dropCapsKeep(unix.CAP_DAC_OVERRIDE); err != nil {
		return err
	}
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return err
	}
	if opts.Pty {
		_ = unix.IoctlSetInt(0, unix.TIOCSCTTY, 0)
	}
	if cwd, err := os.Readlink(fmt.Sprintf("/proc/%d/cwd", pid)); err == nil {
		_ = os.Chdir(cwd)
	}
	if opts.Command != "" {
		return syscall.Exec(shell, []string{shell, "-c", opts.Command}, envv)
	}
	return syscall.Exec(shell, []string{shell}, envv)
}

func innerNetnsPID() (int, error) {
	self, err := os.Stat("/proc/self/ns/net")
	if err != nil {
		return 0, err
	}
	selfIno := self.Sys().(*syscall.Stat_t).Ino
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return 0, err
	}
	pids := make([]int, 0, len(entries))
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		fi, err := os.Stat("/proc/" + e.Name() + "/ns/net")
		if err != nil {
			continue
		}
		if fi.Sys().(*syscall.Stat_t).Ino != selfIno {
			pids = append(pids, pid)
		}
	}
	if len(pids) == 0 {
		return 0, fmt.Errorf("no process of this session holds the inner network namespace (is the session's shell still running?)")
	}
	sort.Ints(pids)
	return pids[0], nil
}

func parseEnviron(data []byte) []string {
	var env []string
	for _, kv := range strings.Split(string(data), "\x00") {
		if kv != "" && strings.Contains(kv, "=") {
			env = append(env, kv)
		}
	}
	return env
}

func envValue(env []string, key string) string {
	prefix := key + "="
	for _, kv := range env {
		if strings.HasPrefix(kv, prefix) {
			return strings.TrimPrefix(kv, prefix)
		}
	}
	return ""
}

func RestrictPrivileges() error {
	if err := dropCapsKeep(unix.CAP_DAC_OVERRIDE); err != nil {
		return err
	}
	return unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0)
}
