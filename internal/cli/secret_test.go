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
	if err := runSecret(t, "add", "--db", db, "--value-stdin", "mistral-key"); err == nil || !strings.Contains(err.Error(), "needs --host") {
		t.Fatalf("add without --host: %v", err)
	}
	feedStdin(t, "super-secret\n")
	if err := runSecret(t, "add", "--db", db, "--value-stdin", "--host", "api.mistral.ai,https://Other.example/", "--host", "*.mistral.ai", "mistral-key"); err != nil {
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
	if hosts, _ := store.SecretHosts("mistral-key"); strings.Join(hosts, ",") != "*.mistral.ai,api.mistral.ai,other.example" {
		t.Fatalf("hosts = %v", hosts)
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
	if hosts, _ := store.SecretHosts("mistral-key"); len(hosts) != 3 {
		t.Fatalf("add --force without --host dropped the hosts: %v", hosts)
	}
	if err := runSecret(t, "disallow", "--db", db, "mistral-key", "*.mistral.ai", "other.example"); err != nil {
		t.Fatal(err)
	}
	if err := runSecret(t, "allow", "--db", db, "mistral-key", "eu.mistral.ai"); err != nil {
		t.Fatal(err)
	}
	if hosts, _ := store.SecretHosts("mistral-key"); strings.Join(hosts, ",") != "api.mistral.ai,eu.mistral.ai" {
		t.Fatalf("hosts after allow/disallow = %v", hosts)
	}
	if err := runSecret(t, "allow", "--db", db, "mistral-key"); err == nil {
		t.Fatal("allow without a host succeeded")
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
	if err := runSecret(t, "add", "--db", db, "--value-stdin", "--host", "a.example", "empty"); err == nil {
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

func TestSecretEditRewritesTheHostList(t *testing.T) {
	db := testStorePath(t)
	feedStdin(t, "v\n")
	if err := runSecret(t, "add", "--db", db, "--value-stdin", "--host", "keep.example", "--host", "drop.example", "k"); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(t.TempDir(), "editor.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\ngrep -q '^# Hosts hsh may send the secret k to' \"$1\" || exit 3\ngrep -q '^drop.example$' \"$1\" || exit 4\nprintf '# comment\\nkeep.example\\n\\nNEW.example  *.more.example\\n' > \"$1\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("VISUAL", script)
	if err := runSecret(t, "edit", "--db", db, "k"); err != nil {
		t.Fatalf("edit: %v", err)
	}
	store, _, err := secretstore.Open(db)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if hosts, _ := store.SecretHosts("k"); strings.Join(hosts, ",") != "*.more.example,keep.example,new.example" {
		t.Fatalf("hosts after edit = %v", hosts)
	}
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho 'not a host!' > \"$1\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	feedStdin(t, "")
	if err := runSecret(t, "edit", "--db", db, "k"); err == nil {
		t.Fatal("an invalid host list was saved")
	}
	if hosts, _ := store.SecretHosts("k"); len(hosts) != 3 {
		t.Fatalf("a failed edit changed the hosts: %v", hosts)
	}
}
