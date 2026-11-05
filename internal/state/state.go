package state

import (
	"os"
	"path/filepath"
	"strings"
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

func nameFile(session string) string {
	return filepath.Join(SessionDir(session), "name")
}

func ReadName(session string) string {
	data, err := os.ReadFile(nameFile(session))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func WriteName(session, name string) error {
	path := nameFile(session)
	if name == "" {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".name-*")
	if err != nil {
		return err
	}
	if _, err := tmp.WriteString(name + "\n"); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return nil
}

func Names() map[string]string {
	names := map[string]string{}
	entries, err := os.ReadDir(SessionsDir())
	if err != nil {
		return names
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if n := ReadName(e.Name()); n != "" {
			names[e.Name()] = n
		}
	}
	return names
}
