package denieddb

import (
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func openTemp(t *testing.T) *sql.DB {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "denied.db"))
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func TestOpenMigratesIgnoredColumn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "denied.db")
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		t.Fatal(err)
	}
	oldSchema := `CREATE TABLE denied (
		host TEXT PRIMARY KEY,
		target TEXT NOT NULL,
		count INTEGER NOT NULL,
		first_seen TEXT NOT NULL,
		last_seen TEXT NOT NULL,
		allowed INTEGER NOT NULL DEFAULT 0,
		allowed_at TEXT
	)`
	if _, err := db.Exec(oldSchema); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO denied (host, target, count, first_seen, last_seen)
		VALUES ('old.example.com', 'old.example.com:443', 1, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	db.Close()

	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	rows, err := db.Query("PRAGMA table_info(denied)")
	if err != nil {
		t.Fatal(err)
	}
	var columns []string
	for rows.Next() {
		var cid, notNull, pk int
		var name, ctype string
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &ctype, &notNull, &dflt, &pk); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		columns = append(columns, name)
	}
	rows.Close()
	hasIgnored := false
	for _, c := range columns {
		if c == "ignored" {
			hasIgnored = true
		}
	}
	if !hasIgnored {
		t.Fatalf("ignored column missing after migration: %v", columns)
	}

	all, err := All(db, Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 || all[0].Host != "old.example.com" {
		t.Fatalf("existing rows lost during migration: %+v", all)
	}
	if all[0].Ignored {
		t.Error("existing row should not be marked ignored")
	}
}

func TestMarkIgnored(t *testing.T) {
	db := openTemp(t)
	defer db.Close()
	if err := Record(db, "", "", "a.example.com", "a.example.com:443"); err != nil {
		t.Fatal(err)
	}
	if err := MarkIgnored(db, "", []string{"a.example.com"}); err != nil {
		t.Fatal(err)
	}
	pending, err := Pending(db, Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 0 {
		t.Errorf("ignored host still pending: %+v", pending)
	}
	ignored, err := Ignored(db, Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(ignored) != 1 {
		t.Fatalf("expected 1 ignored host, got %d", len(ignored))
	}
	if ignored[0].Host != "a.example.com" || !ignored[0].Ignored || !ignored[0].Allowed {
		t.Errorf("unexpected ignored row: %+v", ignored[0])
	}

	if err := Record(db, "", "", "a.example.com", "a.example.com:443"); err != nil {
		t.Fatal(err)
	}
	pending, err = Pending(db, Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 0 {
		t.Errorf("a later denial brought an ignored host back to the review list: %+v", pending)
	}
}

func TestMarkAllowedClearsIgnored(t *testing.T) {
	db := openTemp(t)
	defer db.Close()
	if err := Record(db, "", "", "a.example.com", "a.example.com:443"); err != nil {
		t.Fatal(err)
	}
	if err := MarkIgnored(db, "", []string{"a.example.com"}); err != nil {
		t.Fatal(err)
	}
	if err := MarkAllowed(db, "", []string{"a.example.com"}); err != nil {
		t.Fatal(err)
	}
	ignored, err := Ignored(db, Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(ignored) != 0 {
		t.Errorf("approved host still listed as ignored: %+v", ignored)
	}
	all, err := All(db, Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 || !all[0].Allowed || all[0].Ignored {
		t.Errorf("unexpected row after approve: %+v", all)
	}
}
