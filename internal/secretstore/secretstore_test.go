package secretstore

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func openTest(t *testing.T) (*DB, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "hsh", "secrets.db")
	db, created, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if !created {
		t.Fatal("first open should report the store as newly created")
	}
	t.Cleanup(func() { db.Close() })
	return db, path
}

func TestSetGetDelete(t *testing.T) {
	db, _ := openTest(t)
	if err := db.Set("foo", "bar", false); err != nil {
		t.Fatalf("set: %v", err)
	}
	v, err := db.Get("foo")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if v != "bar" {
		t.Fatalf("got %q, want %q", v, "bar")
	}
	if err := db.Delete("foo"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := db.Get("foo"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound after delete, got %v", err)
	}
	if err := db.Delete("foo"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound deleting a missing entry, got %v", err)
	}
}

func TestOverwriteSemantics(t *testing.T) {
	db, _ := openTest(t)
	if err := db.Set("k", "one", false); err != nil {
		t.Fatalf("set: %v", err)
	}
	if err := db.Set("k", "two", false); !errors.Is(err, ErrExists) {
		t.Fatalf("expected ErrExists without overwrite, got %v", err)
	}
	v, _ := db.Get("k")
	if v != "one" {
		t.Fatalf("value changed without overwrite: %q", v)
	}
	if err := db.Set("k", "two", true); err != nil {
		t.Fatalf("set with overwrite: %v", err)
	}
	v, err := db.Get("k")
	if err != nil || v != "two" {
		t.Fatalf("got (%q, %v), want (two, nil)", v, err)
	}
}

func TestListNeverReturnsValues(t *testing.T) {
	db, _ := openTest(t)
	if err := db.Set("alpha", "secret-alpha", false); err != nil {
		t.Fatalf("set: %v", err)
	}
	if err := db.Set("beta", "secret-beta", false); err != nil {
		t.Fatalf("set: %v", err)
	}
	entries, err := db.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("got %d entries, want 2", len(entries))
	}
	if entries[0].Name != "alpha" || entries[1].Name != "beta" {
		t.Fatalf("entries not sorted by name: %+v", entries)
	}
	for _, e := range entries {
		if e.CreatedAt == "" || e.UpdatedAt == "" {
			t.Fatalf("entry %q missing timestamps", e.Name)
		}
	}
}

func TestPermissions(t *testing.T) {
	_, path := openTest(t)
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("database mode is %v, want 0600", fi.Mode().Perm())
	}
	dir := filepath.Dir(path)
	di, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat dir: %v", err)
	}
	if di.Mode().Perm() != 0o700 {
		t.Fatalf("directory mode is %v, want 0700", di.Mode().Perm())
	}
}

func TestReopenNotCreated(t *testing.T) {
	_, path := openTest(t)
	db, created, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer db.Close()
	if created {
		t.Fatal("second open should not report the store as newly created")
	}
}
