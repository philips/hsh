package cli

import (
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
	return Secret(args)
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
	if err := runSecret(t, "add", "mistral-key", "--db", db, "--value-stdin"); err != nil {
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
	if err := runSecret(t, "add", "mistral-key", "--db", db, "--value-stdin"); !errors.Is(err, secretstore.ErrExists) {
		t.Fatalf("duplicate add error = %v, want ErrExists", err)
	}
	feedStdin(t, "other-secret\n")
	if err := runSecret(t, "add", "mistral-key", "--db", db, "--value-stdin", "--force"); err != nil {
		t.Fatalf("add --force: %v", err)
	}
	v, err = store.Get("mistral-key")
	if err != nil || v != "other-secret" {
		t.Fatalf("Get after --force = (%q, %v), want (other-secret, nil)", v, err)
	}

	if err := runSecret(t, "rm", "nope", "--db", db); !errors.Is(err, secretstore.ErrNotFound) {
		t.Fatalf("rm missing error = %v, want ErrNotFound", err)
	}
	if err := runSecret(t, "rm", "mistral-key", "--db", db); err != nil {
		t.Fatalf("rm: %v", err)
	}
	if err := runSecret(t, "rm", "mistral-key", "--db", db); !errors.Is(err, secretstore.ErrNotFound) {
		t.Fatalf("second rm error = %v, want ErrNotFound", err)
	}
}

func TestSecretAddRejectsEmptyValue(t *testing.T) {
	db := testStorePath(t)
	feedStdin(t, "\n")
	if err := runSecret(t, "add", "empty", "--db", db, "--value-stdin"); err == nil {
		t.Fatal("expected an error for an empty value")
	}
}

func TestSecretAddWritesConfigEntry(t *testing.T) {
	db := testStorePath(t)
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.hsh")
	body := "{\n  \"network\": { \"allow\": [\"api.mistral.ai\"] }\n}\n"
	if err := os.WriteFile(configPath, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	feedStdin(t, "sk-test\n")
	err := runSecret(t, "add", "mistral-key", "--db", db, "--value-stdin",
		"--config", configPath, "--host", "api.mistral.ai",
		"--header", "Authorization", "--prefix", "Bearer ")
	if err != nil {
		t.Fatalf("add with config flags: %v", err)
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	if !strings.Contains(s, "\"from\": \"hsh://mistral-key\"") {
		t.Fatalf("config does not reference hsh://mistral-key:\n%s", s)
	}
	if !strings.Contains(s, "\"Authorization\"") || !strings.Contains(s, "\"Bearer \"") {
		t.Fatalf("config entry is missing header/prefix:\n%s", s)
	}
	if !strings.Contains(s, "\"network\"") {
		t.Fatalf("config lost existing content:\n%s", s)
	}
}

func TestSecretRmWarnsOnDanglingRef(t *testing.T) {
	db := testStorePath(t)
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.hsh")
	body := `{ "secrets": { "API_KEY": { "from": "hsh://mistral-key", "hosts": ["api.mistral.ai"] } } }` + "\n"
	if err := os.WriteFile(configPath, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	feedStdin(t, "sk-test\n")
	if err := runSecret(t, "add", "mistral-key", "--db", db, "--value-stdin"); err != nil {
		t.Fatalf("add: %v", err)
	}
	if err := runSecret(t, "rm", "mistral-key", "--db", db, "--config", configPath); err != nil {
		t.Fatalf("rm: %v", err)
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "hsh://mistral-key") {
		t.Fatal("rm must never edit the config")
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
