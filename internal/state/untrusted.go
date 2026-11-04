package state

import (
	"fmt"
	"io"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

const MaxUntrustedFile = 8 << 20

func ReadUntrusted(path string, max int64) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", path)
	}
	if fi.Size() > max {
		return nil, fmt.Errorf("%s is larger than %d bytes", path, max)
	}
	data, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > max {
		return nil, fmt.Errorf("%s is larger than %d bytes", path, max)
	}
	return data, nil
}

func openUntrustedDir(dir, sub string) (int, error) {
	dfd, err := unix.Open(dir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, err
	}
	defer unix.Close(dfd)
	return unix.Openat(dfd, sub, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
}

func UntrustedEntryExists(dir, sub, name string) bool {
	fd, err := openUntrustedDir(dir, sub)
	if err != nil {
		return false
	}
	defer unix.Close(fd)
	var st unix.Stat_t
	return unix.Fstatat(fd, name, &st, unix.AT_SYMLINK_NOFOLLOW) == nil
}

func RemoveUntrustedEntry(dir, sub, name string) error {
	fd, err := openUntrustedDir(dir, sub)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	return unix.Unlinkat(fd, name, 0)
}
