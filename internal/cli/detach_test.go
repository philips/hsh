package cli

import (
	"os"
	"strconv"
	"testing"
	"time"

	"hsh/internal/denieddb"
)

func TestDetachClientIgnoresMissingAndForeignClients(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	dir := denieddb.SessionDir("abc123")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if ok, err := detachClient(dir, time.Second); ok || err != nil {
		t.Fatalf("no client file: %v %v", ok, err)
	}
	if err := os.WriteFile(clientPIDPath(dir), []byte("999999999\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if ok, err := detachClient(dir, time.Second); ok || err != nil {
		t.Fatalf("dead pid: %v %v", ok, err)
	}
	if err := os.WriteFile(clientPIDPath(dir), []byte(strconv.Itoa(os.Getpid())+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if ok, err := detachClient(dir, time.Second); ok || err != nil {
		t.Fatalf("a live process that is not hsh run must not be signalled: %v %v", ok, err)
	}
	if fileExists(clientPIDPath(dir)) {
		t.Fatal("the stale client file was left behind")
	}
}
