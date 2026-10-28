package cli

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"time"

	"hsh/internal/update"
	"hsh/internal/version"
)

const (
	defaultCheckInterval = 4 * time.Hour
	minCheckInterval     = time.Hour
	checkTickerEvery     = 15 * time.Minute
)

var (
	autoUpdateHook = maybeAutoUpdate
	spawnCheckHook = maybeSpawnUpdateCheck
)

func updateCheckDisabled() bool {
	return os.Getenv("HSH_NO_UPDATE_CHECK") == "1"
}

func checkInterval() time.Duration {
	interval := defaultCheckInterval
	if v := os.Getenv("HSH_UPDATE_CHECK_INTERVAL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			interval = d
		}
	}
	if interval < minCheckInterval {
		interval = minCheckInterval
	}
	return interval
}

func maybeAutoUpdate() {
	if updateCheckDisabled() || !update.AutoEnabled(update.DefaultPath()) {
		return
	}
	path := update.DefaultPath()
	current := version.Get()
	latest := update.KnownLatest(path)
	if !update.Newer(current.Tag, latest) {
		return
	}
	if current.Tag == "" {
		fmt.Fprintf(os.Stderr, "hsh %s is available (this is a dev build): run `hsh update` to upgrade\n", latest)
		return
	}
	fmt.Fprintf(os.Stderr, "hsh %s is available (this is %s), updating...\n", latest, current.Tag)
	if err := runUpdate(updateRunFromEnv()); err != nil {
		fmt.Fprintf(os.Stderr, "hsh: warning: automatic update to %s failed: %v (run `hsh update` to retry)\n", latest, err)
	}
}

func maybeSpawnUpdateCheck() {
	if updateCheckDisabled() || !update.AutoEnabled(update.DefaultPath()) {
		return
	}
	if !update.CheckDue(update.DefaultPath(), checkInterval()) {
		return
	}
	spawnUpdateCheck()
}

func spawnUpdateCheck() {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	devnull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return
	}
	defer devnull.Close()
	cmd := exec.Command(exe, "update", "--autoupdate-check")
	cmd.Args[0] = "hsh-autoupdate"
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Stdout = devnull
	cmd.Stderr = devnull
	if err := cmd.Start(); err != nil {
		return
	}
	cmd.Process.Release()
}

func watchUpdateChecks() {
	ticker := time.NewTicker(checkTickerEvery)
	defer ticker.Stop()
	for range ticker.C {
		maybeSpawnUpdateCheck()
	}
}
