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
}

func (f *fakeStore) Get(name string) (string, error) {
	if v, ok := f.values[name]; ok {
		return v, nil
	}
	return "", ErrSecretNotFound
}

type brokenStore struct{}

func (brokenStore) Get(name string) (string, error) {
	return "", errors.New("database is on fire")
}

func TestResolveSecretsRefs(t *testing.T) {
	cfg := &Config{Secrets: map[string]Secret{
		"API_KEY": {From: "env://HSH_TEST_ENV_KEY", Hosts: []string{"a.example"}, Header: "Authorization", Prefix: "Bearer "},
		"LEGACY":  {From: "HSH_TEST_LEGACY", Hosts: []string{"l.example"}},
		"STORED":  {From: "hsh://stored-key", Hosts: []string{"s.example"}},
		"MISSING": {From: "hsh://gone", Hosts: []string{"m.example"}},
		"UNSET":   {From: "env://HSH_TEST_DEFINITELY_UNSET", Hosts: []string{"u.example"}},
	}}
	t.Setenv("HSH_TEST_ENV_KEY", "env-value")
	t.Setenv("HSH_TEST_LEGACY", "legacy-value")
	store := &fakeStore{values: map[string]string{"stored-key": "store-value"}}

	rs, missing, err := ResolveSecrets(cfg, store)
	if err != nil {
		t.Fatalf("ResolveSecrets: %v", err)
	}
	byName := map[string]ResolvedSecret{}
	for _, s := range rs.Secrets {
		byName[s.Name] = s
	}
	if v := byName["API_KEY"]; v.Value != "env-value" || v.Header != "Authorization" || v.Prefix != "Bearer " {
		t.Errorf("API_KEY resolved wrong: %+v", v)
	}
	if v := byName["LEGACY"]; v.Value != "legacy-value" || v.Header != "LEGACY" {
		t.Errorf("LEGACY resolved wrong: %+v", v)
	}
	if v := byName["STORED"]; v.Value != "store-value" {
		t.Errorf("STORED resolved wrong: %+v", v)
	}
	if _, ok := byName["MISSING"]; ok {
		t.Error("MISSING should not be resolved")
	}
	if _, ok := byName["UNSET"]; ok {
		t.Error("UNSET should not be resolved")
	}
	if len(missing) != 2 {
		t.Errorf("missing = %v, want 2 entries", missing)
	}
	wantMissing := map[string]bool{"hsh://gone": true, "env://HSH_TEST_DEFINITELY_UNSET": true}
	for _, m := range missing {
		if !wantMissing[m] {
			t.Errorf("unexpected missing entry %q", m)
		}
	}
}

func TestResolveSecretsNilStore(t *testing.T) {
	cfg := &Config{Secrets: map[string]Secret{
		"STORED": {From: "hsh://stored-key", Hosts: []string{"s.example"}},
	}}
	_, missing, err := ResolveSecrets(cfg, nil)
	if err != nil {
		t.Fatalf("ResolveSecrets with nil store: %v", err)
	}
	if len(missing) != 1 || missing[0] != "hsh://stored-key" {
		t.Errorf("missing = %v, want [hsh://stored-key]", missing)
	}
}

func TestResolveSecretsStoreFailure(t *testing.T) {
	cfg := &Config{Secrets: map[string]Secret{
		"STORED": {From: "hsh://stored-key"},
	}}
	_, _, err := ResolveSecrets(cfg, brokenStore{})
	if err == nil {
		t.Fatal("expected an error from a failing store")
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

func TestAddSecretPreservesOtherFields(t *testing.T) {
	path := writeConfig(t, `{
  "version": 1,
  "image": "localhost/hsh-example:latest",
  "shell": "/bin/bash",
  "network": { "mode": "allowlist", "allow": ["api.mistral.ai"] }
}
`)
	err := AddSecret(path, "API_KEY", Secret{
		From:   "hsh://my-key",
		Hosts:  []string{"api.mistral.ai"},
		Header: "Authorization",
		Prefix: "Bearer ",
	})
	if err != nil {
		t.Fatalf("AddSecret: %v", err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load after AddSecret: %v", err)
	}
	s, ok := cfg.Secrets["API_KEY"]
	if !ok {
		t.Fatal("API_KEY was not added to the config")
	}
	if s.From != "hsh://my-key" || s.Header != "Authorization" || s.Prefix != "Bearer " || len(s.Hosts) != 1 || s.Hosts[0] != "api.mistral.ai" {
		t.Fatalf("added secret is wrong: %+v", s)
	}
	if cfg.Image != "localhost/hsh-example:latest" || cfg.Shell != "/bin/bash" {
		t.Fatalf("other fields were clobbered: %+v", cfg)
	}
	if len(cfg.Network.Allow) != 1 || cfg.Network.Allow[0] != "api.mistral.ai" {
		t.Fatalf("network.allow was clobbered: %+v", cfg.Network)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) == "" || data[len(data)-1] != '\n' {
		t.Error("config file should end with a newline")
	}
}

func TestAddSecretUpdatesExisting(t *testing.T) {
	path := writeConfig(t, `{
  "version": 1,
  "secrets": { "API_KEY": { "from": "env://OLD", "hosts": ["a.example"], "header": "X-Old" } }
}
`)
	if err := AddSecret(path, "API_KEY", Secret{From: "hsh://new", Hosts: []string{"b.example"}}); err != nil {
		t.Fatalf("AddSecret: %v", err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	s := cfg.Secrets["API_KEY"]
	if s.From != "hsh://new" || s.Header != "" || len(s.Hosts) != 1 || s.Hosts[0] != "b.example" {
		t.Fatalf("secret was not replaced: %+v", s)
	}
}

func TestFindSecretRefs(t *testing.T) {
	path := writeConfig(t, `{
  "version": 1,
  "secrets": {
    "API_KEY": { "from": "hsh://my-key", "hosts": ["a.example"] },
    "OTHER": { "from": "hsh://other-key", "hosts": ["b.example"] }
  }
}
`)
	refs, err := FindSecretRefs(path, "hsh://my-key")
	if err != nil {
		t.Fatalf("FindSecretRefs: %v", err)
	}
	if len(refs) != 1 || refs[0] != "API_KEY" {
		t.Fatalf("refs = %v, want [API_KEY]", refs)
	}
	refs, err = FindSecretRefs(path, "hsh://nothing")
	if err != nil || len(refs) != 0 {
		t.Fatalf("refs = %v, err = %v; want empty", refs, err)
	}
	missingPath := filepath.Join(t.TempDir(), "nope.hsh")
	refs, err = FindSecretRefs(missingPath, "hsh://my-key")
	if err != nil || refs != nil {
		t.Fatalf("missing config should yield no refs, got %v, %v", refs, err)
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
