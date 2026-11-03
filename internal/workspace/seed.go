package workspace

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

const SeedDir = "/hsh-run/seed"

const SeedManifest = "manifest.json"

type SeedEntry struct {
	File string `json:"file"`
	Path string `json:"path"`
	Mode uint32 `json:"mode"`
}

func applySeeds(dir string) error {
	data, err := os.ReadFile(filepath.Join(dir, SeedManifest))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var entries []SeedEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		return fmt.Errorf("seed manifest: %w", err)
	}
	for _, e := range entries {
		if !filepath.IsAbs(e.Path) || filepath.Base(e.File) != e.File {
			continue
		}
		if _, err := os.Lstat(e.Path); err == nil || !errors.Is(err, fs.ErrNotExist) {
			continue
		}
		content, err := os.ReadFile(filepath.Join(dir, e.File))
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(e.Path), 0o700); err != nil {
			return err
		}
		mode := fs.FileMode(e.Mode)
		if mode == 0 {
			mode = 0o600
		}
		if err := os.WriteFile(e.Path, content, mode); err != nil {
			return err
		}
	}
	return nil
}
