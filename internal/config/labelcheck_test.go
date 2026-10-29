package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func hasNoticeFlag(argv []string) bool {
	for i := 0; i+1 < len(argv); i++ {
		if argv[i] == "--append-system-prompt" && argv[i+1] == "{network_policy}" {
			return true
		}
	}
	return false
}

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
		if cfg.Version != Version {
			t.Errorf("%s: label version is %d, want %d", cf, cfg.Version, Version)
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
		if filepath.Base(filepath.Dir(cf)) == "pi" {
			for _, argv := range [][]string{cfg.Agent.Launch, cfg.Agent.Command} {
				if !hasNoticeFlag(argv) {
					t.Errorf("%s: %q does not pass the network notice with --append-system-prompt {network_policy}", cf, argv)
				}
			}
		}
		for _, f := range cfg.Agent.ContextFiles {
			if !filepath.IsAbs(f) && f != "~" && !strings.HasPrefix(f, "~/") {
				t.Errorf("%s: context file %q must be absolute or start with ~/", cf, f)
			}
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
