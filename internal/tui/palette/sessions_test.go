package palette

import (
	"os"
	"path/filepath"
	"testing"

	"hsh/internal/state"
)

func TestListSessionsShowsNames(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	for _, id := range []string{"53bd3fc7e5ba", "53cc00000000"} {
		if err := os.MkdirAll(state.SessionDir(id), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(state.SessionDir(id), "config.json"), []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := state.WriteName("53bd3fc7e5ba", "auth"); err != nil {
		t.Fatal(err)
	}
	items := listSessions()
	if len(items) != 2 {
		t.Fatalf("items = %v", items)
	}
	got := map[string]string{}
	for _, it := range items {
		got[it.id] = it.text
	}
	if got["53bd3fc7e5ba"] != "auth  53bd3fc7e5ba" || got["53cc00000000"] != "53cc00000000" {
		t.Fatalf("texts = %v", got)
	}
	if sessionLabel("53bd3fc7e5ba") != "auth" || sessionLabel("53cc00000000") != "53cc00000000" {
		t.Fatal("sessionLabel")
	}
}
