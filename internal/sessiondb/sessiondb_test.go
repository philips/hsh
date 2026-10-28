package sessiondb

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func rows(t *testing.T, path string) []struct {
	time, kind, version string
} {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	r, err := db.Query("SELECT time, kind, version FROM events ORDER BY id")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	var out []struct {
		time, kind, version string
	}
	for r.Next() {
		var row struct {
			time, kind, version string
		}
		if err := r.Scan(&row.time, &row.kind, &row.version); err != nil {
			t.Fatal(err)
		}
		out = append(out, row)
	}
	if err := r.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestAppend(t *testing.T) {
	dir := t.TempDir()
	if err := Append(dir, "created"); err != nil {
		t.Fatal(err)
	}
	if err := Append(dir, "resumed"); err != nil {
		t.Fatal(err)
	}
	got := rows(t, filepath.Join(dir, "session.db"))
	if len(got) != 2 {
		t.Fatalf("got %d rows, want 2", len(got))
	}
	if got[0].kind != "created" || got[1].kind != "resumed" {
		t.Errorf("kinds = %q, %q, want created, resumed", got[0].kind, got[1].kind)
	}
	if got[0].version == "" || got[0].version == got[1].version && got[0].version == "" {
		t.Errorf("version not recorded: %q", got[0].version)
	}
	for i, row := range got {
		if _, err := time.Parse(time.RFC3339, row.time); err != nil {
			t.Errorf("row %d: time %q is not RFC3339: %v", i, row.time, err)
		}
	}
}

func TestAppendUnwritableDir(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Append(filepath.Join(blocker, "sess"), "created"); err == nil {
		t.Fatal("appending under a regular file should fail")
	}
}

func TestAppendCorruptDB(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.db")
	if err := os.WriteFile(path, []byte("not a sqlite database at all"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Append(dir, "created"); err == nil {
		t.Fatal("appending to a corrupt database should fail")
	}
}
