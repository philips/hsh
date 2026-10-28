//go:build !goexperiment.simd

package vt

const Backend = "none"

func Available() bool { return false }

func New(Options) (Emulator, error) { return nil, ErrUnavailable }
