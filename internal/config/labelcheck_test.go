package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestHarnessImageLabels(t *testing.T) {
	files, err := filepath.Glob("../../images/*/hsh.json")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("no hsh.json files found under images/")
	}
	for _, cf := range files {
		data, err := os.ReadFile(cf)
		if err != nil {
			t.Fatal(err)
		}
		label := string(data)
		cfg, err := FromImageLabel(label)
		if err != nil {
			t.Errorf("%s: label does not parse or validate: %v", cf, err)
			continue
		}
		if cfg.Shell != "/bin/bash" {
			t.Errorf("%s: label shell is %q", cf, cfg.Shell)
		}
		if cfg.Network.ModeOrDefault() != "allowlist" {
			t.Errorf("%s: label network mode is %q, want allowlist", cf, cfg.Network.ModeOrDefault())
		}
		if len(cfg.Agent.Command) == 0 {
			t.Errorf("%s: label has no agent command", cf)
		}
		if len(cfg.Agent.Launch) == 0 {
			t.Errorf("%s: label has no agent launch (interactive sessions auto-launch the harness)", cf)
		}
		for name, secret := range cfg.Secrets {
			scheme, _, err := ParseSecretRef(secret.From)
			if err != nil {
				t.Errorf("%s: secret %s: %v", cf, name, err)
			}
			if scheme != "hsh" {
				t.Errorf("%s: secret %s references the %q store; harness images must use the hsh secret store (hsh://NAME), not the host environment", cf, name, scheme)
			}
		}
	}
}
