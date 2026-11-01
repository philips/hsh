package oauthprov

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestShippedDescriptorsAreValid(t *testing.T) {
	names := ShippedNames()
	want := []string{"anthropic", "openai", "openai-codex"}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("shipped = %v, want %v", names, want)
	}
	for _, n := range names {
		data, _ := Shipped(n)
		if _, err := Parse(data); err != nil {
			t.Errorf("%s: %v", n, err)
		}
	}
}

func TestLoadShippedOnly(t *testing.T) {
	l, err := Load(t.TempDir(), "anthropic")
	if err != nil {
		t.Fatal(err)
	}
	if l.Descriptor.TokenEndpoint.Host != "platform.claude.com" {
		t.Fatalf("host = %q", l.Descriptor.TokenEndpoint.Host)
	}
	if len(l.Sources) != 1 || l.Sources[0].Kind != SourceShipped {
		t.Fatalf("sources = %+v", l.Sources)
	}
	if len(l.Warnings) != 0 {
		t.Fatalf("warnings = %v", l.Warnings)
	}
}

func TestLoadDropInsInLexicalOrder(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "anthropic.d", "20-host.json"), `{"token_endpoint": {"host": "second.example"}}`)
	writeFile(t, filepath.Join(dir, "anthropic.d", "10-host.json"), `{"token_endpoint": {"host": "first.example"}, "request_nonce_fields": null}`)
	writeFile(t, filepath.Join(dir, "anthropic.d", "ignored.txt"), `{"token_endpoint": {"host": "never"}}`)
	l, err := Load(dir, "anthropic")
	if err != nil {
		t.Fatal(err)
	}
	if got := l.Descriptor.TokenEndpoint.Host; got != "second.example" {
		t.Fatalf("host = %q, want the later drop-in to win", got)
	}
	if got := l.Descriptor.TokenEndpoint.Path; got != "/v1/oauth/token" {
		t.Fatalf("path = %q, want it inherited from the shipped descriptor", got)
	}
	if l.Descriptor.RequestNonceFields != nil {
		t.Fatalf("a null in a drop-in must delete the field, got %v", l.Descriptor.RequestNonceFields)
	}
	if len(l.Sources) != 3 || l.Sources[1].Kind != SourceDropIn || !strings.HasSuffix(l.Sources[1].Path, "10-host.json") {
		t.Fatalf("sources = %+v", l.Sources)
	}
}

func TestLoadFullOverrideWithDropIn(t *testing.T) {
	dir := t.TempDir()
	shippedData, _ := Shipped("openai")
	var doc map[string]any
	if err := json.Unmarshal(shippedData, &doc); err != nil {
		t.Fatal(err)
	}
	doc["base"] = Hash(shippedData)
	doc["api"] = map[string]any{"hosts": []string{"api.openai.com", "extra.example"}}
	data, _ := json.Marshal(doc)
	writeFile(t, filepath.Join(dir, "openai.json"), string(data))
	writeFile(t, filepath.Join(dir, "openai.d", "50-local.json"), `{"api": {"header": "X-Token", "prefix": ""}}`)
	l, err := Load(dir, "openai")
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Warnings) != 0 {
		t.Fatalf("matching base must not warn: %v", l.Warnings)
	}
	r, err := l.Descriptor.Resolve("openai", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(r.APIHosts, []string{"api.openai.com", "extra.example"}) || r.Header != "X-Token" || r.Prefix != "" {
		t.Fatalf("resolved = %+v", r)
	}
	if l.Sources[0].Kind != SourceOverride || l.Sources[1].Kind != SourceDropIn {
		t.Fatalf("sources = %+v", l.Sources)
	}
}

func TestLoadStaleOverrideWarns(t *testing.T) {
	dir := t.TempDir()
	shippedData, _ := Shipped("openai")
	stale := strings.Replace(string(shippedData), `"version": 1,`, `"version": 1, "base": "sha256:0000",`, 1)
	writeFile(t, filepath.Join(dir, "openai.json"), stale)
	l, err := Load(dir, "openai")
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Warnings) != 1 || !strings.Contains(l.Warnings[0], "changed since") {
		t.Fatalf("warnings = %v", l.Warnings)
	}
	writeFile(t, filepath.Join(dir, "openai.json"), string(shippedData))
	l, err = Load(dir, "openai")
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Warnings) != 1 || !strings.Contains(l.Warnings[0], "no \"base\"") {
		t.Fatalf("warnings = %v", l.Warnings)
	}
}

func TestLoadUserProvider(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "mine.json"), `{"version": 1, "token_endpoint": {"host": "auth.example", "path": "/token"}, "response_fields": ["access_token"], "api": {"hosts": ["api.example"]}}`)
	l, err := Load(dir, "mine")
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Warnings) != 0 {
		t.Fatalf("a provider hsh does not ship has nothing to go stale against: %v", l.Warnings)
	}
	names, err := Names(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(names, []string{"anthropic", "mine", "openai", "openai-codex"}) {
		t.Fatalf("names = %v", names)
	}
}

func TestLoadErrors(t *testing.T) {
	dir := t.TempDir()
	cases := map[string]struct {
		name    string
		files   map[string]string
		wantErr string
	}{
		"unknown":       {name: "nope", wantErr: "unknown oauth provider"},
		"bad name":      {name: "../etc", wantErr: "invalid provider name"},
		"orphan dropin": {name: "orphan", files: map[string]string{"orphan.d/1.json": `{}`}, wantErr: "no descriptor to apply them to"},
		"newer":         {name: "newer", files: map[string]string{"newer.json": `{"version": 2}`}, wantErr: "newer than this hsh reads"},
		"no version":    {name: "nover", files: map[string]string{"nover.json": `{"token_endpoint": {}}`}, wantErr: "no \"version\""},
		"unknown field": {name: "extra", files: map[string]string{"extra.json": `{"version": 1, "token_endpoint": {"host": "a", "path": "/t"}, "response_fields": ["x"], "api": {"hosts": ["a"]}, "bogus": 1}`}, wantErr: "unknown field"},
		"bad kind":      {name: "kind", files: map[string]string{"kind.json": `{"version": 1, "token_endpoint": {"host": "a", "path": "/t"}, "response_fields": [{"path": "x", "kind": "magic"}], "api": {"hosts": ["a"]}}`}, wantErr: "unknown kind"},
		"bad format":    {name: "format", files: map[string]string{"format.json": `{"version": 1, "token_endpoint": {"host": "a", "path": "/t"}, "response_fields": [{"path": "x", "format": "sk-"}], "api": {"hosts": ["a"]}}`}, wantErr: "exactly once"},
		"no hosts":      {name: "nohosts", files: map[string]string{"nohosts.json": `{"version": 1, "token_endpoint": {"host": "a", "path": "/t"}, "response_fields": ["x"], "api": {"hosts": []}}`}, wantErr: "api.hosts is empty"},
	}
	for label, c := range cases {
		for p, content := range c.files {
			writeFile(t, filepath.Join(dir, p), content)
		}
		_, err := Load(dir, c.name)
		if err == nil || !strings.Contains(err.Error(), c.wantErr) {
			t.Errorf("%s: err = %v, want %q", label, err, c.wantErr)
		}
	}
}

func TestResolveNarrowing(t *testing.T) {
	l, err := Load(t.TempDir(), "openai-codex")
	if err != nil {
		t.Fatal(err)
	}
	r, err := l.Descriptor.Resolve("openai-codex", []string{"chatgpt.com"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(r.APIHosts, []string{"chatgpt.com"}) || r.Header != "Authorization" || r.Prefix != "Bearer " {
		t.Fatalf("resolved = %+v", r)
	}
	if _, err := l.Descriptor.Resolve("openai-codex", []string{"evil.example"}); err == nil || !strings.Contains(err.Error(), "never widen") {
		t.Fatalf("widening err = %v", err)
	}
}

func TestMergePatch(t *testing.T) {
	var target, patch any
	_ = json.Unmarshal([]byte(`{"a": "b", "c": {"d": "e", "f": "g"}, "l": [1, 2]}`), &target)
	_ = json.Unmarshal([]byte(`{"a": "z", "c": {"f": null}, "l": [3]}`), &patch)
	got, _ := json.Marshal(MergePatch(target, patch))
	if string(got) != `{"a":"z","c":{"d":"e"},"l":[3]}` {
		t.Fatalf("merge = %s", got)
	}
	orig, _ := json.Marshal(target)
	if string(orig) != `{"a":"b","c":{"d":"e","f":"g"},"l":[1,2]}` {
		t.Fatalf("the target was mutated: %s", orig)
	}
}
