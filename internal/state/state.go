package state

import (
	"os"
	"path/filepath"
)

func Dir() string {
	if v := os.Getenv("XDG_STATE_HOME"); v != "" {
		return filepath.Join(v, "hsh")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "hsh-state"
	}
	return filepath.Join(home, ".local", "state", "hsh")
}

func SessionsDir() string {
	return filepath.Join(Dir(), "sessions")
}

func SessionDir(session string) string {
	return filepath.Join(SessionsDir(), session)
}
