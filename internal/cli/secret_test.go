package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"hsh/internal/secretstore"
)

func testStorePath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "secrets.db")
}

func runSecret(t *testing.T, args ...string) error {
	t.Helper()
	root := secretCommand()
	if err := root.Parse(args); err != nil {
		return err
	}
	return root.Run(context.Background())
}

func feedStdin(t *testing.T, value string) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	backup := os.Stdin
	os.Stdin = r
	go func() {
		w.WriteString(value)
		w.Close()
	}()
	t.Cleanup(func() { os.Stdin = backup })
}

func TestSecretAddLsRm(t *testing.T) {
	db := testStorePath(t)
	feedStdin(t, "super-secret\n")
	if err := runSecret(t, "add", "--db", db, "--value-stdin", "mistral-key"); err != nil {
		t.Fatalf("add: %v", err)
	}
	store, _, err := secretstore.Open(db)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	v, err := store.Get("mistral-key")
	if err != nil || v != "super-secret" {
		t.Fatalf("Get = (%q, %v), want (super-secret, nil)", v, err)
	}

	feedStdin(t, "super-secret\n")
	if err := runSecret(t, "add", "--db", db, "--value-stdin", "mistral-key"); !errors.Is(err, secretstore.ErrExists) {
		t.Fatalf("duplicate add error = %v, want ErrExists", err)
	}
	feedStdin(t, "other-secret\n")
	if err := runSecret(t, "add", "--db", db, "--value-stdin", "--force", "mistral-key"); err != nil {
		t.Fatalf("add --force: %v", err)
	}
	v, err = store.Get("mistral-key")
	if err != nil || v != "other-secret" {
		t.Fatalf("Get after --force = (%q, %v), want (other-secret, nil)", v, err)
	}

	if err := runSecret(t, "rm", "--db", db, "nope"); !errors.Is(err, secretstore.ErrNotFound) {
		t.Fatalf("rm missing error = %v, want ErrNotFound", err)
	}
	if err := runSecret(t, "rm", "--db", db, "mistral-key"); err != nil {
		t.Fatalf("rm: %v", err)
	}
	if err := runSecret(t, "rm", "--db", db, "mistral-key"); !errors.Is(err, secretstore.ErrNotFound) {
		t.Fatalf("second rm error = %v, want ErrNotFound", err)
	}
}

func TestSecretAddRejectsEmptyValue(t *testing.T) {
	db := testStorePath(t)
	feedStdin(t, "\n")
	if err := runSecret(t, "add", "--db", db, "--value-stdin", "empty"); err == nil {
		t.Fatal("expected an error for an empty value")
	}
}

func TestSecretLsMissingStore(t *testing.T) {
	db := filepath.Join(t.TempDir(), "secrets.db")
	if err := runSecret(t, "ls", "--db", db); err != nil {
		t.Fatalf("ls on a missing store: %v", err)
	}
	if _, err := os.Stat(db); !os.IsNotExist(err) {
		t.Fatal("ls must not create the store")
	}
}

func TestSecretUnknownSubcommand(t *testing.T) {
	if err := runSecret(t, "show"); err == nil {
		t.Fatal("expected an error for an unknown subcommand")
	}
}

func TestFlagsBeforePositionalsContract(t *testing.T) {
	db := testStorePath(t)
	feedStdin(t, "value\n")
	err := runSecret(t, "add", "some-name", "--db", db, "--value-stdin")
	if err == nil {
		t.Fatal("expected an error: flags must come before the NAME positional")
	}
	if !strings.Contains(err.Error(), "exactly one NAME") {
		t.Fatalf("unexpected error: %v", err)
	}
}
