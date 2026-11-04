package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func testRegistry(t *testing.T) *forwardRegistry {
	t.Helper()
	r := &forwardRegistry{dir: filepath.Join(t.TempDir(), "forwards")}
	oldSpawn, oldRunning := forwardSpawn, forwardSessionRunning
	forwardSessionRunning = func(string) bool { return true }
	t.Cleanup(func() { forwardSpawn, forwardSessionRunning = oldSpawn, oldRunning })
	return r
}

func sleepChild(t *testing.T) (pid int, kill func()) {
	t.Helper()
	cmd := exec.Command("sleep", "60")
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot start a child process: %v", err)
	}
	go func() { _ = cmd.Wait() }()
	return cmd.Process.Pid, func() { _ = cmd.Process.Kill() }
}

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

func writeMeta(t *testing.T, dir string, meta forwardMeta) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(meta)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "meta.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestForwardStartRecordsMeta(t *testing.T) {
	r := testRegistry(t)
	pid, kill := sleepChild(t)
	defer kill()
	var gotArgs []string
	var gotLog string
	forwardSpawn = func(exe string, args []string, log string) (int, error) {
		if exe == "" {
			t.Error("no executable passed to the child")
		}
		gotArgs, gotLog = args, log
		return pid, nil
	}
	port := freePort(t)
	m, err := r.start("s1", port)
	if err != nil {
		t.Fatal(err)
	}
	id := "s1-" + strconv.Itoa(port)
	if m.ID != id || m.Session != "s1" || m.Bind != "127.0.0.1" || m.Local != port || m.Remote != port || m.PID != pid {
		t.Fatalf("managed = %+v", m)
	}
	if m.Started.IsZero() {
		t.Fatal("started is zero")
	}
	want := strings.Join([]string{"forward", "--managed", "--session", "s1", "--bind", "127.0.0.1", fmt.Sprintf("%d:%d", port, port)}, " ")
	if strings.Join(gotArgs, " ") != want {
		t.Fatalf("child args = %v, want %s", gotArgs, want)
	}
	if gotLog != filepath.Join(r.dir, id, "log") {
		t.Fatalf("child log = %s", gotLog)
	}
	data, err := os.ReadFile(filepath.Join(r.dir, id, "meta.json"))
	if err != nil {
		t.Fatal(err)
	}
	var meta forwardMeta
	if err := json.Unmarshal(data, &meta); err != nil {
		t.Fatal(err)
	}
	if meta.ID != id || meta.Session != "s1" || meta.Bind != "127.0.0.1" || meta.Local != port || meta.Remote != port || meta.PID != pid || !meta.Started.Equal(m.Started) {
		t.Fatalf("meta = %+v", meta)
	}
}

func TestForwardStartTwiceIsAlreadyForwarding(t *testing.T) {
	r := testRegistry(t)
	pid, kill := sleepChild(t)
	defer kill()
	forwardSpawn = func(string, []string, string) (int, error) { return pid, nil }
	port := freePort(t)
	if _, err := r.start("s1", port); err != nil {
		t.Fatal(err)
	}
	_, err := r.start("s1", port)
	if err == nil || !strings.Contains(err.Error(), "already forwarding session s1's port") {
		t.Fatalf("err = %v, want already forwarding", err)
	}
}

func TestForwardStartOnABusyLocalPort(t *testing.T) {
	r := testRegistry(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port
	forwardSpawn = func(string, []string, string) (int, error) {
		t.Error("spawned a child despite the busy local port")
		return 0, nil
	}
	_, err = r.start("s1", port)
	if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("local port %d is already in use", port)) || !strings.Contains(err.Error(), "hsh forward <other>:") {
		t.Fatalf("err = %v", err)
	}
}

func TestForwardStartWithTheSessionDown(t *testing.T) {
	r := testRegistry(t)
	forwardSessionRunning = func(string) bool { return false }
	_, err := r.start("s1", freePort(t))
	if err == nil || !strings.Contains(err.Error(), "session s1 has no running workspace container") {
		t.Fatalf("err = %v", err)
	}
}

func TestForwardListPrunesDeadPids(t *testing.T) {
	r := testRegistry(t)
	dead := exec.Command("sleep", "60")
	if err := dead.Start(); err != nil {
		t.Skipf("cannot start a child process: %v", err)
	}
	deadPid := dead.Process.Pid
	_ = dead.Process.Kill()
	_ = dead.Wait()
	writeMeta(t, filepath.Join(r.dir, "s1-8000"), forwardMeta{ID: "s1-8000", Session: "s1", Bind: "127.0.0.1", Local: 8000, Remote: 8000, PID: deadPid, Started: time.Now()})

	pid, kill := sleepChild(t)
	defer kill()
	writeMeta(t, filepath.Join(r.dir, "s2-9000"), forwardMeta{ID: "s2-9000", Session: "s2", Bind: "127.0.0.1", Local: 9000, Remote: 9000, PID: pid, Started: time.Now().Add(-time.Minute)})

	active, err := r.list()
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 1 || active[0].ID != "s2-9000" {
		t.Fatalf("active = %+v", active)
	}
	if _, err := os.Stat(filepath.Join(r.dir, "s1-8000")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the dead entry was not pruned")
	}
}

func TestForwardListWithoutARegistry(t *testing.T) {
	r := testRegistry(t)
	active, err := r.list()
	if err != nil || active != nil {
		t.Fatalf("active = %v, err = %v", active, err)
	}
}

func TestForwardStopKillsTheChildAndRemovesTheEntry(t *testing.T) {
	r := testRegistry(t)
	pid, kill := sleepChild(t)
	defer kill()
	forwardSpawn = func(string, []string, string) (int, error) { return pid, nil }
	port := freePort(t)
	if _, err := r.start("s1", port); err != nil {
		t.Fatal(err)
	}
	id := "s1-" + strconv.Itoa(port)
	text, err := r.stop(id)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "s1") || !strings.Contains(text, strconv.Itoa(port)) {
		t.Fatalf("text = %q", text)
	}
	if !pidDead(pid) {
		t.Fatal("the child survived the stop")
	}
	if _, err := os.Stat(filepath.Join(r.dir, id)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the registry entry was not removed")
	}
	if _, err := r.stop(id); err == nil || !strings.Contains(err.Error(), "no active forward") {
		t.Fatalf("stopping again gave %v", err)
	}
}

func TestForwardMetaRoundTrip(t *testing.T) {
	started := time.Now().Truncate(time.Second).UTC()
	meta := forwardMeta{ID: "s1-8000", Session: "s1", Bind: "127.0.0.1", Local: 8000, Remote: 8000, PID: 123, Started: started}
	data, err := json.Marshal(meta)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"started":"`+started.Format(time.RFC3339)) {
		t.Fatalf("started is not RFC3339: %s", data)
	}
	var back forwardMeta
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatal(err)
	}
	if back != meta {
		t.Fatalf("round trip: %+v", back)
	}
}

func TestForwardIDAndDir(t *testing.T) {
	if id := forwardID("abc123", 8000); id != "abc123-8000" {
		t.Fatalf("id = %s", id)
	}
	if !strings.Contains(forwardsDir(), "forwards") {
		t.Fatalf("forwardsDir = %s", forwardsDir())
	}
}

func TestRemoveManagedForward(t *testing.T) {
	stateHome := t.TempDir()
	t.Setenv("XDG_STATE_HOME", stateHome)
	dir := filepath.Join(forwardsDir(), "s1-8000")
	writeMeta(t, dir, forwardMeta{ID: "s1-8000", Session: "s1", Bind: "127.0.0.1", Local: 8000, Remote: 8000, PID: 42, Started: time.Now()})
	if err := os.WriteFile(filepath.Join(dir, "log"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	removeManagedForward("s1", 8000)
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the registry entry survived the managed child's exit")
	}
	removeManagedForward("s2", 9000)
}

func TestPidDead(t *testing.T) {
	if !pidDead(0) || !pidDead(-1) {
		t.Fatal("a non-positive pid must count as dead")
	}
	pid, kill := sleepChild(t)
	defer kill()
	if pidDead(pid) {
		t.Fatal("a live child counts as dead")
	}
	_ = syscall.Kill(pid, syscall.SIGKILL)
	deadline := time.Now().Add(5 * time.Second)
	for pidDead(pid) == false && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !pidDead(pid) {
		t.Fatal("a killed child still counts as alive")
	}
}
