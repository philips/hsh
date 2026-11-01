package netsetup

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writePID(t *testing.T, dir, content string) {
	t.Helper()
	if err := os.WriteFile(PinPIDPath(dir), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func openFile(t *testing.T, path string) *os.File {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Skipf("open %s: %v", path, err)
	}
	t.Cleanup(func() { f.Close() })
	return f
}

func wantErr(t *testing.T, err error, substr string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), substr) {
		t.Fatalf("err = %v, want it to contain %q", err, substr)
	}
}

func TestOpenPinNotReady(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(PinNetnsPath(dir), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	_, _, err := OpenPin(dir, 200*time.Millisecond)
	if !errors.Is(err, ErrPinNotReady) {
		t.Fatalf("err = %v, want ErrPinNotReady", err)
	}
	if time.Since(start) < 200*time.Millisecond {
		t.Fatal("OpenPin did not wait")
	}
	_, _, err = OpenPin(filepath.Join(dir, "missing"), 0)
	if !errors.Is(err, ErrPinNotReady) {
		t.Fatalf("missing dir: err = %v, want ErrPinNotReady", err)
	}
}

func TestCheckPinRejectsRegularFile(t *testing.T) {
	dir := t.TempDir()
	path := PinNetnsPath(dir)
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := checkPin(openFile(t, path), dir)
	wantErr(t, err, "is not a namespace")
}

func TestCheckPinRejectsOtherNamespaceType(t *testing.T) {
	_, err := checkPin(openFile(t, "/proc/self/ns/uts"), t.TempDir())
	wantErr(t, err, "is not a network namespace")
}

func TestCheckPinRejectsOwnNetns(t *testing.T) {
	_, err := checkPin(openFile(t, "/proc/self/ns/net"), t.TempDir())
	wantErr(t, err, "is the pod's")
}

func otherNetns(t *testing.T) (*os.File, int) {
	t.Helper()
	cmd := exec.Command("unshare", "-Urn", "sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Skipf("unshare: %v", err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
	pid := cmd.Process.Pid
	self, _ := os.Readlink("/proc/self/ns/net")
	for i := 0; i < 50; i++ {
		if ns, err := os.Readlink(fmt.Sprintf("/proc/%d/ns/net", pid)); err == nil && ns != self {
			return openFile(t, fmt.Sprintf("/proc/%d/ns/net", pid)), pid
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Skip("unshare -Urn did not create a network namespace")
	return nil, 0
}

func TestCheckPinShellPID(t *testing.T) {
	pin, pid := otherNetns(t)
	dir := t.TempDir()

	_, err := checkPin(pin, dir)
	wantErr(t, err, "read the session shell pid")

	writePID(t, dir, "garbage\n")
	_, err = checkPin(pin, dir)
	wantErr(t, err, "malformed session shell pid")

	writePID(t, dir, fmt.Sprintf("%d\n", os.Getpid()))
	_, err = checkPin(pin, dir)
	wantErr(t, err, "is not running")

	writePID(t, dir, fmt.Sprintf("%d\n", pid))
	got, err := checkPin(pin, dir)
	if err != nil || got != pid {
		t.Fatalf("checkPin = %d, %v; want %d", got, err, pid)
	}
}
