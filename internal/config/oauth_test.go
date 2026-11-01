package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestOAuthConfigNamesProviders(t *testing.T) {
	cfg, err := decode([]byte(`{"version": 1, "oauth": {"providers": ["openai-codex", "anthropic"], "api_hosts": {"openai-codex": ["chatgpt.com"]}}}`), "test")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(cfg.OAuth.Providers, []string{"openai-codex", "anthropic"}) {
		t.Fatalf("providers = %v", cfg.OAuth.Providers)
	}
	resolved, warnings, err := ResolveOAuth(cfg, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 || len(resolved) != 2 {
		t.Fatalf("resolved = %+v, warnings = %v", resolved, warnings)
	}
	if !reflect.DeepEqual(resolved[0].APIHosts, []string{"chatgpt.com"}) || resolved[0].Name != "openai-codex" {
		t.Fatalf("narrowed = %+v", resolved[0])
	}
	if resolved[1].TokenEndpoint.Host != "platform.claude.com" || resolved[1].Prefix != "Bearer " {
		t.Fatalf("anthropic = %+v", resolved[1])
	}
}

func TestOAuthConfigRejectsInlineDefinitions(t *testing.T) {
	_, err := decode([]byte(`{"version": 1, "oauth": {"openai": {"token_endpoint": {"host": "evil.example", "path": "/t"}, "api_hosts": ["evil.example"]}}}`), "label")
	if err == nil || !strings.Contains(err.Error(), "names OAuth providers instead of defining them") || !strings.Contains(err.Error(), `"openai"`) {
		t.Fatalf("err = %v", err)
	}
}

func TestOAuthConfigValidation(t *testing.T) {
	cases := map[string]string{
		`{"version": 1, "oauth": {"providers": ["a", "a"]}}`:                       "listed twice",
		`{"version": 1, "oauth": {"providers": ["a"], "api_hosts": {"b": ["x"]}}}`: "not in oauth.providers",
		`{"version": 1, "oauth": {"providers": ["../x"]}}`:                         "invalid provider name",
		`{"version": 1, "oauth": {"providers": ["a"], "extra": 1}}`:                "unknown field",
	}
	for doc, want := range cases {
		if _, err := decode([]byte(doc), "test"); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v, want %q", doc, err, want)
		}
	}
}

func TestResolveOAuthErrorsAndWarnings(t *testing.T) {
	dir := t.TempDir()
	cfg, err := decode([]byte(`{"version": 1, "oauth": {"providers": ["nope"]}}`), "test")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := ResolveOAuth(cfg, dir); err == nil || !strings.Contains(err.Error(), "unknown oauth provider") {
		t.Fatalf("err = %v", err)
	}
	cfg, _ = decode([]byte(`{"version": 1, "oauth": {"providers": ["anthropic"], "api_hosts": {"anthropic": ["evil.example"]}}}`), "test")
	if _, _, err := ResolveOAuth(cfg, dir); err == nil || !strings.Contains(err.Error(), "never widen") {
		t.Fatalf("widening err = %v", err)
	}
	stale := `{"version": 1, "base": "sha256:00", "token_endpoint": {"host": "platform.claude.com", "path": "/v1/oauth/token"}, "response_fields": ["access_token"], "api": {"hosts": ["api.anthropic.com"]}}`
	if err := os.WriteFile(filepath.Join(dir, "anthropic.json"), []byte(stale), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, _ = decode([]byte(`{"version": 1, "oauth": {"providers": ["anthropic"]}}`), "test")
	_, warnings, err := ResolveOAuth(cfg, dir)
	if err != nil || len(warnings) != 1 {
		t.Fatalf("stale override: warnings = %v, err = %v", warnings, err)
	}
}
