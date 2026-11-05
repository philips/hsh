package cli

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/peterbourgon/ff/v4"

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
		"freeze a session's processes to save CPU; re-enter it later with hsh resume", nil,
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
