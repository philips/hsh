package workspace

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/sys/unix"
)

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

func RestrictThread() error {
	if err := dropCapsKeep(unix.CAP_DAC_OVERRIDE); err != nil {
		return err
	}
	return unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0)
}

func CheckRestricted() error {
	nnp, err := unix.PrctlRetInt(unix.PR_GET_NO_NEW_PRIVS, 0, 0, 0, 0)
	if err != nil {
		return err
	}
	hdr := &unix.CapUserHeader{Version: unix.LINUX_CAPABILITY_VERSION_3}
	data := make([]unix.CapUserData, 2)
	if err := unix.Capget(hdr, &data[0]); err != nil {
		return err
	}
	keep := uint32(1) << unix.CAP_DAC_OVERRIDE
	if nnp != 1 || data[0].Permitted&^keep != 0 || data[1].Permitted != 0 || data[0].Inheritable|data[1].Inheritable != 0 {
		return fmt.Errorf("not restricted (no_new_privs=%d, permitted=%08x%08x)", nnp, data[1].Permitted, data[0].Permitted)
	}
	return nil
}

type ShellSnapshot struct {
	Shell string   `json:"shell"`
	Env   []string `json:"env"`
	Dir   string   `json:"dir"`
}

func (s ShellSnapshot) WriteMemfd() (*os.File, error) {
	data, err := json.Marshal(s)
	if err != nil {
		return nil, err
	}
	fd, err := unix.MemfdCreate("hsh-sshd-snapshot", unix.MFD_ALLOW_SEALING)
	if err != nil {
		return nil, fmt.Errorf("create the snapshot memfd: %w", err)
	}
	f := os.NewFile(uintptr(fd), "hsh-sshd-snapshot")
	if _, err := f.Write(data); err != nil {
		f.Close()
		return nil, err
	}
	if _, err := unix.FcntlInt(uintptr(fd), unix.F_ADD_SEALS, unix.F_SEAL_SEAL|unix.F_SEAL_SHRINK|unix.F_SEAL_GROW|unix.F_SEAL_WRITE); err != nil {
		f.Close()
		return nil, fmt.Errorf("seal the snapshot memfd: %w", err)
	}
	return f, nil
}

func ReadSnapshot(fd int) (ShellSnapshot, error) {
	f := os.NewFile(uintptr(fd), "hsh-sshd-snapshot")
	if f == nil {
		return ShellSnapshot{}, fmt.Errorf("no snapshot on fd %d", fd)
	}
	defer f.Close()
	var snap ShellSnapshot
	if err := json.NewDecoder(io.NewSectionReader(f, 0, 1<<24)).Decode(&snap); err != nil {
		return ShellSnapshot{}, fmt.Errorf("read the session shell snapshot: %w", err)
	}
	if snap.Shell == "" || snap.Dir == "" {
		return ShellSnapshot{}, fmt.Errorf("incomplete session shell snapshot")
	}
	return snap, nil
}

func SnapshotShell(pid int) ShellSnapshot {
	env, _ := os.ReadFile(fmt.Sprintf("/proc/%d/environ", pid))
	snap := ShellSnapshot{Env: parseEnviron(env), Dir: "/"}
	snap.Shell = envValue(snap.Env, "HSH_SHELL")
	if snap.Shell == "" {
		snap.Shell = "/bin/sh"
	}
	if cwd, err := os.Readlink(fmt.Sprintf("/proc/%d/cwd", pid)); err == nil {
		if fi, err := os.Stat(cwd); err == nil && fi.IsDir() {
			snap.Dir = cwd
		}
	}
	return snap
}

func (s ShellSnapshot) EnvWithTerm(term string) []string {
	env := append([]string(nil), s.Env...)
	if term != "" {
		env = setEnv(env, "TERM", term)
	}
	return env
}
