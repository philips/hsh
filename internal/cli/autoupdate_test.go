package cli

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"hsh/internal/update"
	"hsh/internal/version"
)

func runUpdateCmd(t *testing.T, args ...string) (string, error) {
	t.Helper()
	saved := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	cmdErr := func() error {
		cmd := updateCommand()
		if err := cmd.Parse(args[1:]); err != nil {
			return err
		}
		return cmd.Run(context.Background())
	}()
	w.Close()
	os.Stdout = saved
	out, _ := io.ReadAll(r)
	return string(out), cmdErr
}

func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	saved := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = w
	fn()
	w.Close()
	os.Stderr = saved
	out, _ := io.ReadAll(r)
	return string(out)
}

func TestUpdateAutoToggle(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	path := update.DefaultPath()

	out, err := runUpdateCmd(t, "update", "--auto")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "automatic updates enabled") {
		t.Errorf("--auto output: %q", out)
	}
	if !update.AutoEnabled(path) {
		t.Error("--auto did not enable autoupdate")
	}

	out, err = runUpdateCmd(t, "update", "--no-auto")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "automatic updates disabled") {
		t.Errorf("--no-auto output: %q", out)
	}
	if update.AutoEnabled(path) {
		t.Error("--no-auto did not disable autoupdate")
	}

	if _, err := runUpdateCmd(t, "update", "--auto", "--no-auto"); err == nil {
		t.Error("--auto and --no-auto together should fail")
	}
}

func TestAutoupdateCheck(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)
	path := update.DefaultPath()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/latest":
			http.Redirect(w, r, "/tag/v9.9.9", http.StatusFound)
		case strings.HasPrefix(r.URL.Path, "/tag/"):
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	t.Setenv("HSH_RELEASE_BASE", srv.URL)

	out, err := runUpdateCmd(t, "update", "--autoupdate-check")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "hsh v9.9.9 is available") {
		t.Errorf("check output: %q", out)
	}
	if got := update.KnownLatest(path); got != "v9.9.9" {
		t.Errorf("KnownLatest = %q, want v9.9.9", got)
	}
	if update.CheckDue(path, time.Hour) {
		t.Error("a check just ran, so another is not due")
	}

	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusInternalServerError)
	}))
	defer bad.Close()
	t.Setenv("HSH_RELEASE_BASE", bad.URL)
	if _, err := runUpdateCmd(t, "update", "--autoupdate-check"); err != nil {
		t.Errorf("a failed check must not fail the command: %v", err)
	}
}

func TestMaybeAutoUpdate(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)
	t.Setenv("HSH_RELEASE_BASE", "http://127.0.0.1:1")
	path := update.DefaultPath()

	out := captureStderr(t, maybeAutoUpdate)
	if out != "" {
		t.Errorf("autoupdate disabled printed: %q", out)
	}

	if err := update.SetAuto(path, true); err != nil {
		t.Fatal(err)
	}
	if err := update.SetLatest(path, "v9.9.9"); err != nil {
		t.Fatal(err)
	}

	out = captureStderr(t, maybeAutoUpdate)
	if !strings.Contains(out, "hsh v9.9.9 is available (this is a dev build)") {
		t.Errorf("dev notice: %q", out)
	}
	if strings.Contains(out, "installed") {
		t.Errorf("a dev build must not update inline: %q", out)
	}

	t.Setenv("HSH_NO_UPDATE_CHECK", "1")
	out = captureStderr(t, maybeAutoUpdate)
	if out != "" {
		t.Errorf("HSH_NO_UPDATE_CHECK printed: %q", out)
	}
	t.Setenv("HSH_NO_UPDATE_CHECK", "")

	saved := version.Tag
	version.Tag = "v1.2.3"
	defer func() { version.Tag = saved }()
	out = captureStderr(t, maybeAutoUpdate)
	if !strings.Contains(out, "hsh v9.9.9 is available (this is v1.2.3), updating") {
		t.Errorf("release notice: %q", out)
	}
	if !strings.Contains(out, "automatic update to v9.9.9 failed") {
		t.Errorf("failed inline update must warn: %q", out)
	}
}

func TestCheckInterval(t *testing.T) {
	if got := checkInterval(); got != 4*time.Hour {
		t.Errorf("default interval = %v, want 4h", got)
	}
	t.Setenv("HSH_UPDATE_CHECK_INTERVAL", "30m")
	if got := checkInterval(); got != time.Hour {
		t.Errorf("sub-floor interval = %v, want 1h", got)
	}
	t.Setenv("HSH_UPDATE_CHECK_INTERVAL", "6h")
	if got := checkInterval(); got != 6*time.Hour {
		t.Errorf("6h interval = %v, want 6h", got)
	}
	t.Setenv("HSH_UPDATE_CHECK_INTERVAL", "banana")
	if got := checkInterval(); got != 4*time.Hour {
		t.Errorf("invalid interval = %v, want the 4h default", got)
	}
}

func TestRunHooksSkipUpdate(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)
	path := update.DefaultPath()
	if err := update.SetAuto(path, true); err != nil {
		t.Fatal(err)
	}
	if err := update.SetLatest(path, "v9.9.9"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HSH_RELEASE_BASE", "http://127.0.0.1:1")

	var ran bool
	savedAuto, savedSpawn := autoUpdateHook, spawnCheckHook
	autoUpdateHook = func() { ran = true }
	spawnCheckHook = func() {}
	defer func() { autoUpdateHook, spawnCheckHook = savedAuto, savedSpawn }()

	if err := Run([]string{"version"}); err != nil {
		t.Fatal(err)
	}
	if !ran {
		t.Error("host commands did not run the autoupdate hook")
	}

	ran = false
	if err := Run([]string{"update", "--no-auto"}); err != nil {
		t.Fatal(err)
	}
	if ran {
		t.Error("the update command must not run the autoupdate hook")
	}
	if update.AutoEnabled(path) {
		t.Error("--no-auto via Run did not disable autoupdate")
	}
}
