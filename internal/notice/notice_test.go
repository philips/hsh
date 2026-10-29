package notice

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestEnabled(t *testing.T) {
	for _, tc := range []struct {
		notice bool
		mode   string
		want   bool
	}{
		{true, "https", true},
		{true, "allowlist", true},
		{true, "none", false},
		{false, "https", false},
		{false, "allowlist", false},
	} {
		if got := Enabled(tc.notice, tc.mode); got != tc.want {
			t.Errorf("Enabled(%v, %q) = %v, want %v", tc.notice, tc.mode, got, tc.want)
		}
	}
}

func TestTextDoesNotNameHosts(t *testing.T) {
	if !strings.Contains(Text, "403 Forbidden") || !strings.Contains(Text, "not an outage") {
		t.Fatalf("notice is missing the denial shape: %q", Text)
	}
	if n := len(strings.Fields(Text)); n > 100 {
		t.Fatalf("notice is %d words, want at most 100", n)
	}
}

func TestWrite(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "notice")
	if err := Write(dir, true); err != nil {
		t.Fatal(err)
	}
	if got := Read(filepath.Join(dir, FileName)); got != Text {
		t.Fatalf("notice file = %q", got)
	}
	if err := Write(dir, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, FileName)); !os.IsNotExist(err) {
		t.Fatalf("disabled notice left the file behind: %v", err)
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		t.Fatalf("disabled notice must keep the directory for the mount: %v", err)
	}
	if err := Write(dir, false); err != nil {
		t.Fatalf("removing an absent notice: %v", err)
	}
}

func TestSubstituteArgv(t *testing.T) {
	for _, tc := range []struct {
		name string
		argv []string
		path string
		want []string
	}{
		{"enabled", []string{"pi", "--append-system-prompt", Placeholder}, Path, []string{"pi", "--append-system-prompt", Path}},
		{"disabled drops the flag", []string{"pi", "--append-system-prompt", Placeholder, "-p", "x"}, "", []string{"pi", "-p", "x"}},
		{"disabled positional", []string{"tool", Placeholder}, "", []string{"tool"}},
		{"joined enabled", []string{"pi", "--append-system-prompt=" + Placeholder}, Path, []string{"pi", "--append-system-prompt=" + Path}},
		{"joined disabled", []string{"pi", "--append-system-prompt=" + Placeholder, "x"}, "", []string{"pi", "x"}},
		{"prompt text untouched", []string{"pi", "-p", "Complete " + Placeholder + "\n"}, "", []string{"pi", "-p", "Complete " + Placeholder + "\n"}},
		{"no placeholder", []string{"pi"}, Path, []string{"pi"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := SubstituteArgv(tc.argv, tc.path); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestExpandHome(t *testing.T) {
	if got := ExpandHome("~/.codex/AGENTS.md", "/home/hsh"); got != "/home/hsh/.codex/AGENTS.md" {
		t.Fatal(got)
	}
	if got := ExpandHome("~", "/home/hsh"); got != "/home/hsh" {
		t.Fatal(got)
	}
	if got := ExpandHome("/etc/x", "/home/hsh"); got != "/etc/x" {
		t.Fatal(got)
	}
}

func TestApplyBlock(t *testing.T) {
	block := BlockBegin + "\nnotice\n" + BlockEnd + "\n"
	if got := ApplyBlock("", "notice\n"); got != block {
		t.Fatalf("empty file: %q", got)
	}
	user := "# Mine\nkeep this\n"
	once := ApplyBlock(user, "notice\n")
	if once != user+"\n"+block {
		t.Fatalf("append: %q", once)
	}
	if twice := ApplyBlock(once, "notice\n"); twice != once {
		t.Fatalf("not idempotent: %q", twice)
	}
	modified := strings.Replace(once, "notice", "agent edited this", 1)
	if got := ApplyBlock(modified, "notice\n"); got != once {
		t.Fatalf("modified block not replaced: %q", got)
	}
	middle := "top\n" + BlockBegin + "\nold\n" + BlockEnd + "\nbottom\n"
	if got := ApplyBlock(middle, "notice\n"); got != "top\n"+block+"bottom\n" {
		t.Fatalf("middle block: %q", got)
	}
	if got := ApplyBlock(once, ""); got != user {
		t.Fatalf("removal: %q", got)
	}
	if got := ApplyBlock(middle, ""); got != "top\nbottom\n" {
		t.Fatalf("middle removal: %q", got)
	}
	if got := ApplyBlock(user, ""); got != user {
		t.Fatalf("removal without a block changed the file: %q", got)
	}
	noEnd := "x\n" + BlockBegin + "\nunterminated\n"
	if got := ApplyBlock(noEnd, ""); got != noEnd {
		t.Fatalf("an unterminated marker must be left alone: %q", got)
	}
}

func TestApplyContextFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a", "b", "AGENTS.md")
	if err := ApplyContextFile(path, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("removing from a missing file must not create it")
	}
	if err := ApplyContextFile(path, Text); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), BlockBegin) || !strings.Contains(string(data), "403 Forbidden") {
		t.Fatalf("missing file not created with the block: %q", data)
	}
	if err := os.WriteFile(path, append([]byte("# user rules\n\n"), data...), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := ApplyContextFile(path, Text); err != nil {
			t.Fatal(err)
		}
	}
	data, _ = os.ReadFile(path)
	if strings.Count(string(data), BlockBegin) != 1 || !strings.HasPrefix(string(data), "# user rules\n") {
		t.Fatalf("user text or single block lost: %q", data)
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0o600 {
		t.Fatalf("mode changed to %v", info.Mode().Perm())
	}
	if err := ApplyContextFile(path, ""); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(path)
	if string(data) != "# user rules\n" {
		t.Fatalf("disabled notice left: %q", data)
	}
}
