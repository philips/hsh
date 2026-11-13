package cli

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func mkPod(wsStatus, podStatus string) podInfo {
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

func TestSessionStateFromPod(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	dir := sessionDirFor("hsh-abc123")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name            string
		ws, pod         string
		attached        bool
		want            SessionState
		running, reach  bool
	}{
		{"no ws container", "", "Running", false, SessionStarting, false, false},
		{"detached", "running", "Running", false, SessionDetached, true, true},
		{"attached", "running", "Running", true, SessionAttached, true, true},
		{"suspended", "paused", "Paused", false, SessionSuspended, false, false},
		{"shell exited", "exited", "Degraded", false, SessionShellExited, false, false},
		{"stopped", "exited", "Exited", false, SessionStopped, false, false},
		{"starting", "created", "Running", false, SessionStarting, false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			pidPath := filepath.Join(dir, "client.pid")
			if c.attached {
				if err := os.WriteFile(pidPath, []byte(strconv.Itoa(os.Getpid())+"\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			} else {
				os.Remove(pidPath)
			}
			got := sessionStateFromPod(mkPod(c.ws, c.pod))
			if got != c.want {
				t.Fatalf("state = %v, want %v", got, c.want)
			}
			if got.Running() != c.running {
				t.Errorf("Running() = %v, want %v", got.Running(), c.running)
			}
			if got.Reachable() != c.reach {
				t.Errorf("Reachable() = %v, want %v", got.Reachable(), c.reach)
			}
		})
	}
}

func TestWatchSessionReachableStopsWhenNotReachable(t *testing.T) {
	old := sessionStateResolve
	t.Cleanup(func() { sessionStateResolve = old })

	states := make(chan SessionState, 8)
	states <- SessionDetached
	states <- SessionSuspended
	sessionStateResolve = func(string) SessionState {
		select {
		case s := <-states:
			return s
		default:
			return SessionSuspended
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var leftWith SessionState
	var calls int
	done := make(chan struct{})
	go func() {
		watchSessionReachable(ctx, cancel, "abc123", func(s SessionState) {
			leftWith = s
			calls++
		})
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("watcher did not return")
	}
	if ctx.Err() == nil {
		t.Fatal("context was not cancelled")
	}
	if leftWith != SessionSuspended {
		t.Fatalf("onLeave state = %v, want Suspended", leftWith)
	}
	if calls != 1 {
		t.Fatalf("onLeave called %d times, want 1", calls)
	}
}
