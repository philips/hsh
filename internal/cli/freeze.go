package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	"hsh/internal/config"
	"hsh/internal/monitordb"
)

const freezeOnHoldEnv = "HSH_FREEZE_ON_HOLD"

const (
	freezeNoteText  = "SUSPENDED: proxy approval"
	thawNoteText    = "hsh: thawed the agent; the request proceeds"
	freezeNotePrint = "\nhsh: froze the agent while a blocked request waits for approval\n"
)

type freezeState struct {
	note atomic.Pointer[string]
	sink atomic.Bool
}

func (f *freezeState) set(note string) { f.note.Store(&note) }

func (f *freezeState) clear() { f.note.Store(nil) }

func (f *freezeState) current() string {
	if p := f.note.Load(); p != nil {
		return *p
	}
	return ""
}

func (f *freezeState) markSink() { f.sink.Store(true) }

func (f *freezeState) announce(set bool) {
	if f.sink.Load() {
		return
	}
	if set {
		fmt.Fprint(os.Stderr, freezeNotePrint)
	} else {
		fmt.Fprintln(os.Stderr, thawNoteText)
	}
}

func freezeOnHoldEnabled() bool {
	return os.Getenv(freezeOnHoldEnv) == "1"
}

func freezeDelay(sessionDir string) time.Duration {
	if cfg, err := config.Load(filepath.Join(sessionDir, "config.json")); err == nil && cfg.Network.FreezeDelay != "" {
		return cfg.Network.FreezeDelayDuration()
	}
	if d, err := globalFreezeDelay(); err == nil {
		return d
	}
	return config.DefaultFreezeDelay
}

func watchHoldFreeze(ctx context.Context, sessionDir, wsName string, fstate *freezeState) {
	if !freezeOnHoldEnabled() {
		return
	}
	tick := time.NewTicker(150 * time.Millisecond)
	defer tick.Stop()
	frozen := false
	var heldSince time.Time
	defer func() {
		if frozen {
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
			case held > 0 && !frozen:
				if heldSince.IsZero() {
					heldSince = time.Now()
				}
				if time.Since(heldSince) < freezeDelay(sessionDir) {
					continue
				}
				if !containerRunning(wsName) || containerPaused(wsName) {
					continue
				}
				if _, err := podman("pause", wsName); err == nil {
					frozen = true
					fstate.set(freezeNoteText)
					fstate.announce(true)
				}
			case held == 0:
				heldSince = time.Time{}
				if frozen {
					if _, err := podman("unpause", wsName); err == nil {
						frozen = false
						fstate.clear()
						fstate.announce(false)
					}
				}
			}
		}
	}
}
