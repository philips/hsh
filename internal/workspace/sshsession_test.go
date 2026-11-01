package workspace

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestSnapshotMemfdRoundTrip(t *testing.T) {
	want := ShellSnapshot{Shell: "/bin/bash", Env: []string{"HSH_SHELL=/bin/bash", "A=b=c"}, Dir: "/workspace"}
	f, err := want.WriteMemfd()
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.Write([]byte("x")); err == nil {
		t.Fatal("the snapshot memfd is writable after sealing")
	}
	fd, err := unix.Dup(int(f.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	got, err := ReadSnapshot(fd)
	if err != nil {
		t.Fatal(err)
	}
	if got.Shell != want.Shell || got.Dir != want.Dir || strings.Join(got.Env, "\n") != strings.Join(want.Env, "\n") {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestReadSnapshotRejectsIncomplete(t *testing.T) {
	f, err := ShellSnapshot{Env: []string{"A=b"}}.WriteMemfd()
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	fd, _ := unix.Dup(int(f.Fd()))
	if _, err := ReadSnapshot(fd); err == nil {
		t.Fatal("accepted a snapshot without shell and dir")
	}
}

func TestSnapshotShell(t *testing.T) {
	cmd := exec.Command("sleep", "30")
	cmd.Env = []string{"HSH_SHELL=/bin/zsh", "PATH=/usr/bin:/bin", "X=1"}
	cmd.Dir = t.TempDir()
	if err := cmd.Start(); err != nil {
		t.Skip(err)
	}
	defer func() { cmd.Process.Kill(); cmd.Wait() }()
	for i := 0; i < 100; i++ {
		if data, _ := os.ReadFile(fmt.Sprintf("/proc/%d/environ", cmd.Process.Pid)); strings.Contains(string(data), "HSH_SHELL=") {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	snap := SnapshotShell(cmd.Process.Pid)
	if snap.Shell != "/bin/zsh" || snap.Dir != cmd.Dir || envValue(snap.Env, "X") != "1" {
		t.Fatalf("snapshot = %+v", snap)
	}
	if got := envValue(snap.EnvWithTerm("xterm-ghostty"), "TERM"); got != "xterm-ghostty" {
		t.Fatalf("TERM = %q", got)
	}
}

func TestSnapshotShellMissingProcess(t *testing.T) {
	snap := SnapshotShell(1 << 30)
	if snap.Shell != "/bin/sh" || snap.Dir != "/" {
		t.Fatalf("snapshot = %+v", snap)
	}
}

func TestCheckRestrictedRejectsNoNewPrivsUnset(t *testing.T) {
	nnp, _ := unix.PrctlRetInt(unix.PR_GET_NO_NEW_PRIVS, 0, 0, 0, 0)
	if nnp != 0 {
		t.Skip("the test process already has no_new_privs")
	}
	if err := CheckRestricted(); err == nil {
		t.Fatal("an unrestricted process passed CheckRestricted")
	}
}
