package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func runOAuthStatus(t *testing.T, args ...string) string {
	t.Helper()
	return captureStdout(t, func() {
		cmd := oauthCommand()
		if err := cmd.Parse(append([]string{"status"}, args...)); err != nil {
			t.Fatal(err)
		}
		if err := cmd.Run(context.Background()); err != nil {
			t.Fatal(err)
		}
	})
}

func TestOAuthStatus(t *testing.T) {
	store := filepath.Join(t.TempDir(), "oauth-tokens.json")
	doc := `{
  "hsh_aa": {"provider": "openai", "field": "access_token", "value": "real-secret", "created": "2025-06-11T18:02:14Z"},
  "hsh_bb": {"provider": "openai", "field": "refresh_token", "value": "real-secret", "created": "2025-06-11T18:02:14Z"}
}`
	if err := os.WriteFile(store, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	got := runOAuthStatus(t, "--store", store)
	for _, want := range []string{"openai:", "access_token", "refresh_token", "2 phantom(s)", "2025-06-11T18:02:14Z"} {
		if !strings.Contains(got, want) {
			t.Fatalf("oauth status output missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "real-secret") || strings.Contains(got, "hsh_aa") {
		t.Fatalf("oauth status printed token material or phantoms:\n%s", got)
	}
}

func TestOAuthStatusEmptyStore(t *testing.T) {
	store := filepath.Join(t.TempDir(), "oauth-tokens.json")
	if err := os.WriteFile(store, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := runOAuthStatus(t, "--store", store); !strings.Contains(got, "no captured tokens") {
		t.Fatalf("oauth status for an empty store:\n%s", got)
	}
}

func TestOAuthStatusMissingStore(t *testing.T) {
	store := filepath.Join(t.TempDir(), "oauth-tokens.json")
	if got := runOAuthStatus(t, "--store", store); !strings.Contains(got, "no captured tokens") {
		t.Fatalf("oauth status for a missing store:\n%s", got)
	}
}
