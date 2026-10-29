package cli

import (
	"io"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"

	"hsh/internal/denieddb"
)

func captureStdout(t *testing.T, f func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	defer func() { os.Stdout = old }()
	f()
	w.Close()
	b, _ := io.ReadAll(r)
	return string(b)
}

func TestHelpEverywhere(t *testing.T) {
	cases := [][]string{
		{"run", "--help"},
		{"run", "-h"},
		{"secret", "--help"},
		{"secret"},
		{"proxy", "--help"},
		{"proxy"},
		{"oauth", "--help"},
		{"oauth"},
		{"ssh", "--help"},
		{"ssh"},
		{"ssh", "proxy", "--help"},
		{"ssh", "config", "-h"},
		{"proxy", "allow", "--help"},
		{"secret", "add", "-h"},
		{"ls", "--help"},
		{"update", "--help"},
	}
	for _, args := range cases {
		var err error
		out := captureStdout(t, func() { err = Run(args) })
		if err != nil {
			t.Errorf("%v: unexpected error: %v", args, err)
		}
		if !strings.Contains(out, "USAGE") {
			t.Errorf("%v: no usage printed: %q", args, out)
		}
	}
}

func TestGroupUnknownSubcommand(t *testing.T) {
	var err error
	captureStdout(t, func() { err = Run([]string{"proxy", "bogus"}) })
	if err == nil || !strings.Contains(err.Error(), "unknown subcommand") {
		t.Fatalf("got %v", err)
	}
}

func TestProxyDeniedWatchStopsOnInterrupt(t *testing.T) {
	db := t.TempDir() + "/denied.db"
	h, err := denieddb.Open(db)
	if err != nil {
		t.Fatal(err)
	}
	h.Close()
	done := make(chan error, 1)
	go func() {
		done <- Run([]string{"proxy", "denied", "--db", db, "--watch"})
	}()
	time.Sleep(500 * time.Millisecond)
	syscall.Kill(os.Getpid(), syscall.SIGINT)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("watch returned %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("watch did not stop on SIGINT")
	}
}
