package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"hsh/internal/oauthprov"
)

func runOAuthProvider(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var runErr error
	out := captureStdout(t, func() {
		cmd := oauthCommand()
		if err := cmd.Parse(append([]string{"provider"}, args...)); err != nil {
			runErr = err
			return
		}
		runErr = cmd.Run(context.Background())
	})
	return out, runErr
}

func TestOAuthProviderLsAndCat(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	out, err := runOAuthProvider(t, "ls")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"anthropic", "openai-codex", "shipped", "platform.claude.com/v1/oauth/token"} {
		if !strings.Contains(out, want) {
			t.Fatalf("ls missing %q:\n%s", want, out)
		}
	}
	dropIn := filepath.Join(oauthProvidersDir(), "anthropic.d", "10-host.json")
	if err := os.MkdirAll(filepath.Dir(dropIn), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dropIn, []byte(`{"token_endpoint": {"host": "console.anthropic.com"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err = runOAuthProvider(t, "cat", "anthropic")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "# shipped: (built into hsh)") || !strings.Contains(out, "# drop-in: "+dropIn) || !strings.Contains(out, `"console.anthropic.com"`) {
		t.Fatalf("cat:\n%s", out)
	}
	out, err = runOAuthProvider(t, "cat", "--shipped", "anthropic")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "platform.claude.com") || !strings.Contains(out, "sha256:") {
		t.Fatalf("cat --shipped:\n%s", out)
	}
	out, _ = runOAuthProvider(t, "ls")
	if !strings.Contains(out, "1 drop-in(s)") {
		t.Fatalf("ls after a drop-in:\n%s", out)
	}
}

func TestOAuthProviderEdit(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("VISUAL", "true")
	if _, err := runOAuthProvider(t, "edit", "openai"); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(filepath.Join(oauthProvidersDir(), "openai.d", "50-local.json")); err != nil || strings.TrimSpace(string(data)) != "{}" {
		t.Fatalf("drop-in = %q, %v", data, err)
	}
	if _, err := runOAuthProvider(t, "edit", "--full", "openai"); err != nil {
		t.Fatal(err)
	}
	full, err := os.ReadFile(oauthprov.OverridePath(oauthProvidersDir(), "openai"))
	if err != nil {
		t.Fatal(err)
	}
	shipped, _ := oauthprov.Shipped("openai")
	if !strings.Contains(string(full), oauthprov.Hash(shipped)) {
		t.Fatalf("a full copy must record the shipped hash as base:\n%s", full)
	}
	if _, err := runOAuthProvider(t, "edit", "mine"); err == nil || !strings.Contains(err.Error(), "--full") {
		t.Fatalf("a drop-in for an unknown provider must point at --full, got %v", err)
	}
	if _, err := runOAuthProvider(t, "edit", "--full", "mine"); err != nil {
		t.Fatalf("a skeleton for a new provider must load: %v", err)
	}
	script := filepath.Join(t.TempDir(), "break.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho '{broken' > \"$1\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("VISUAL", script)
	if _, err := runOAuthProvider(t, "edit", "--drop-in", "60-bad.json", "openai"); err == nil || !strings.Contains(err.Error(), "does not load") {
		t.Fatalf("a broken edit must be reported, got %v", err)
	}
}

func TestAddSuggestedProviders(t *testing.T) {
	dir := t.TempDir()
	label := `{"mine": {"version": 1, "token_endpoint": {"host": "auth.example", "path": "/token"}, "response_fields": ["access_token"], "api": {"hosts": ["api.example"]}}}`
	var out bytes.Buffer
	if err := addSuggestedProviders(dir, label, nil, false, false, strings.NewReader("n\n"), &out); err != nil {
		t.Fatal(err)
	}
	if fileExists(oauthprov.OverridePath(dir, "mine")) || !strings.Contains(out.String(), "skipped") {
		t.Fatalf("declining must not write anything:\n%s", out.String())
	}
	out.Reset()
	if err := addSuggestedProviders(dir, label, nil, false, false, strings.NewReader("y\n"), &out); err != nil {
		t.Fatal(err)
	}
	if _, err := oauthprov.Load(dir, "mine"); err != nil {
		t.Fatalf("accepted provider does not load: %v\n%s", err, out.String())
	}
	if err := addSuggestedProviders(dir, label, nil, true, false, nil, &out); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("existing files must not be replaced without --force, got %v", err)
	}
	bad := `{"evil": {"version": 1, "token_endpoint": {"host": "a"}, "response_fields": ["x"], "api": {"hosts": ["a"]}}}`
	if err := addSuggestedProviders(dir, bad, nil, true, false, nil, &out); err == nil {
		t.Fatal("an invalid suggestion must be rejected")
	}
	if err := addSuggestedProviders(dir, "", nil, true, false, nil, &out); err == nil {
		t.Fatal("a missing label must be an error")
	}
}
