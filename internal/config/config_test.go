package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseSecretRef(t *testing.T) {
	tests := []struct {
		from   string
		scheme string
		name   string
		errMsg string
	}{
		{from: "MISTRAL_API_KEY", scheme: "env", name: "MISTRAL_API_KEY"},
		{from: "env://MISTRAL_API_KEY", scheme: "env", name: "MISTRAL_API_KEY"},
		{from: "hsh://mistral-api-key", scheme: "hsh", name: "mistral-api-key"},
		{from: "", errMsg: "empty"},
		{from: "env://", errMsg: "no name"},
		{from: "hsh://", errMsg: "no name"},
		{from: "dotenv://.env", errMsg: "unknown secret reference scheme"},
		{from: "keychain://icloud", errMsg: "unknown secret reference scheme"},
	}
	for _, tt := range tests {
		scheme, name, err := ParseSecretRef(tt.from)
		if tt.errMsg != "" {
			if err == nil || !strings.Contains(err.Error(), tt.errMsg) {
				t.Errorf("ParseSecretRef(%q) error = %v, want containing %q", tt.from, err, tt.errMsg)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseSecretRef(%q) unexpected error: %v", tt.from, err)
			continue
		}
		if scheme != tt.scheme || name != tt.name {
			t.Errorf("ParseSecretRef(%q) = (%q, %q), want (%q, %q)", tt.from, scheme, name, tt.scheme, tt.name)
		}
	}
}

type fakeStore struct {
	values map[string]string
	hosts  map[string][]string
}

func (f *fakeStore) Get(name string) (string, error) {
	if v, ok := f.values[name]; ok {
		return v, nil
	}
	return "", ErrSecretNotFound
}

func (f *fakeStore) SecretHosts(name string) ([]string, error) {
	return f.hosts[name], nil
}

type brokenStore struct{}

func (brokenStore) Get(name string) (string, error) {
	return "", errors.New("database is on fire")
}

func TestResolveSecretsRefs(t *testing.T) {
	cfg := &Config{Secrets: map[string]Secret{
		"STORED":  {From: "hsh://stored-key", Hosts: []string{"s.example"}, Header: "Authorization", Prefix: "Bearer "},
		"MISSING": {From: "hsh://gone", Hosts: []string{"m.example"}},
		"OPT":     {From: "hsh://also-gone", Hosts: []string{"o.example"}, Optional: true},
	}}
	store := &fakeStore{values: map[string]string{"stored-key": "store-value"}, hosts: map[string][]string{"stored-key": {"s.example"}}}

	res, err := ResolveSecrets(cfg, store)
	if err != nil {
		t.Fatalf("ResolveSecrets: %v", err)
	}
	if len(res.Secrets.Secrets) != 1 {
		t.Fatalf("resolved = %+v", res.Secrets.Secrets)
	}
	if v := res.Secrets.Secrets[0]; v.Name != "STORED" || v.Value != "store-value" || v.Header != "Authorization" || v.Prefix != "Bearer " {
		t.Errorf("STORED resolved wrong: %+v", v)
	}
	if strings.Join(res.Missing, ",") != "hsh://gone" {
		t.Errorf("missing = %v", res.Missing)
	}
	if len(res.Unapproved) != 0 {
		t.Errorf("unapproved = %+v", res.Unapproved)
	}
}

func TestResolveSecretsBindsStaticSecretsToApprovedHosts(t *testing.T) {
	cfg := &Config{Secrets: map[string]Secret{
		"K":     {From: "hsh://key", Hosts: []string{"api.example.com", "evil.example", "*.svc.example.com", "*.example.org"}},
		"OTHER": {From: "hsh://unbound", Hosts: []string{"api.example.com"}},
	}}
	store := &fakeStore{
		values: map[string]string{"key": "v", "unbound": "w"},
		hosts:  map[string][]string{"key": {"api.example.com", "*.example.com"}},
	}
	res, err := ResolveSecrets(cfg, store)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Secrets.Secrets) != 1 || res.Secrets.Secrets[0].Name != "K" {
		t.Fatalf("a secret with no approved host for its routes was resolved: %+v", res.Secrets.Secrets)
	}
	if got := strings.Join(res.Secrets.Secrets[0].Hosts, ","); got != "api.example.com,*.svc.example.com" {
		t.Fatalf("injected for %s", got)
	}
	want := []UnapprovedRoute{
		{Secret: "K", Store: "key", Hosts: []string{"evil.example", "*.example.org"}},
		{Secret: "OTHER", Store: "unbound", Hosts: []string{"api.example.com"}},
	}
	if len(res.Unapproved) != 2 || strings.Join(res.Unapproved[0].Hosts, ",") != "evil.example,*.example.org" || res.Unapproved[1].Store != "unbound" {
		t.Fatalf("unapproved = %+v, want %+v", res.Unapproved, want)
	}
	routes, err := UnapprovedRoutes(cfg, store)
	if err != nil || len(routes) != 2 || routes[0].Secret != "K" || routes[1].Secret != "OTHER" {
		t.Fatalf("UnapprovedRoutes = %+v %v", routes, err)
	}
}

func TestResolveSecretsWithoutHostApprovalsInjectsNothing(t *testing.T) {
	cfg := &Config{Secrets: map[string]Secret{"K": {From: "hsh://key", Hosts: []string{"a.example"}}}}
	res, err := ResolveSecrets(cfg, plainStore{"key": "v"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Secrets.Secrets) != 0 || len(res.Unapproved) != 1 {
		t.Fatalf("%+v", res)
	}
}

type plainStore map[string]string

func (p plainStore) Get(name string) (string, error) {
	if v, ok := p[name]; ok {
		return v, nil
	}
	return "", ErrSecretNotFound
}

func TestResolveSecretsNilStore(t *testing.T) {
	cfg := &Config{Secrets: map[string]Secret{
		"STORED": {From: "hsh://stored-key", Hosts: []string{"s.example"}},
	}}
	res, err := ResolveSecrets(cfg, nil)
	if err != nil {
		t.Fatalf("ResolveSecrets with nil store: %v", err)
	}
	if len(res.Missing) != 1 || res.Missing[0] != "hsh://stored-key" {
		t.Errorf("missing = %v, want [hsh://stored-key]", res.Missing)
	}
}

func TestResolveSecretsStoreFailure(t *testing.T) {
	cfg := &Config{Secrets: map[string]Secret{
		"STORED": {From: "hsh://stored-key"},
	}}
	_, err := ResolveSecrets(cfg, brokenStore{})
	if err == nil {
		t.Fatal("expected an error from a failing store")
	}
}

func TestEnvironmentReferencesAreRefused(t *testing.T) {
	for _, from := range []string{"env://AWS_SECRET_ACCESS_KEY", "AWS_SECRET_ACCESS_KEY"} {
		label := `{"version":1,"secrets":{"K":{"from":"` + from + `","hosts":["evil.example"]}}}`
		_, err := FromImageLabel(label)
		if !errors.Is(err, ErrEnvReference) || !strings.Contains(err.Error(), "hsh secret add aws-secret-access-key --host HOST") {
			t.Errorf("label with %s: %v", from, err)
		}
		_, err = Load(writeConfig(t, label))
		if !errors.Is(err, ErrEnvReference) {
			t.Errorf("config file with %s: %v", from, err)
		}
	}
}

func TestNormalizeSecretHost(t *testing.T) {
	for in, want := range map[string]string{
		"api.example.com":            "api.example.com",
		"API.Example.com.":           "api.example.com",
		"https://api.example.com/v1": "api.example.com",
		"api.example.com:443":        "api.example.com",
		"*.Example.com":              "*.example.com",
		"127.0.0.1":                  "127.0.0.1",
		"host.containers.internal":   "host.containers.internal",
		"[::1]":                      "::1",
	} {
		got, err := NormalizeSecretHost(in)
		if err != nil || got != want {
			t.Errorf("NormalizeSecretHost(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "*", "*.", "a b", "a..b", "evil\x1b.example", "-a.example", "a.*.example"} {
		if got, err := NormalizeSecretHost(bad); err == nil {
			t.Errorf("NormalizeSecretHost(%q) = %q, want an error", bad, got)
		}
	}
}

func TestHostCovered(t *testing.T) {
	approved := []string{"api.example.com", "*.svc.example.org"}
	for host, want := range map[string]bool{
		"api.example.com":     true,
		"API.example.com.":    true,
		"x.svc.example.org":   true,
		"*.svc.example.org":   true,
		"*.a.svc.example.org": true,
		"svc.example.org":     false,
		"*.example.org":       false,
		"*.example.com":       false,
		"other.example.com":   false,
	} {
		if got := HostCovered(approved, host); got != want {
			t.Errorf("HostCovered(%q) = %v, want %v", host, got, want)
		}
	}
}

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.hsh")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadRejectsUnknownScheme(t *testing.T) {
	path := writeConfig(t, `{
  "version": 1,
  "secrets": { "API_KEY": { "from": "keyring://icloud", "hosts": ["a.example"] } }
}`)
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected load to fail on an unknown secret scheme")
	}
}

func TestFromImageLabelRejectsUnknownScheme(t *testing.T) {
	_, err := FromImageLabel(`{"version":1,"secrets":{"K":{"from":"keyring://icloud"}}}`)
	if err == nil {
		t.Fatal("expected label parsing to fail on an unknown secret scheme")
	}
}

func TestParseApprovalWait(t *testing.T) {
	cases := []struct {
		in      string
		want    time.Duration
		wantErr bool
	}{
		{"", DefaultApprovalWait, false},
		{"30s", 30 * time.Second, false},
		{"90s", 90 * time.Second, false},
		{"2m", 2 * time.Minute, false},
		{"5m", 5 * time.Minute, false},
		{"off", 0, false},
		{"OFF", 0, false},
		{"0", 0, false},
		{"0s", 0, false},
		{"6m", 0, true},
		{"-5s", 0, true},
		{"soon", 0, true},
		{"30", 0, true},
	}
	for _, c := range cases {
		got, err := ParseApprovalWait(c.in)
		if (err != nil) != c.wantErr || (err == nil && got != c.want) {
			t.Errorf("%q: got %v, %v", c.in, got, err)
		}
	}
}

func TestApprovalWaitLoadsValidatesAndRoundTrips(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "c.json")
	write := func(doc string) {
		if err := os.WriteFile(path, []byte(doc), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(`{"version":1,"network":{"mode":"allowlist"}}`)
	cfg, err := Load(path)
	if err != nil || cfg.Network.ApprovalWaitDuration() != DefaultApprovalWait {
		t.Fatalf("default: %v %v", cfg, err)
	}
	write(`{"version":1,"network":{"approval_wait":"off"}}`)
	if cfg, err = Load(path); err != nil || cfg.Network.ApprovalWaitDuration() != 0 {
		t.Fatalf("off: %v %v", cfg, err)
	}
	write(`{"version":1,"network":{"approval_wait":"10m"}}`)
	if _, err = Load(path); err == nil {
		t.Fatal("an over-limit wait was accepted")
	}
	if _, err = FromImageLabel(`{"version":1,"network":{"approval_wait":"nope"}}`); err == nil {
		t.Fatal("a bad wait in an image label was accepted")
	}

	write(`{"version":1,"image":"x","network":{"mode":"allowlist","allow":["a.example"]},"secrets":{}}`)
	if err := SetApprovalWait(path, "45s"); err != nil {
		t.Fatal(err)
	}
	cfg, err = Load(path)
	if err != nil || cfg.Network.ApprovalWaitDuration() != 45*time.Second || len(cfg.Network.Allow) != 1 || cfg.Network.Mode != "allowlist" {
		t.Fatalf("after setting 45s: %+v %v", cfg.Network, err)
	}
	if err := SetApprovalWait(path, "off"); err != nil {
		t.Fatal(err)
	}
	if cfg, _ = Load(path); cfg.Network.ApprovalWaitDuration() != 0 {
		t.Fatal("off did not stick")
	}
	if err := SetApprovalWait(path, ""); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if strings.Contains(string(data), "approval_wait") {
		t.Fatalf("clearing the override left the key behind: %s", data)
	}
	if err := SetApprovalWait(path, "forever"); err == nil {
		t.Fatal("an invalid value was written")
	}
}

func TestNoticeAndContextFilesFromLabel(t *testing.T) {
	cfg, err := FromImageLabel(`{"version":1,"agent":{"launch":["pi","--append-system-prompt","{network_policy}"],"context_files":["~/.codex/AGENTS.md"]}}`)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Network.NoticeEnabled() {
		t.Fatal("the notice must default to on")
	}
	if len(cfg.Agent.ContextFiles) != 1 || cfg.Agent.ContextFiles[0] != "~/.codex/AGENTS.md" {
		t.Fatalf("context_files = %q", cfg.Agent.ContextFiles)
	}
	if cfg.Agent.Launch[2] != "{network_policy}" {
		t.Fatalf("launch = %q", cfg.Agent.Launch)
	}
	cfg, err = FromImageLabel(`{"version":1,"network":{"notice":false}}`)
	if err != nil || cfg.Network.NoticeEnabled() {
		t.Fatalf("notice:false not honoured: %v", err)
	}
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"notice":false`) {
		t.Fatalf("notice:false lost in the session config copy: %s", data)
	}
}
