package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"hsh/internal/monitordb"
)

func writePolicyFile(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "p.json")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestPolicyAddApplyUnapply(t *testing.T) {
	proxySession(t)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "config"))

	out, _ := runOut(t, "policy", "ls")
	if !strings.Contains(out, "no policies in") {
		t.Fatalf("ls with no policies: %q", out)
	}
	src := writePolicyFile(t, `{"version":1,"description":"corp rules","rules":[
		{"hosts":["b.example","*.ok.example"],"access":"allow"},
		{"hosts":["a.example"],"access":"deny"},
		{"hosts":["*.sentry.example.com"],"log":"record"}
	]}`)
	out, err := runOut(t, "policy", "add", "corp", src)
	if err != nil || !strings.Contains(out, "added policy corp") || !strings.Contains(out, "allow              b.example, *.ok.example") || !strings.Contains(out, "log record         *.sentry.example.com") {
		t.Fatalf("add: %q %v", out, err)
	}
	if _, err := runOut(t, "policy", "add", "corp", src); err == nil || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("a second add without --force: %v", err)
	}
	if _, err := runOut(t, "policy", "add", "--force", "corp", src); err != nil {
		t.Fatal(err)
	}
	out, _ = runOut(t, "policy", "ls")
	if !strings.Contains(out, "corp  3      corp rules") {
		t.Fatalf("ls: %q", out)
	}
	out, _ = runOut(t, "policy", "show", "corp")
	if !strings.Contains(out, `"description": "corp rules"`) {
		t.Fatalf("show: %q", out)
	}

	if err := monitordb.DefaultStore().Allow("s1", []string{"a.example"}); err != nil {
		t.Fatal(err)
	}
	out, err = runOut(t, "policy", "apply", "--session", "s1", "corp")
	if err != nil || !strings.Contains(out, "applied policy corp to session s1: 4 host rule(s)") {
		t.Fatalf("apply: %q %v", out, err)
	}
	if _, err := runOut(t, "policy", "apply", "--session", "s1", "corp"); err != nil {
		t.Fatalf("reapplying: %v", err)
	}
	if got := rulesOf(t); got != "config:keep.example=allow,user:a.example=allow,policy:corp:b.example=allow,policy:corp:*.ok.example=allow,policy:corp:a.example=deny,policy:corp:*.sentry.example.com=" {
		t.Fatalf("rules = %s", got)
	}
	out, _ = runOut(t, "proxy", "rules", "--session", "s1")
	if !strings.Contains(out, "*.sentry.example.com  -       record  policy:corp") || !strings.Contains(out, "a.example             allow   -       user") {
		t.Fatalf("proxy rules: %q", out)
	}
	out, _ = runOut(t, "proxy", "denied", "--all", "--session", "s1")
	if !strings.Contains(out, "https://a.example  denied by policy corp") || !strings.Contains(out, "https://b.example  allowed") {
		t.Fatalf("a policy deny does not beat the user allow: %q", out)
	}
	out, _ = runOut(t, "proxy", "allow", "--session", "s1", "https://a.example")
	if out != "already allowed https://a.example\n" && !strings.Contains(out, "stays denied by policy corp") {
		t.Fatalf("allow under a policy deny: %q", out)
	}

	out, err = runOut(t, "policy", "unapply", "--session", "s1", "corp")
	if err != nil || out != "removed 4 rule(s) of policy corp from session s1\n" {
		t.Fatalf("unapply: %q %v", out, err)
	}
	if got := rulesOf(t); got != "config:keep.example=allow,user:a.example=allow" {
		t.Fatalf("rules after unapply = %s", got)
	}
	if _, err := runOut(t, "policy", "unapply", "--session", "s1", "corp"); err == nil || !strings.Contains(err.Error(), "not applied") {
		t.Fatalf("a second unapply: %v", err)
	}

	out, err = runOut(t, "policy", "rm", "corp")
	if err != nil || !strings.Contains(out, "removed policy corp") {
		t.Fatalf("rm: %q %v", out, err)
	}
	if _, err := runOut(t, "policy", "apply", "--session", "s1", "corp"); err == nil || !strings.Contains(err.Error(), `no policy "corp"`) {
		t.Fatalf("applying a removed policy: %v", err)
	}
}

func TestPolicyCommandsRefuseBadInput(t *testing.T) {
	proxySession(t)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "config"))
	bad := writePolicyFile(t, `{"version":1,"rules":[{"hosts":["*.com"],"access":"allow"}]}`)
	for _, args := range [][]string{
		{"policy", "add", "x", bad},
		{"policy", "add", "Bad_Name", writePolicyFile(t, `{"version":1,"rules":[{"hosts":["a.example"],"access":"allow"}]}`)},
		{"policy", "add", "x"},
		{"policy", "show", "../x"},
		{"policy", "rm", "missing"},
		{"policy", "apply", "--session", "missing", "x"},
		{"proxy", "rules", "--session", "missing"},
	} {
		if _, err := runOut(t, args...); err == nil {
			t.Errorf("%v was accepted", args)
		}
	}
	if _, err := os.Stat(filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "hsh", "policy", "network", "x.json")); err == nil {
		t.Error("an invalid policy was written")
	}
}
