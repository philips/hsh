package cli

import (
	"io"
	"os"
	"strings"
	"testing"
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
		{"proxy", "allow", "--help"},
		{"secret", "add", "-h"},
		{"ls", "--help"},
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
