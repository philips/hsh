package cli

import (
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"
)

func TestAttachFlags(t *testing.T) {
	cases := []struct {
		name        string
		interactive bool
		detachable  bool
		keys        string
		want        []string
	}{
		{"piped", false, false, "ctrl-]", nil},
		{"detachable", true, true, "ctrl-]", []string{"--sig-proxy=false", "--detach-keys=ctrl-]"}},
		{"custom keys", true, true, "ctrl-a,d", []string{"--sig-proxy=false", "--detach-keys=ctrl-a,d"}},
		{"detach disabled by config", true, true, "", []string{"--sig-proxy=false", "--detach-keys="}},
		{"interactive but not detachable", true, false, "ctrl-]", []string{"--detach-keys="}},
	}
	for _, c := range cases {
		if got := attachFlags(c.interactive, c.detachable, c.keys); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
}

func TestWithRunFlagsPutsFlagsBeforeTheImage(t *testing.T) {
	ws := []string{"create", "--pod", "p", "-i", "-t", "image", "--shell", "/bin/sh"}
	got := withRunFlags(ws, []string{"--detach-keys="})
	want := []string{"run", "--detach-keys=", "--pod", "p", "-i", "-t", "image", "--shell", "/bin/sh"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v", got)
	}
	if ws[0] != "create" {
		t.Fatal("withRunFlags modified its input")
	}
}

func TestSessionStatus(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	mk := func(podStatus, wsStatus string) podInfo {
		p := podInfo{Name: "hsh-abc123", Status: podStatus}
		if wsStatus != "" {
			p.Containers = []struct {
				ID     string `json:"Id"`
				Names  string `json:"Names"`
				Status string `json:"Status"`
			}{
				{Names: "hsh-abc123-proxy", Status: "running"},
				{Names: "hsh-abc123-ws", Status: wsStatus},
			}
		}
		return p
	}
	dir := sessionDirFor("hsh-abc123")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	if got := sessionStatus(mk("Running", "")); got != "Running" {
		t.Errorf("no workspace container: %q", got)
	}
	if got := sessionStatus(mk("Running", "running")); got != "Detached" {
		t.Errorf("running without a client: %q", got)
	}
	if err := os.WriteFile(filepath.Join(dir, "client.pid"), []byte(strconv.Itoa(os.Getpid())+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := sessionStatus(mk("Running", "running")); got != "Attached" {
		t.Errorf("running with a live client: %q", got)
	}
	if err := os.WriteFile(filepath.Join(dir, "client.pid"), []byte("999999999\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := sessionStatus(mk("Running", "running")); got != "Detached" {
		t.Errorf("stale client pid: %q", got)
	}
	if got := sessionStatus(mk("Degraded", "exited")); got != "Shell exited" {
		t.Errorf("shell exited under a running proxy: %q", got)
	}
	if got := sessionStatus(mk("Exited", "exited")); got != "Exited" {
		t.Errorf("fully stopped pod: %q", got)
	}
}
