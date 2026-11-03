package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"hsh/internal/settings"
	"hsh/internal/state"
)

func wipeDirs(t *testing.T) (cfgDir, stateDir string) {
	t.Helper()
	root := t.TempDir()
	cfgDir = filepath.Join(root, "config")
	stateDir = filepath.Join(root, "state")
	t.Setenv("XDG_CONFIG_HOME", cfgDir)
	t.Setenv("XDG_STATE_HOME", stateDir)
	for _, p := range []string{
		filepath.Join(cfgDir, "hsh", "settings.json"),
		filepath.Join(cfgDir, "hsh", "secrets.db"),
		filepath.Join(cfgDir, "hsh", "oauth-providers", "github.json"),
		filepath.Join(stateDir, "hsh", "sessions", "53bd3fc7e5ba", "config.json"),
		filepath.Join(stateDir, "hsh", "monitor.db"),
		filepath.Join(stateDir, "hsh", "metrics.db"),
		filepath.Join(stateDir, "hsh", "crash.log"),
	} {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return filepath.Join(cfgDir, "hsh"), filepath.Join(stateDir, "hsh")
}

func runWipeTest(t *testing.T, answer string, pods []podInfo) (string, error) {
	t.Helper()
	wipeDirs(t)
	var out bytes.Buffer
	err := runWipe(strings.NewReader(answer), &out, pods, nil)
	return out.String(), err
}

func assertWiped(t *testing.T) {
	t.Helper()
	for _, dir := range []string{settings.Dir(), state.Dir()} {
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Errorf("%s still exists", dir)
		}
	}
}

func TestWipeYes(t *testing.T) {
	out, err := runWipeTest(t, "YES\n", nil)
	if err != nil {
		t.Fatalf("wipe: %v", err)
	}
	assertWiped(t)
	if !strings.Contains(out, "type YES to wipe") || !strings.Contains(out, "wiped hsh configuration and state") {
		t.Errorf("unexpected output: %q", out)
	}
}

func TestWipeRequiresExactYes(t *testing.T) {
	for _, answer := range []string{"yes\n", "Yes\n", "Y\n", "\n", "no\n", ""} {
		t.Run(answer, func(t *testing.T) {
			cfg, state := wipeDirs(t)
			var out bytes.Buffer
			if err := runWipe(strings.NewReader(answer), &out, nil, nil); err != nil {
				t.Fatalf("wipe: %v", err)
			}
			for _, dir := range []string{cfg, state} {
				if _, err := os.Stat(dir); err != nil {
					t.Errorf("%s removed after answering %q", dir, answer)
				}
			}
			if !strings.Contains(out.String(), "aborted, nothing removed") {
				t.Errorf("unexpected output: %q", out.String())
			}
		})
	}
}

func TestWipeRefusesWithSandboxes(t *testing.T) {
	out, err := runWipeTest(t, "YES\n", []podInfo{{Name: "hsh-53bd3fc7e5ba"}})
	if err == nil {
		t.Fatal("expected refusal error")
	}
	if !strings.Contains(out, "refusing to wipe") || !strings.Contains(out, "hsh rm --all --force") {
		t.Errorf("unexpected output: %q", out)
	}
	for _, dir := range []string{settings.Dir(), state.Dir()} {
		if _, err := os.Stat(dir); err != nil {
			t.Errorf("%s removed despite refusal", dir)
		}
	}
}

func TestWipePodmanCheckFailedStillWipes(t *testing.T) {
	wipeDirs(t)
	var out bytes.Buffer
	err := runWipe(strings.NewReader("YES\n"), &out, nil, errFakePodman{})
	if err != nil {
		t.Fatalf("wipe: %v", err)
	}
	assertWiped(t)
	if !strings.Contains(out.String(), "warning: could not check for existing sandboxes") {
		t.Errorf("missing warning: %q", out.String())
	}
}

type errFakePodman struct{}

func (errFakePodman) Error() string { return "podman is broken" }
