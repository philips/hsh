package netsetup

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

const PinDir = "/run/hsh"

func PinNetnsPath(dir string) string { return filepath.Join(dir, "netns") }
func PinPIDPath(dir string) string   { return filepath.Join(dir, "shell.pid") }

func PinSession(pid int) error {
	const flags = unix.MS_NOSUID | unix.MS_NODEV | unix.MS_NOEXEC
	if err := os.MkdirAll(PinDir, 0o755); err != nil {
		return fmt.Errorf("pin the session: %w", err)
	}
	if err := unix.Mount("tmpfs", PinDir, "tmpfs", flags, "mode=0755,size=64k"); err != nil {
		return fmt.Errorf("pin the session: mount tmpfs on %s: %w", PinDir, err)
	}
	if err := os.WriteFile(PinPIDPath(PinDir), []byte(strconv.Itoa(pid)+"\n"), 0o444); err != nil {
		return fmt.Errorf("pin the session: %w", err)
	}
	if err := os.WriteFile(PinNetnsPath(PinDir), nil, 0o444); err != nil {
		return fmt.Errorf("pin the session: %w", err)
	}
	if err := unix.Mount(fmt.Sprintf("/proc/%d/ns/net", pid), PinNetnsPath(PinDir), "", unix.MS_BIND, ""); err != nil {
		return fmt.Errorf("pin the session: bind the network namespace: %w", err)
	}
	if err := unix.Mount("", PinDir, "", unix.MS_REMOUNT|unix.MS_RDONLY|flags, "mode=0755,size=64k"); err != nil {
		return fmt.Errorf("pin the session: remount %s read-only: %w", PinDir, err)
	}
	return nil
}

func OnPodSide() bool {
	_, err := netlink.LinkByName(HostVethName)
	return err == nil
}

var ErrPinNotReady = errors.New("the session network namespace is not pinned")

func pinReady(dir string) bool {
	var st unix.Statfs_t
	if err := unix.Statfs(dir, &st); err != nil {
		return false
	}
	return st.Type == unix.TMPFS_MAGIC && st.Flags&unix.ST_RDONLY != 0
}

func OpenPin(dir string, wait time.Duration) (*os.File, int, error) {
	deadline := time.Now().Add(wait)
	for !pinReady(dir) {
		if time.Now().After(deadline) {
			return nil, 0, ErrPinNotReady
		}
		time.Sleep(100 * time.Millisecond)
	}
	f, err := os.Open(PinNetnsPath(dir))
	if err != nil {
		return nil, 0, fmt.Errorf("%w: %v", ErrPinNotReady, err)
	}
	pid, err := checkPin(f, dir)
	if err != nil {
		f.Close()
		return nil, 0, err
	}
	return f, pid, nil
}

func checkPin(f *os.File, dir string) (int, error) {
	var sfs unix.Statfs_t
	if err := unix.Fstatfs(int(f.Fd()), &sfs); err != nil {
		return 0, fmt.Errorf("stat the pinned network namespace: %w", err)
	}
	if sfs.Type != unix.NSFS_MAGIC {
		return 0, fmt.Errorf("%s is not a namespace", f.Name())
	}
	kind, err := unix.IoctlRetInt(int(f.Fd()), unix.NS_GET_NSTYPE)
	if err != nil {
		return 0, fmt.Errorf("query the pinned namespace type: %w", err)
	}
	if kind != unix.CLONE_NEWNET {
		return 0, fmt.Errorf("%s is not a network namespace", f.Name())
	}
	pinned, err := fdIno(f)
	if err != nil {
		return 0, err
	}
	own, err := pathIno("/proc/self/ns/net")
	if err != nil {
		return 0, err
	}
	if pinned == own {
		return 0, fmt.Errorf("the pinned network namespace is the pod's, not the session's")
	}
	data, err := os.ReadFile(PinPIDPath(dir))
	if err != nil {
		return 0, fmt.Errorf("read the session shell pid: %w", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid < 1 {
		return 0, fmt.Errorf("malformed session shell pid in %s", PinPIDPath(dir))
	}
	shell, err := pathIno(fmt.Sprintf("/proc/%d/ns/net", pid))
	if err != nil || shell != pinned {
		return 0, fmt.Errorf("the session shell (pid %d) is not running", pid)
	}
	return pid, nil
}

type nsID struct{ dev, ino uint64 }

func fdIno(f *os.File) (nsID, error) {
	var st unix.Stat_t
	if err := unix.Fstat(int(f.Fd()), &st); err != nil {
		return nsID{}, err
	}
	return nsID{uint64(st.Dev), st.Ino}, nil
}

func pathIno(path string) (nsID, error) {
	var st unix.Stat_t
	if err := unix.Stat(path, &st); err != nil {
		return nsID{}, err
	}
	return nsID{uint64(st.Dev), st.Ino}, nil
}
