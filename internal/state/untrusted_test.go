package state

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestReadUntrustedRefusesSymlinksFIFOsAndLargeFiles(t *testing.T) {
	dir := t.TempDir()
	secret := filepath.Join(dir, "secret")
	if err := os.WriteFile(secret, []byte("host secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(secret, link); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadUntrusted(link, 1<<20); err == nil {
		t.Fatal("followed a symlink")
	}
	fifo := filepath.Join(dir, "fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := ReadUntrusted(fifo, 1<<20); done <- err }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("read a FIFO")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("a FIFO blocked the reader")
	}
	if _, err := ReadUntrusted(secret, 4); err == nil || !strings.Contains(err.Error(), "larger than") {
		t.Fatalf("size limit: %v", err)
	}
}

func TestUntrustedEntriesAreResolvedWithoutFollowingSymlinks(t *testing.T) {
	root := t.TempDir()
	proxyDir := filepath.Join(root, "proxy")
	victimDir := filepath.Join(root, "victim")
	for _, d := range []string{proxyDir, victimDir} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	victim := filepath.Join(victimDir, "ant-login")
	if err := os.WriteFile(victim, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victimDir, filepath.Join(proxyDir, "refresh-wanted")); err != nil {
		t.Fatal(err)
	}
	if UntrustedEntryExists(proxyDir, "refresh-wanted", "ant-login") {
		t.Fatal("followed a symlinked directory")
	}
	if err := RemoveUntrustedEntry(proxyDir, "refresh-wanted", "ant-login"); err == nil {
		t.Fatal("removed through a symlinked directory")
	}
	if _, err := os.Stat(victim); err != nil {
		t.Fatalf("the victim file is gone: %v", err)
	}

	if err := os.Remove(filepath.Join(proxyDir, "refresh-wanted")); err != nil {
		t.Fatal(err)
	}
	wanted := filepath.Join(proxyDir, "refresh-wanted")
	if err := os.Mkdir(wanted, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, filepath.Join(wanted, "ant-login")); err != nil {
		t.Fatal(err)
	}
	if !UntrustedEntryExists(proxyDir, "refresh-wanted", "ant-login") {
		t.Fatal("the marker is not seen")
	}
	if err := RemoveUntrustedEntry(proxyDir, "refresh-wanted", "ant-login"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(victim); err != nil {
		t.Fatalf("removing a symlinked marker removed its target: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(wanted, "ant-login")); err == nil {
		t.Fatal("the marker is still there")
	}
}
