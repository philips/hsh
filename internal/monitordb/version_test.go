package monitordb

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
)

func rawVersion(t *testing.T, path string) int {
	t.Helper()
	raw, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	var v int
	if err := raw.QueryRow("PRAGMA user_version").Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

func setRawVersion(t *testing.T, path string, v int) {
	t.Helper()
	raw, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if _, err := raw.Exec(fmt.Sprintf("PRAGMA user_version = %d", v)); err != nil {
		t.Fatal(err)
	}
}

func TestNewDatabaseHasTheSchemaVersionAndTable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "monitor.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if got := rawVersion(t, path); got != SchemaVersion {
		t.Fatalf("user_version = %d, want %d", got, SchemaVersion)
	}
	if err := Record(db, "/ws", "s1", "a.example", "a.example:443"); err != nil {
		t.Fatalf("the schema was not created: %v", err)
	}
}

func TestOpenRefusesANewerSchemaVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "monitor.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()

	setRawVersion(t, path, SchemaVersion+1)
	_, err = Open(path)
	want := fmt.Sprintf("monitor database: monitor.db has schema version %d, newer than this hsh reads (version %d); update hsh", SchemaVersion+1, SchemaVersion)
	if err == nil || err.Error() != want {
		t.Fatalf("newer: %v", err)
	}

}

func TestConcurrentFirstOpensShareOneSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "monitor.db")
	var wg sync.WaitGroup
	errs := make(chan error, 10)
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			db, err := Open(path)
			if err != nil {
				errs <- err
				return
			}
			defer db.Close()
			errs <- Record(db, "/ws", fmt.Sprintf("s%d", i), "a.example", "a.example:443")
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := All(db, Filter{})
	if err != nil || len(rows) != 10 {
		t.Fatalf("%d rows, %v", len(rows), err)
	}
}
