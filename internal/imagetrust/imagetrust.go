package imagetrust

import (
	"bufio"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"hsh/internal/settings"
)

func Path() string {
	return filepath.Join(settings.Dir(), "trusted-images")
}

func validID(id string) bool {
	if len(id) < 12 {
		return false
	}
	for _, c := range id {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func read(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var lines []string
	sc := bufio.NewScanner(strings.NewReader(string(data)))
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}
	return lines, sc.Err()
}

func Trusted(path, id string) (bool, error) {
	if !validID(id) {
		return false, nil
	}
	lines, err := read(path)
	if err != nil {
		return false, err
	}
	for _, line := range lines {
		if f := strings.Fields(line); len(f) > 0 && !strings.HasPrefix(f[0], "#") && f[0] == id {
			return true, nil
		}
	}
	return false, nil
}

func Trust(path, id, ref string) error {
	if !validID(id) {
		return fmt.Errorf("invalid image id %q", id)
	}
	if ok, err := Trusted(path, id); err != nil || ok {
		return err
	}
	lines, err := read(path)
	if err != nil {
		return err
	}
	if len(lines) == 0 {
		lines = append(lines, "# Images whose built-in hsh config (the org.ifup.hsh label) hsh run applies: image id, reference when trusted, time.")
	}
	ref = strings.Join(strings.Fields(ref), "_")
	lines = append(lines, fmt.Sprintf("%s %s %s", id, ref, time.Now().UTC().Format(time.RFC3339)))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
