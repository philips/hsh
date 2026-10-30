package cli

import (
	"os"
	"strconv"
	"testing"
	"time"

	"hsh/internal/state"
)

func TestDetachClientIgnoresMissingAndForeignClients(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	dir := state.SessionDir("abc123")
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

func TestIsHshClientArgs(t *testing.T) {
	for _, c := range []struct {
		args []string
		want bool
	}{
		{[]string{"/usr/bin/hsh", "run", "pi", "."}, true},
		{[]string{"hsh", "resume", "abc"}, true},
		{[]string{"hsh", "resume", "--take-over", "abc"}, true},
		{[]string{"hsh", "ls"}, false},
		{[]string{"sleep", "run"}, false},
		{nil, false},
	} {
		if got := isHshClientArgs(c.args); got != c.want {
			t.Errorf("isHshClientArgs(%v) = %v, want %v", c.args, got, c.want)
		}
	}
}
