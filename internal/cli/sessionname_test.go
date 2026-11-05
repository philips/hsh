package cli

import (
	"os"
	"strings"
	"testing"

	"hsh/internal/state"
)

func withSessions(t *testing.T, names map[string]string, live ...string) {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	for id, name := range names {
		if err := os.MkdirAll(state.SessionDir(id), 0o755); err != nil {
			t.Fatal(err)
		}
		if name != "" {
			if err := state.WriteName(id, name); err != nil {
				t.Fatal(err)
			}
		}
	}
	liveSet := map[string]bool{}
	for _, id := range live {
		liveSet["hsh-"+id] = true
	}
	old := sessionPodExists
	sessionPodExists = func(name string) bool { return liveSet[name] }
	t.Cleanup(func() { sessionPodExists = old })
}

func TestValidateSessionName(t *testing.T) {
	withSessions(t, map[string]string{"53bd3fc7e5ba": "taken", "9f0000000000": "orphan"}, "53bd3fc7e5ba")
	for _, c := range []struct {
		name, self string
		errHas     string
	}{
		{"auth-refactor", "", ""},
		{"a", "", ""},
		{"a.b_c-1", "", ""},
		{strings.Repeat("a", 63), "", ""},
		{"orphan", "", ""},
		{"taken", "53bd3fc7e5ba", ""},
		{"", "", "empty"},
		{"Upper", "", "invalid session name"},
		{"-lead", "", "invalid session name"},
		{strings.Repeat("a", 64), "", "invalid session name"},
		{"53bd3fc7e5ba", "", "looks like a session id"},
		{"hsh-foo", "", `"hsh-"`},
		{"..", "", "invalid session name"},
		{"a/b", "", "path separator"},
		{"has space", "", "invalid session name"},
		{"taken", "", "is taken by session 53bd3fc7e5ba"},
		{"taken", "9f0000000000", "hsh resume taken"},
	} {
		err := validateSessionName(c.name, c.self)
		if c.errHas == "" {
			if err != nil {
				t.Errorf("%q: unexpected error %v", c.name, err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), c.errHas) {
			t.Errorf("%q: error %v, want %q", c.name, err, c.errHas)
		}
	}
}

func TestInitialSessionName(t *testing.T) {
	withSessions(t, nil)
	t.Setenv(sessionNameEnv, "")
	if n, err := initialSessionName(""); err != nil || n != "" {
		t.Fatalf("no name: %q %v", n, err)
	}
	t.Setenv(sessionNameEnv, "from-env")
	if n, err := initialSessionName(""); err != nil || n != "from-env" {
		t.Fatalf("env: %q %v", n, err)
	}
	if n, err := initialSessionName("from-flag"); err != nil || n != "from-flag" {
		t.Fatalf("flag beats env: %q %v", n, err)
	}
	t.Setenv(sessionNameEnv, "BAD")
	if _, err := initialSessionName(""); err == nil || !strings.Contains(err.Error(), sessionNameEnv) {
		t.Fatalf("bad env should name the variable: %v", err)
	}
	if n, err := initialSessionName("ok"); err != nil || n != "ok" {
		t.Fatalf("a valid flag ignores a bad env: %q %v", n, err)
	}
	if _, err := initialSessionName("BAD"); err == nil || !strings.Contains(err.Error(), "--name") {
		t.Fatalf("bad flag should name the flag: %v", err)
	}
}

func TestResolveSessionRef(t *testing.T) {
	withSessions(t, map[string]string{
		"53bd3fc7e5ba": "auth",
		"53cc00000000": "",
		"9f0000000000": "53",
		"aaaa00000000": "dup",
		"aaab00000000": "dup",
		"bbbb00000000": "gone",
	}, "53bd3fc7e5ba", "53cc00000000", "9f0000000000", "aaaa00000000", "aaab00000000")
	for _, c := range []struct {
		arg, want, errHas string
	}{
		{"auth", "53bd3fc7e5ba", ""},
		{"53bd3fc7e5ba", "53bd3fc7e5ba", ""},
		{"hsh-53bd3fc7e5ba", "53bd3fc7e5ba", ""},
		{"53bd", "53bd3fc7e5ba", ""},
		{"53", "9f0000000000", ""},
		{"53c", "53cc00000000", ""},
		{"dup", "", "matches sessions"},
		{"gone", "gone", ""},
		{"nothing", "nothing", ""},
		{"../x", "../x", ""},
	} {
		got, err := resolveSessionRef(c.arg)
		if c.errHas != "" {
			if err == nil || !strings.Contains(err.Error(), c.errHas) {
				t.Errorf("%q: %q, %v; want error %q", c.arg, got, err, c.errHas)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("%q: got %q, %v; want %q", c.arg, got, err, c.want)
		}
	}
}

func TestResolveSessionRefAmbiguousPrefix(t *testing.T) {
	withSessions(t, map[string]string{"53bd3fc7e5ba": "", "53cc00000000": ""}, "53bd3fc7e5ba", "53cc00000000")
	if _, err := resolveSessionRef("53"); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("got %v", err)
	}
}

func TestMatchPodsByName(t *testing.T) {
	withSessions(t, map[string]string{"53bd3fc7e5ba": "auth", "9f0000000000": "53", "aaaa00000000": "dup", "aaab00000000": "dup"})
	pods := []podInfo{
		{ID: "a", Name: "hsh-53bd3fc7e5ba"},
		{ID: "b", Name: "hsh-9f0000000000"},
		{ID: "c", Name: "hsh-aaaa00000000"},
		{ID: "d", Name: "hsh-aaab00000000"},
	}
	got, err := matchPods("auth", pods)
	if err != nil || len(got) != 1 || got[0].Session() != "53bd3fc7e5ba" {
		t.Fatalf("name: %v %v", got, err)
	}
	got, err = matchPods("53", pods)
	if err != nil || len(got) != 1 || got[0].Session() != "9f0000000000" {
		t.Fatalf("name beats id prefix: %v %v", got, err)
	}
	if _, err := matchPods("dup", pods); err == nil || !strings.Contains(err.Error(), "use an id") {
		t.Fatalf("duplicate name: %v", err)
	}
}

func TestNameDirectoryConflict(t *testing.T) {
	withSessions(t, map[string]string{"53bd3fc7e5ba": "auth"})
	t.Chdir(t.TempDir())
	if err := os.Mkdir("auth", 0o755); err != nil {
		t.Fatal(err)
	}
	pods := []podInfo{{ID: "a", Name: "hsh-53bd3fc7e5ba", Labels: map[string]string{"hsh.workspace": "/elsewhere"}}}
	for _, f := range []func() error{
		func() error { _, err := matchPods("auth", pods); return err },
		func() error { _, err := resolveResumeTarget("auth", t.TempDir(), pods); return err },
	} {
		err := f()
		if err == nil || !strings.Contains(err.Error(), "./auth") || !strings.Contains(err.Error(), "53bd3fc7e5ba") {
			t.Fatalf("conflict error: %v", err)
		}
	}
	if _, err := matchPods("./auth", pods); err != nil {
		t.Fatalf("./auth is the directory: %v", err)
	}
}

func TestResolveResumeTargetByName(t *testing.T) {
	withSessions(t, map[string]string{"53bd3fc7e5ba": "auth", "53cc00000000": ""})
	ws := t.TempDir()
	pods := []podInfo{
		{ID: "a", Name: "hsh-53bd3fc7e5ba", Created: "2024-01-01T00:00:00Z", Labels: map[string]string{"hsh.workspace": ws}},
		{ID: "b", Name: "hsh-53cc00000000", Created: "2024-01-02T00:00:00Z", Labels: map[string]string{"hsh.workspace": ws}},
	}
	if got, err := resolveResumeTarget("auth", t.TempDir(), pods); err != nil || got != "53bd3fc7e5ba" {
		t.Fatalf("by name: %q %v", got, err)
	}
	_, err := resolveResumeTarget("", ws, pods)
	if err == nil {
		t.Fatal("want a multiple sessions error")
	}
	for _, s := range []string{"auth  53bd3fc7e5ba", "-  53cc00000000", "hsh resume <name or session id>"} {
		if !strings.Contains(err.Error(), s) {
			t.Errorf("error %q lacks %q", err, s)
		}
	}
}

func TestSessionLabel(t *testing.T) {
	withSessions(t, map[string]string{"53bd3fc7e5ba": "auth", "53cc00000000": ""})
	if got := sessionLabel("53bd3fc7e5ba"); got != "auth" {
		t.Errorf("named: %q", got)
	}
	if got := sessionLabel("53cc00000000"); got != "53cc00000000" {
		t.Errorf("unnamed: %q", got)
	}
}
