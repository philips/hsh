package cli

import (
	"context"
	"strings"
	"time"
)

var sessionStateResolve = resolveSessionState

type SessionState int

const (
	SessionAbsent SessionState = iota
	SessionStarting
	SessionAttached
	SessionDetached
	SessionSuspended
	SessionShellExited
	SessionStopped
)

func (s SessionState) Running() bool {
	return s == SessionAttached || s == SessionDetached
}

func (s SessionState) Reachable() bool { return s.Running() }

func (s SessionState) String() string {
	switch s {
	case SessionAbsent:
		return "Absent"
	case SessionStarting:
		return "Starting"
	case SessionAttached:
		return "Attached"
	case SessionDetached:
		return "Detached"
	case SessionSuspended:
		return "Suspended"
	case SessionShellExited:
		return "Shell exited"
	case SessionStopped:
		return "Stopped"
	default:
		return "Unknown"
	}
}

func (p podInfo) workspaceState() (SessionState, bool) {
	ws, ok := p.workspaceContainerStatus()
	if !ok {
		return 0, false
	}
	switch ws {
	case "paused":
		return SessionSuspended, true
	case "created":
		return SessionStarting, true
	case "running":
		if clientAttached(sessionDirFor(p.Name)) {
			return SessionAttached, true
		}
		return SessionDetached, true
	case "exited", "stopped":
		if strings.EqualFold(p.Status, "running") || strings.EqualFold(p.Status, "degraded") {
			return SessionShellExited, true
		}
		return SessionStopped, true
	}
	return 0, false
}

func sessionStateFromPod(p podInfo) SessionState {
	if st, ok := p.workspaceState(); ok {
		return st
	}
	if strings.EqualFold(p.Status, "running") || strings.EqualFold(p.Status, "degraded") {
		return SessionStarting
	}
	return SessionStopped
}

func resolveSessionState(id string) SessionState {
	podName := "hsh-" + id
	if !podExists(podName) {
		return SessionAbsent
	}
	wsName := podName + "-ws"
	switch containerStatus(wsName) {
	case "paused":
		return SessionSuspended
	case "created":
		return SessionStarting
	case "running":
		if clientAttached(sessionDirFor(podName)) {
			return SessionAttached
		}
		return SessionDetached
	case "exited", "stopped", "missing":
		if containerRunning(podName + "-proxy") {
			return SessionShellExited
		}
		return SessionStopped
	}
	return SessionStopped
}

func watchSessionReachable(ctx context.Context, cancel context.CancelFunc, sessionID string, onLeave func(SessionState)) {
	t := time.NewTicker(1500 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if st := sessionStateResolve(sessionID); !st.Reachable() {
				if onLeave != nil {
					onLeave(st)
				}
				cancel()
				return
			}
		}
	}
}
