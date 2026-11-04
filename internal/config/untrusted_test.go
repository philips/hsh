package config

import (
	"strings"
	"testing"
)

func TestRestrictUntrustedImage(t *testing.T) {
	cfg, err := FromImageLabel(`{"version":1,"mounts":["/home/u/.ssh:/x", ".:/workspace"],"network":{"mode":"https","allow":["a.example","b.example"],"approval_wait":"30s"},"oauth":{"providers":["openai"]},"secrets":{"K":{"from":"hsh://k","hosts":["a.example"]}},"agent":{"command":["pi"]}}`)
	if err != nil {
		t.Fatal(err)
	}
	dropped := cfg.RestrictUntrustedImage()
	got := strings.Join(dropped, "; ")
	for _, want := range []string{"network.allow (a.example, b.example)", "network.mode https (using allowlist)", "oauth.providers (openai)", "mounts (/home/u/.ssh:/x, .:/workspace; using .:/workspace)"} {
		if !strings.Contains(got, want) {
			t.Errorf("dropped %q lacks %q", got, want)
		}
	}
	if len(cfg.Network.Allow) != 0 || cfg.Network.Mode != "allowlist" || len(cfg.OAuth.Providers) != 0 || strings.Join(cfg.Mounts, ",") != ".:/workspace" {
		t.Fatalf("not restricted: %+v", cfg)
	}
	if cfg.Network.ApprovalWait != "30s" || len(cfg.Secrets) != 1 || cfg.Agent.Command[0] != "pi" {
		t.Fatalf("restricted more than the trust-sensitive fields: %+v", cfg)
	}
	if again := cfg.RestrictUntrustedImage(); len(again) != 0 {
		t.Fatalf("a restricted config reports drops again: %v", again)
	}
}
