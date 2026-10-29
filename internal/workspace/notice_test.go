package workspace

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"hsh/internal/notice"
)

func TestApplyNoticeEnabled(t *testing.T) {
	dir := t.TempDir()
	home := filepath.Join(dir, "home")
	noticeDir := filepath.Join(dir, "notice")
	if err := notice.Write(noticeDir, true); err != nil {
		t.Fatal(err)
	}
	noticePath := filepath.Join(noticeDir, notice.FileName)
	ctx := filepath.Join(home, ".codex", "AGENTS.md")
	if err := os.MkdirAll(filepath.Dir(ctx), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ctx, []byte("# mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	opts := InnerOptions{
		Home:         home,
		Launch:       []string{"pi", "--append-system-prompt", notice.Placeholder},
		Exec:         []string{"pi", "--append-system-prompt", notice.Placeholder, "-p", "task"},
		ContextFiles: []string{"~/.codex/AGENTS.md", "~/.claude/CLAUDE.md"},
	}
	for range 2 {
		o := opts
		o.Launch = append([]string(nil), opts.Launch...)
		o.Exec = append([]string(nil), opts.Exec...)
		applyNotice(&o, noticePath)
		if want := []string{"pi", "--append-system-prompt", noticePath}; !reflect.DeepEqual(o.Launch, want) {
			t.Fatalf("launch = %q", o.Launch)
		}
		if want := []string{"pi", "--append-system-prompt", noticePath, "-p", "task"}; !reflect.DeepEqual(o.Exec, want) {
			t.Fatalf("exec = %q", o.Exec)
		}
	}
	data, _ := os.ReadFile(ctx)
	if !strings.HasPrefix(string(data), "# mine\n") || strings.Count(string(data), notice.BlockBegin) != 1 {
		t.Fatalf("existing instruction file after two starts: %q", data)
	}
	created, err := os.ReadFile(filepath.Join(home, ".claude", "CLAUDE.md"))
	if err != nil || !strings.Contains(string(created), "403 Forbidden") {
		t.Fatalf("missing instruction file not created: %v %q", err, created)
	}
}

func TestApplyNoticeDisabled(t *testing.T) {
	dir := t.TempDir()
	home := filepath.Join(dir, "home")
	ctx := filepath.Join(home, "AGENTS.md")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ctx, []byte(notice.ApplyBlock("# mine\n", notice.Text)), 0o644); err != nil {
		t.Fatal(err)
	}
	opts := InnerOptions{
		Home:         home,
		Launch:       []string{"pi", "--append-system-prompt", notice.Placeholder},
		ContextFiles: []string{"~/AGENTS.md", "~/absent.md"},
	}
	applyNotice(&opts, filepath.Join(dir, "notice", notice.FileName))
	if !reflect.DeepEqual(opts.Launch, []string{"pi"}) {
		t.Fatalf("launch = %q", opts.Launch)
	}
	if data, _ := os.ReadFile(ctx); string(data) != "# mine\n" {
		t.Fatalf("leftover block not removed: %q", data)
	}
	if _, err := os.Stat(filepath.Join(home, "absent.md")); !os.IsNotExist(err) {
		t.Fatal("a disabled notice created an instruction file")
	}
}
