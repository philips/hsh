package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"hsh/internal/monitordb"
	"hsh/internal/monitordb/monitordbtest"
	"hsh/internal/state"
)

func proxySession(t *testing.T) string {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", filepath.Join(t.TempDir(), "state"))
	dir := state.SessionDir("s1")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := `{"version":1,"network":{"mode":"allowlist","allow":["keep.example"]}}`
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := monitordb.InitSession(dir, []string{"keep.example"}); err != nil {
		t.Fatal(err)
	}
	if err := monitordbtest.Seed(dir, monitordb.DeniedHost{Host: "a.example"}, monitordb.DeniedHost{Host: "b.example"}); err != nil {
		t.Fatal(err)
	}
	return dir
}

func runOut(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var err error
	out := captureStdout(t, func() { err = Run(args) })
	return out, err
}

func rulesOf(t *testing.T) string {
	t.Helper()
	rules, err := monitordb.DefaultStore().Rules("s1")
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, r := range rules {
		out = append(out, r.Source+":"+r.Pattern+"="+r.Access)
	}
	return strings.Join(out, ",")
}

func TestProxyDenyAndAllowWriteSessionRules(t *testing.T) {
	dir := proxySession(t)
	before, _ := os.ReadFile(filepath.Join(dir, "config.json"))

	out, err := runOut(t, "proxy", "deny", "--session", "s1", "https://A.example/x")
	if err != nil || out != "denied a.example\n" {
		t.Fatalf("deny: %q %v", out, err)
	}
	if _, err := runOut(t, "proxy", "deny", "--session", "s1", "*.wild.example"); err != nil {
		t.Fatal(err)
	}
	if got := rulesOf(t); got != "config:keep.example=allow,user:a.example=deny,user:*.wild.example=deny" {
		t.Fatalf("rules = %s", got)
	}

	out, err = runOut(t, "proxy", "denied", "--all", "--session", "s1")
	if err != nil || !strings.Contains(out, "https://a.example  denied by you") || !strings.Contains(out, "https://b.example  pending") {
		t.Fatalf("denied --all: %q %v", out, err)
	}
	out, _ = runOut(t, "proxy", "denied", "--session", "s1")
	if strings.TrimSpace(out) != "https://b.example" {
		t.Fatalf("a denied host is still pending: %q", out)
	}

	out, err = runOut(t, "proxy", "allow", "--session", "s1", "--all")
	if err != nil || out != "added https://b.example\n" {
		t.Fatalf("allow --all: %q %v", out, err)
	}
	out, err = runOut(t, "proxy", "allow", "--session", "s1", "https://keep.example")
	if err != nil || out != "already allowed https://keep.example\n" {
		t.Fatalf("allow of a copied host: %q %v", out, err)
	}
	out, err = runOut(t, "proxy", "allow", "--session", "s1", "https://api.wild.example")
	if err != nil || out != "added https://api.wild.example, but it stays denied by you\n" {
		t.Fatalf("allow under a wildcard deny: %q %v", out, err)
	}
	out, err = runOut(t, "proxy", "allow", "--session", "s1", "https://a.example")
	if err != nil || out != "added https://a.example\n" {
		t.Fatalf("allow after deny: %q %v", out, err)
	}
	if got := rulesOf(t); got != "config:keep.example=allow,user:a.example=allow,user:*.wild.example=deny,user:b.example=allow,user:api.wild.example=allow" {
		t.Fatalf("rules = %s", got)
	}
	after, _ := os.ReadFile(filepath.Join(dir, "config.json"))
	if string(after) != string(before) {
		t.Fatalf("the session config was edited:\n%s", after)
	}
}

func TestProxyDenyRefusesBadInput(t *testing.T) {
	proxySession(t)
	for _, args := range [][]string{
		{"proxy", "deny", "a.example"},
		{"proxy", "deny", "--session", "s1"},
		{"proxy", "deny", "--session", "s1", "*.*.example"},
		{"proxy", "deny", "--session", "missing", "a.example"},
	} {
		if _, err := runOut(t, args...); err == nil {
			t.Errorf("%v was accepted", args)
		}
	}
	if _, err := runOut(t, "proxy", "denied", "--all", "--watch"); err == nil {
		t.Error("--all with --watch was accepted")
	}
}
