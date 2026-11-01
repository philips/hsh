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

func TestStripControl(t *testing.T) {
	got := stripControl("HTTP/1.1 200 OK\x1b]52;c;ZXZpbA==\x07\x1b[2J\r\u009b31m")
	if got != "HTTP/1.1 200 OK]52;c;ZXZpbA==[2J31m" {
		t.Fatalf("stripControl = %q", got)
	}
}

func TestOAuthStatusAllSessions(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	store := sessionOAuthStore("abc123")
	if err := os.MkdirAll(filepath.Dir(store), 0o700); err != nil {
		t.Fatal(err)
	}
	doc := `{"hsh_aa": {"provider": "anthropic", "field": "access_token", "value": "real-secret", "created": "2025-06-11T18:02:14Z"}}`
	if err := os.WriteFile(store, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(filepath.Dir(filepath.Dir(store)), "..", "nocaptures"), 0o700); err != nil {
		t.Fatal(err)
	}
	got := runOAuthStatus(t)
	if !strings.Contains(got, "session abc123") || !strings.Contains(got, "anthropic:") || strings.Contains(got, "nocaptures") {
		t.Fatalf("oauth status across sessions:\n%s", got)
	}
	if strings.Contains(got, "real-secret") {
		t.Fatalf("oauth status printed token material:\n%s", got)
	}
	if got := runOAuthStatus(t, "--session", "hsh-abc123"); !strings.Contains(got, "1 phantom(s)") {
		t.Fatalf("oauth status --session:\n%s", got)
	}
}

func TestOAuthStatusNoSessions(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	if got := runOAuthStatus(t); !strings.Contains(got, "no session has captured tokens") {
		t.Fatalf("oauth status without sessions:\n%s", got)
	}
}
