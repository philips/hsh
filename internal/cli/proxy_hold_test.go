package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"hsh/internal/config"
	"hsh/internal/denieddb"
)

func TestProxyHoldCommand(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	dir := denieddb.SessionDir("s1")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(dir, "config.json")
	if err := os.WriteFile(cfgPath, []byte(`{"image":"x","network":{"mode":"allowlist","allow":["keep.example"]}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) (string, error) {
		var err error
		out := captureStdout(t, func() { err = Run(append([]string{"proxy", "hold", "--session", "s1"}, args...)) })
		return out, err
	}
	wait := func() string {
		cfg, err := config.Load(cfgPath)
		if err != nil {
			t.Fatal(err)
		}
		return cfg.Network.HoldSummary()
	}

	out, err := run()
	if err != nil || !strings.Contains(out, "held for up to 30s") || !strings.Contains(out, "(default)") {
		t.Fatalf("status: %q %v", out, err)
	}
	if out, err = run("off"); err != nil || !strings.Contains(out, "holding is off") || wait() != "holding: off" {
		t.Fatalf("off: %q %v %s", out, err, wait())
	}
	if out, err = run("45s"); err != nil || !strings.Contains(out, "45s") || wait() != "holding: 45s" {
		t.Fatalf("45s: %q %v %s", out, err, wait())
	}
	if out, err = run("on"); err != nil || !strings.Contains(out, "(default)") || wait() != "holding: 30s" {
		t.Fatalf("on: %q %v %s", out, err, wait())
	}
	if _, err = run("forever"); err == nil {
		t.Fatal("an invalid duration was accepted")
	}
	if _, err = run("on", "off"); err == nil {
		t.Fatal("two arguments were accepted")
	}
	data, _ := os.ReadFile(cfgPath)
	if !strings.Contains(string(data), "keep.example") {
		t.Fatalf("the allowlist was lost: %s", data)
	}
	err = Run([]string{"proxy", "hold", "--session", "nosuch", "off"})
	if err == nil {
		t.Fatal("a missing session was accepted")
	}
}
