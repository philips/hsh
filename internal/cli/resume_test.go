package cli

import (
	"strings"
	"testing"
)

func resumePods(ws1, ws2 string) []podInfo {
	return []podInfo{
		{ID: "aaa111", Name: "hsh-53bd3fc7e5ba", Created: "2024-01-01T00:00:00Z", Labels: map[string]string{"hsh.workspace": ws1}},
		{ID: "bbb222", Name: "hsh-53cc00000000", Created: "2024-01-02T00:00:00Z", Labels: map[string]string{"hsh.workspace": ws1}},
		{ID: "ccc333", Name: "hsh-9f0000000000", Created: "2024-01-03T00:00:00Z", Labels: map[string]string{"hsh.workspace": ws2}},
	}
}

func TestResolveResumeTarget(t *testing.T) {
	ws1 := t.TempDir()
	ws2 := t.TempDir()
	empty := t.TempDir()
	pods := resumePods(ws1, ws2)

	for _, c := range []struct {
		name, arg, cwd string
		want           string
		errHas         []string
	}{
		{name: "cwd with one session", cwd: ws2, want: "9f0000000000"},
		{name: "directory argument", arg: ws2, cwd: empty, want: "9f0000000000"},
		{name: "cwd with no session", cwd: empty, errHas: []string{"no session for workspace " + empty, "hsh run IMAGE"}},
		{name: "directory with no session", arg: empty, cwd: ws1, errHas: []string{"no session for workspace " + empty}},
		{name: "cwd with several sessions", cwd: ws1, errHas: []string{"multiple sessions for workspace " + ws1, "53bd3fc7e5ba", "53cc00000000", "hsh resume <session id>"}},
		{name: "exact id", arg: "9f0000000000", cwd: empty, want: "9f0000000000"},
		{name: "pod name", arg: "hsh-9f0000000000", cwd: empty, want: "9f0000000000"},
		{name: "unique prefix", arg: "53bd", cwd: empty, want: "53bd3fc7e5ba"},
		{name: "pod name prefix", arg: "hsh-53cc", cwd: empty, want: "53cc00000000"},
		{name: "ambiguous prefix", arg: "53", cwd: empty, errHas: []string{"ambiguous"}},
		{name: "unknown id", arg: "zzzz", cwd: empty, errHas: []string{`no session matching "zzzz"`, "hsh ls"}},
	} {
		got, err := resolveResumeTarget(c.arg, c.cwd, pods)
		if len(c.errHas) > 0 {
			if err == nil {
				t.Errorf("%s: got %q, want an error", c.name, got)
				continue
			}
			for _, s := range c.errHas {
				if !strings.Contains(err.Error(), s) {
					t.Errorf("%s: error %q lacks %q", c.name, err, s)
				}
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("%s: got %q, %v; want %q", c.name, got, err, c.want)
		}
	}
}

func TestResolveResumeTargetDirectoryWinsOverID(t *testing.T) {
	ws := t.TempDir()
	pods := []podInfo{{ID: "a", Name: "hsh-" + ws, Labels: map[string]string{"hsh.workspace": "/elsewhere"}}}
	if _, err := resolveResumeTarget(ws, t.TempDir(), pods); err == nil || !strings.Contains(err.Error(), "no session for workspace") {
		t.Fatalf("a directory argument must be treated as a workspace, got %v", err)
	}
}

func TestRunRejectsRemovedFlags(t *testing.T) {
	for _, args := range [][]string{
		{"run", "--resume"},
		{"run", "--fresh"},
		{"run", "--take-over"},
		{"run", "--workspace", "."},
		{"resume", "--fresh"},
		{"resume", "--runtime", "gvisor"},
	} {
		if err := Root().Parse(args); err == nil {
			t.Errorf("%v: parsed without error", args)
		}
	}
}
