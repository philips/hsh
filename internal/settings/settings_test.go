package settings

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadMissingIsEmpty(t *testing.T) {
	s, err := Load(filepath.Join(t.TempDir(), "settings.json"))
	if err != nil || s.MonitorKey != "" {
		t.Fatalf("got %+v %v", s, err)
	}
}

func TestSetMonitorKeyRoundTrips(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hsh", "settings.json")
	if err := SetMonitorKey(path, "ctrl-a"); err != nil {
		t.Fatal(err)
	}
	s, err := Load(path)
	if err != nil || s.MonitorKey != "ctrl-a" {
		t.Fatalf("got %+v %v", s, err)
	}
}

func TestLoadRejectsUnknownFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(path, []byte(`{"monitor_kye":"ctrl-a"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "monitor_kye") {
		t.Fatalf("want an unknown field error, got %v", err)
	}
	if err := SetMonitorKey(path, "ctrl-b"); err == nil {
		t.Fatal("SetMonitorKey overwrote a file it could not read")
	}
}

func TestPathFollowsXDGConfigHome(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/x")
	if got := Path(); got != "/x/hsh/settings.json" {
		t.Fatalf("got %s", got)
	}
}
