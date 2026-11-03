package dbschema

import (
	"database/sql"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	_ "modernc.org/sqlite"
)

const testSchema = `CREATE TABLE IF NOT EXISTS t (id INTEGER PRIMARY KEY, v TEXT);`

func open(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func userVersion(t *testing.T, db *sql.DB) int {
	t.Helper()
	var v int
	if err := db.QueryRow("PRAGMA user_version").Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestNewFileGetsSchemaAndVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.db")
	db := open(t, path)
	if err := Apply(db, "x.db", path, 3, testSchema); err != nil {
		t.Fatal(err)
	}
	if v := userVersion(t, db); v != 3 {
		t.Fatalf("user_version = %d", v)
	}
	if _, err := db.Exec("INSERT INTO t (v) VALUES ('a')"); err != nil {
		t.Fatalf("schema not created: %v", err)
	}
	if err := Apply(db, "x.db", path, 3, testSchema); err != nil {
		t.Fatalf("reopening at the same version: %v", err)
	}
}

func TestExistingUnversionedFileIsAdopted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.db")
	db := open(t, path)
	if _, err := db.Exec(testSchema); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO t (v) VALUES ('keep')"); err != nil {
		t.Fatal(err)
	}
	if err := Apply(db, "x.db", path, 1, testSchema); err != nil {
		t.Fatal(err)
	}
	var v string
	if err := db.QueryRow("SELECT v FROM t").Scan(&v); err != nil || v != "keep" {
		t.Fatalf("existing rows lost: %v %q", err, v)
	}
	if got := userVersion(t, db); got != 1 {
		t.Fatalf("user_version = %d", got)
	}
}

func TestNewerAndOlderAreRefused(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.db")
	db := open(t, path)
	if err := Apply(db, "x.db", path, 2, testSchema); err != nil {
		t.Fatal(err)
	}
	err := Apply(db, "x.db", path, 1, testSchema)
	if err == nil || !strings.Contains(err.Error(), "x.db has schema version 2, newer than this hsh reads (version 1); update hsh") {
		t.Fatalf("newer: %v", err)
	}
	err = Apply(db, "x.db", path, 3, testSchema)
	want := "x.db has schema version 2, older than this hsh reads (version 3); remove " + path
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("older: %v", err)
	}
	if got := userVersion(t, db); got != 2 {
		t.Fatalf("a refused open changed user_version to %d", got)
	}
}

func TestConcurrentFirstOpens(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.db")
	var wg sync.WaitGroup
	errs := make(chan error, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
			if err != nil {
				errs <- err
				return
			}
			defer db.Close()
			errs <- Apply(db, "x.db", path, 1, testSchema)
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent first open: %v", err)
		}
	}
	db := open(t, path)
	if v := userVersion(t, db); v != 1 {
		t.Fatalf("user_version = %d", v)
	}
}

func TestMigrationsUpgradeOlderFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.db")
	db := open(t, path)
	if err := Apply(db, "x.db", path, 1, testSchema); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO t (v) VALUES ('kept')`); err != nil {
		t.Fatal(err)
	}
	v2 := testSchema + `CREATE TABLE IF NOT EXISTS u (id INTEGER PRIMARY KEY);`
	migrations := map[int]string{1: `CREATE TABLE IF NOT EXISTS u (id INTEGER PRIMARY KEY);`}
	if err := ApplyWithMigrations(db, "x.db", path, 2, v2, migrations); err != nil {
		t.Fatal(err)
	}
	if v := userVersion(t, db); v != 2 {
		t.Fatalf("user_version = %d", v)
	}
	var got string
	if err := db.QueryRow(`SELECT v FROM t`).Scan(&got); err != nil || got != "kept" {
		t.Fatalf("existing rows must survive a migration: %q %v", got, err)
	}
	if _, err := db.Exec(`INSERT INTO u (id) VALUES (1)`); err != nil {
		t.Fatalf("the migration did not create the new table: %v", err)
	}
	if err := ApplyWithMigrations(db, "x.db", path, 3, v2, migrations); err == nil || !strings.Contains(err.Error(), "older than this hsh reads") {
		t.Fatalf("a missing migration step must fail as before, got %v", err)
	}
}
