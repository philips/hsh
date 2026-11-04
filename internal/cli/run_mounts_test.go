package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"hsh/internal/config"
)

func TestSessionMountsNoWorkspaceDefault(t *testing.T) {
	cfg := config.Default()
	mounts, source, warning, err := sessionMounts(cfg, "", "the label")
	if err != nil {
		t.Fatal(err)
	}
	if len(mounts) != 0 || source != "" || warning != "" {
		t.Fatalf("got mounts %v, source %q, warning %q; want none", mounts, source, warning)
	}
	if cfg.Mounts == nil || len(cfg.Mounts) != 0 {
		t.Fatalf("cfg.Mounts = %#v, want an empty non-nil list", cfg.Mounts)
	}
}

func TestSessionMountsNoWorkspaceCustom(t *testing.T) {
	cfg := config.Default()
	cfg.Mounts = []string{".:/workspace", "/etc/hosts:/hosts:ro"}
	mounts, _, warning, err := sessionMounts(cfg, "", "config file /x.hsh")
	if err != nil {
		t.Fatal(err)
	}
	if len(mounts) != 0 {
		t.Fatalf("mounts = %v, want none", mounts)
	}
	for _, want := range []string{".:/workspace", "/etc/hosts:/hosts:ro", "config file /x.hsh", "no workspace directory"} {
		if !strings.Contains(warning, want) {
			t.Errorf("warning %q does not mention %q", warning, want)
		}
	}
	if len(cfg.Mounts) != 0 {
		t.Fatalf("cfg.Mounts = %v, want empty", cfg.Mounts)
	}
}

func TestSessionMountsWorkspace(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Default()
	cfg.Mounts = []string{".:/workspace", "cache:/cache:ro"}
	mounts, source, warning, err := sessionMounts(cfg, dir, "the label")
	if err != nil {
		t.Fatal(err)
	}
	if warning != "" {
		t.Fatalf("unexpected warning %q", warning)
	}
	if source != dir {
		t.Fatalf("source = %q, want %q", source, dir)
	}
	if len(mounts) != 2 || mounts[0].Target != "/workspace" || mounts[1].Source != filepath.Join(dir, "cache") || mounts[1].Options != "ro" {
		t.Fatalf("mounts = %+v", mounts)
	}
	want := []string{dir + ":/workspace", filepath.Join(dir, "cache") + ":/cache:ro"}
	if strings.Join(cfg.Mounts, ",") != strings.Join(want, ",") {
		t.Fatalf("cfg.Mounts = %v, want %v", cfg.Mounts, want)
	}
}

func TestNoWorkspaceSessionConfigRoundTrip(t *testing.T) {
	cfg := config.Default()
	if _, _, _, err := sessionMounts(cfg, "", "the label"); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	loaded, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	mounts, err := loaded.ParsedMounts(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(mounts) != 0 {
		t.Fatalf("resumed session config has mounts %v, want none", mounts)
	}
}
