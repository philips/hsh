package attend

import (
	"os"
	"path/filepath"
	"time"
)

const (
	FileName   = "attended"
	MaxAge     = 20 * time.Second
	TouchEvery = 5 * time.Second
)

func Path(sessionDir string) string { return filepath.Join(sessionDir, FileName) }

func Touch(path string) error {
	now := time.Now()
	if err := os.Chtimes(path, now, now); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	return f.Close()
}

func Fresh(path string, maxAge time.Duration) bool {
	if path == "" {
		return false
	}
	fi, err := os.Stat(path)
	if err != nil {
		return false
	}
	return time.Since(fi.ModTime()) <= maxAge
}

func Run(path string, stop <-chan struct{}) {
	_ = Touch(path)
	t := time.NewTicker(TouchEvery)
	defer t.Stop()
	for {
		select {
		case <-stop:
			_ = os.Remove(path)
			return
		case <-t.C:
			_ = Touch(path)
		}
	}
}
