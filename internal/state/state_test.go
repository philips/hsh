package state

import (
	"os"
	"testing"
)

func TestNameRoundTrip(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	if err := os.MkdirAll(SessionDir("aaa"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := ReadName("aaa"); got != "" {
		t.Fatalf("missing name = %q", got)
	}
	if err := WriteName("aaa", "first"); err != nil {
		t.Fatal(err)
	}
	if err := WriteName("aaa", "second"); err != nil {
		t.Fatal(err)
	}
	if got := ReadName("aaa"); got != "second" {
		t.Fatalf("name = %q", got)
	}
	entries, _ := os.ReadDir(SessionDir("aaa"))
	if len(entries) != 1 {
		t.Fatalf("leftover files: %v", entries)
	}
	if err := os.MkdirAll(SessionDir("bbb"), 0o755); err != nil {
		t.Fatal(err)
	}
	names := Names()
	if len(names) != 1 || names["aaa"] != "second" {
		t.Fatalf("Names = %v", names)
	}
	if err := WriteName("aaa", ""); err != nil {
		t.Fatal(err)
	}
	if got := ReadName("aaa"); got != "" {
		t.Fatalf("cleared name = %q", got)
	}
	if err := WriteName("aaa", ""); err != nil {
		t.Fatalf("clearing twice: %v", err)
	}
}
