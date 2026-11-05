package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/peterbourgon/ff/v4"

	"hsh/internal/config"
	"hsh/internal/monitordb"
	"hsh/internal/state"
)

func containerPaused(name string) bool {
	out, err := podman("inspect", "--format", "{{.State.Paused}}", name)
	if err != nil {
		return false
	}
	return strings.TrimSpace(out) == "true"
}

func unpausePod(podName string) error {
	if out, err := podman("pod", "unpause", podName); err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(out))
	}
	return nil
}

func suspendCommand() *ff.Command {
	return newCommand("suspend", "hsh suspend [SESSION]",
		"suspend a session's processes to save CPU; re-enter it later with hsh resume", nil,
		func(ctx context.Context, args []string) error {
			if len(args) > 1 {
				return fmt.Errorf("suspend takes at most one session id")
			}
			id, err := resolveSessionArg(args)
			if err != nil {
				return err
			}
			podName := "hsh-" + id
			if !podExists(podName) {
				return fmt.Errorf("no session %s (see: hsh ls)", id)
			}
			if containerPaused(podName + "-ws") {
				fmt.Printf("session %s is already suspended\n", id)
				return nil
			}
			if !containerRunning(podName + "-ws") {
				return fmt.Errorf("session %s is not running, so there is nothing to suspend (see: hsh ls)", id)
			}
			sessionDir := state.SessionDir(id)
			if _, err := detachClient(sessionDir, 10*time.Second); err != nil {
				return err
			}
			if out, err := podman("pod", "pause", podName); err != nil {
				return fmt.Errorf("pausing session pod: %w: %s", err, strings.TrimSpace(out))
			}
			fmt.Printf("suspended session %s; resume it with: hsh resume %s\n", id, id)
			return nil
		})
}

const (
	suspendNoteText  = "SUSPENDED: proxy approval"
	resumeNoteText   = "hsh: resumed the agent; the request proceeds"
	suspendNotePrint = "\nhsh: suspended the agent while a blocked request waits for approval\n"
)

type suspendState struct {
	note atomic.Pointer[string]
	sink atomic.Bool
}

func (s *suspendState) set(note string) { s.note.Store(&note) }

func (s *suspendState) clear() { s.note.Store(nil) }

func (s *suspendState) current() string {
	if p := s.note.Load(); p != nil {
		return *p
	}
	return ""
}

func (s *suspendState) markSink() { s.sink.Store(true) }

func (s *suspendState) announce(suspended bool) {
	if s.sink.Load() {
		return
	}
	if suspended {
		fmt.Fprint(os.Stderr, suspendNotePrint)
	} else {
		fmt.Fprintln(os.Stderr, resumeNoteText)
	}
}

func suspendDelay(sessionDir string) time.Duration {
	if cfg, err := config.Load(filepath.Join(sessionDir, "config.json")); err == nil && cfg.Network.SuspendDelay != "" {
		return cfg.Network.SuspendDelayDuration()
	}
	if d, err := globalSuspendDelay(); err == nil {
		return d
	}
	return config.DefaultSuspendDelay
}

func watchHoldSuspend(ctx context.Context, sessionDir, wsName string, sstate *suspendState) {
	tick := time.NewTicker(150 * time.Millisecond)
	defer tick.Stop()
	suspended := false
	var heldSince time.Time
	defer func() {
		if suspended {
			_, _ = podman("unpause", wsName)
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			held, err := monitordb.HoldingCount(sessionDir)
			if err != nil {
				continue
			}
			switch {
			case held > 0 && !suspended:
				delay := suspendDelay(sessionDir)
				if delay <= 0 {
					heldSince = time.Time{}
					continue
				}
				if heldSince.IsZero() {
					heldSince = time.Now()
				}
				if time.Since(heldSince) < delay {
					continue
				}
				if !containerRunning(wsName) || containerPaused(wsName) {
					continue
				}
				if _, err := podman("pause", wsName); err == nil {
					suspended = true
					sstate.set(suspendNoteText)
					sstate.announce(true)
				}
			case held == 0:
				heldSince = time.Time{}
				if suspended {
					if _, err := podman("unpause", wsName); err == nil {
						suspended = false
						sstate.clear()
						sstate.announce(false)
					}
				}
			}
		}
	}
}
