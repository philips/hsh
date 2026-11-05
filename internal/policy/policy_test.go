package policy

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"hsh/internal/monitordb"
)

const golang = `{
  "version": 1,
  "description": "Go toolchain and module proxy",
  "rules": [
    {"hosts": ["go.dev", "Proxy.Golang.org.", "sum.golang.org", "dl.google.com"], "access": "allow"}
  ]
}`

func TestParse(t *testing.T) {
	p, err := Parse([]byte(golang), "golang.json")
	if err != nil {
		t.Fatal(err)
	}
	if p.Description != "Go toolchain and module proxy" || len(p.Rules) != 1 || p.Rules[0].Hosts[1] != "proxy.golang.org" {
		t.Fatalf("%+v", p)
	}
	rec := `{"version":1,"recorded":{"image":"fedora:42","command":["dnf","-y","install","neovim"],"runs":3,"at":"2025-06-01T12:00:00Z","hsh":"v0.1.0"},"rules":[{"hosts":["a.example"],"access":"allow"}]}`
	if p, err := Parse([]byte(rec), "r"); err != nil || p.Recorded == nil || p.Recorded.Runs != 3 || len(p.Recorded.Command) != 4 {
		t.Fatalf("recorded: %+v %v", p, err)
	}
}

func TestParseRefuses(t *testing.T) {
	cases := map[string]string{
		`{"rules":[{"hosts":["a.example"],"access":"allow"}]}`:                           `no "version"`,
		`{"version":2,"rules":[{"hosts":["a.example"],"access":"allow"}]}`:               "newer than this hsh reads",
		`{"version":0,"rules":[{"hosts":["a.example"],"access":"allow"}]}`:               "not supported",
		`{"version":1,"rules":[]}`:                                                       "at least one rule",
		`{"version":1}`:                                                                  "at least one rule",
		`{"version":1,"rules":[{"hosts":["a.example"]}]}`:                                "sets access, log, or both",
		`{"version":1,"rules":[{"hosts":[],"access":"allow"}]}`:                          "hosts is empty",
		`{"version":1,"rules":[{"hosts":["*.com"],"access":"allow"}]}`:                   `invalid host pattern "*.com"`,
		`{"version":1,"rules":[{"hosts":["a.example"],"access":"maybe"}]}`:               `access "maybe"`,
		`{"version":1,"rules":[{"hosts":["a.example"],"log":"off"}]}`:                    `log "off"`,
		`{"version":1,"rules":[{"hosts":["a.example"],"access":"allow","paths":["/"]}]}`: `unknown field "paths"`,
		`{"version":1,"extra":1,"rules":[{"hosts":["a.example"],"access":"allow"}]}`:     `unknown field "extra"`,
		`{"version":1,"rules":[{"hosts":["a.example"],"access":"allow"}]} {}`:            "after top-level value",
		`{"version":1,"rules":[{"hosts":["a.example"],"access":"allow"}],}`:              "invalid character",
	}
	for in, want := range cases {
		if _, err := Parse([]byte(in), "p.json"); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", in, err, want)
		}
	}
}

func TestValidName(t *testing.T) {
	for _, n := range []string{"golang", "go-1", "a", "0x"} {
		if ValidName(n) != nil {
			t.Errorf("%q rejected", n)
		}
	}
	for _, n := range []string{"", "-a", "Go", "a_b", "a.json", "../x", strings.Repeat("a", 64)} {
		if ValidName(n) == nil {
			t.Errorf("%q accepted", n)
		}
	}
}

func TestSessionRulesMergePerPattern(t *testing.T) {
	p, err := Parse([]byte(`{"version":1,"rules":[
		{"hosts":["a.example","b.example"],"access":"allow"},
		{"hosts":["b.example"],"access":"deny"},
		{"hosts":["a.example","*.sentry.example.com"],"log":"review"},
		{"hosts":["a.example"],"log":"record"}
	]}`), "p")
	if err != nil {
		t.Fatal(err)
	}
	got := p.SessionRules("mix")
	want := []monitordb.Rule{
		{Pattern: "a.example", Source: "policy:mix", Access: "allow", Log: "record"},
		{Pattern: "b.example", Source: "policy:mix", Access: "deny"},
		{Pattern: "*.sentry.example.com", Source: "policy:mix", Log: "review"},
	}
	if len(got) != len(want) {
		t.Fatalf("%+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("rule %d: %+v want %+v", i, got[i], want[i])
		}
	}
}

func TestSaveLoadListRemove(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	p, err := Parse([]byte(golang), "x")
	if err != nil {
		t.Fatal(err)
	}
	if err := Save("golang", p, false); err != nil {
		t.Fatal(err)
	}
	if err := Save("golang", p, false); !errors.Is(err, ErrExists) {
		t.Fatalf("a second save without force: %v", err)
	}
	if err := Save("golang", p, true); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(Path("golang")); err != nil || fi.Mode().Perm() != 0o644 {
		t.Fatalf("%v %v", fi, err)
	}
	if err := os.WriteFile(filepath.Join(Dir(), "broken.json"), []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(Dir(), "notes.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	entries, err := List()
	if err != nil || len(entries) != 2 || entries[0].Name != "broken" || entries[0].Err == nil || entries[1].Name != "golang" || entries[1].Policy == nil {
		t.Fatalf("%+v %v", entries, err)
	}
	loaded, err := Load("golang")
	if err != nil || loaded.Rules[0].Hosts[1] != "proxy.golang.org" {
		t.Fatalf("%+v %v", loaded, err)
	}
	if _, err := Load("missing"); err == nil || !strings.Contains(err.Error(), "hsh policy add missing FILE") {
		t.Fatalf("missing: %v", err)
	}
	if _, err := Load("../etc"); err == nil {
		t.Fatal("a path was accepted as a name")
	}
	if err := Remove("golang"); err != nil {
		t.Fatal(err)
	}
	if err := Remove("golang"); err == nil {
		t.Fatal("removing a missing policy succeeded")
	}
}
