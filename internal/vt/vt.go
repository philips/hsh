package vt

import "errors"

var ErrUnavailable = errors.New("terminal emulator unavailable: hsh was built without GOEXPERIMENT=simd")

type Emulator interface {
	Write(p []byte)
	Resize(cols, rows int) error
	Ground() bool
	Cursor() (x, y int)
	Plain() (string, error)
	Replay() ([]byte, error)
	Row(y int) (string, error)
	Close()
}

type Options struct {
	Cols       int
	Rows       int
	Scrollback int
}
