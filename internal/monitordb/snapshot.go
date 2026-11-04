package monitordb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"syscall"
	"time"
)

const (
	pendingByte  = 0x40000000
	reservedByte = pendingByte + 1
	sharedFirst  = pendingByte + 2
	sharedSize   = 510
)

var errBusy = errors.New("database is locked by its writer")
var errHotJournal = errors.New("database has an unfinished write")

func lockRange(fd int, typ int16, start, length int64) error {
	return syscall.FcntlFlock(uintptr(fd), syscall.F_SETLK, &syscall.Flock_t{Type: typ, Whence: io.SeekStart, Start: start, Len: length})
}

func sharedLock(fd int) error {
	if err := lockRange(fd, syscall.F_RDLCK, pendingByte, 1); err != nil {
		return err
	}
	err := lockRange(fd, syscall.F_RDLCK, sharedFirst, sharedSize)
	_ = lockRange(fd, syscall.F_UNLCK, pendingByte, 1)
	return err
}

func reservedHeld(fd int) bool {
	l := syscall.Flock_t{Type: syscall.F_WRLCK, Whence: io.SeekStart, Start: reservedByte, Len: 1}
	if err := syscall.FcntlFlock(uintptr(fd), syscall.F_GETLK, &l); err != nil {
		return true
	}
	return l.Type != syscall.F_UNLCK
}

func lockBusy(err error) bool {
	return errors.Is(err, syscall.EAGAIN) || errors.Is(err, syscall.EACCES)
}

func snapshotFile(path string, limit int64, wait time.Duration) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
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
	fd := int(f.Fd())
	deadline := time.Now().Add(wait)
	for {
		err := sharedLock(fd)
		if err == nil {
			break
		}
		if !lockBusy(err) {
			return nil, err
		}
		if time.Now().After(deadline) {
			return nil, errBusy
		}
		time.Sleep(10 * time.Millisecond)
	}
	defer lockRange(fd, syscall.F_UNLCK, sharedFirst, sharedSize)
	if jfi, err := os.Lstat(path + "-journal"); err == nil && jfi.Size() > 0 && !reservedHeld(fd) {
		return nil, errHotJournal
	}
	if fi, err = f.Stat(); err != nil {
		return nil, err
	}
	if fi.Size() == 0 || fi.Size() > limit {
		return nil, fmt.Errorf("%s has size %d (want 1 to %d bytes)", path, fi.Size(), limit)
	}
	data := make([]byte, fi.Size())
	if _, err := f.ReadAt(data, 0); err != nil {
		return nil, err
	}
	return data, nil
}

type deserializer interface {
	Deserialize([]byte) error
}

func withSnapshot(data []byte, fn func(*sql.Conn) error) error {
	db, err := sql.Open("sqlite", "file::memory:?_pragma=trusted_schema(0)")
	if err != nil {
		return err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if err := conn.Raw(func(dc any) error {
		d, ok := dc.(deserializer)
		if !ok {
			return errors.New("the sqlite driver cannot load a database from memory")
		}
		return d.Deserialize(data)
	}); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, "PRAGMA query_only = 1"); err != nil {
		return err
	}
	return fn(conn)
}
