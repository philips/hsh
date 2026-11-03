package workspace

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestApplySeedsWritesOnlyMissingFiles(t *testing.T) {
	seedDir := t.TempDir()
	home := t.TempDir()
	fresh := filepath.Join(home, ".claude", ".credentials.json")
	existing := filepath.Join(home, "kept.json")
	if err := os.WriteFile(existing, []byte("client wrote this"), 0o600); err != nil {
		t.Fatal(err)
	}
	manifest := []SeedEntry{
		{File: "0", Path: fresh, Mode: 0o600},
		{File: "1", Path: existing, Mode: 0o600},
		{File: "../escape", Path: filepath.Join(home, "escape"), Mode: 0o600},
	}
	data, _ := json.Marshal(manifest)
	for name, content := range map[string]string{SeedManifest: string(data), "0": "seeded", "1": "seeded"} {
		if err := os.WriteFile(filepath.Join(seedDir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := applySeeds(seedDir); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(fresh); string(got) != "seeded" {
		t.Fatalf("missing file not seeded: %q", got)
	}
	if fi, _ := os.Stat(fresh); fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v", fi.Mode())
	}
	if got, _ := os.ReadFile(existing); string(got) != "client wrote this" {
		t.Fatalf("an existing file must never be overwritten: %q", got)
	}
	if _, err := os.Stat(filepath.Join(home, "escape")); err == nil {
		t.Fatal("a manifest entry naming a file outside the seed dir must be ignored")
	}
	if err := applySeeds(filepath.Join(seedDir, "missing")); err != nil {
		t.Fatalf("no seed dir is not an error: %v", err)
	}
}
