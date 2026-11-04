package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/peterbourgon/ff/v4"

	"hsh/internal/settings"
	"hsh/internal/state"
)

func wipeCommand() *ff.Command {
	return newCommand("wipe", "hsh wipe", "remove all hsh configuration and state files (asks for confirmation)", nil, func(ctx context.Context, args []string) error {
		if len(args) > 0 {
			return fmt.Errorf("wipe takes no arguments")
		}
		pods, podsErr := listSandboxes()
		return runWipe(os.Stdin, os.Stdout, pods, podsErr)
	})
}

func runWipe(in io.Reader, out io.Writer, pods []podInfo, podsErr error) error {
	if podsErr != nil {
		fmt.Fprintf(out, "warning: could not check for existing sandboxes: %v\n", podsErr)
	} else if n := len(sandboxPods(pods)); n > 0 {
		fmt.Fprintf(out, "refusing to wipe: %d hsh sandbox(es) still exist\n", n)
		fmt.Fprintln(out, "remove them first with: hsh rm --all --force")
		return fmt.Errorf("sandboxes present")
	}
	cfgDir := settings.Dir()
	stateDir := state.Dir()
	fmt.Fprintln(out, "this will permanently remove:")
	fmt.Fprintf(out, "  %s (settings, secrets, trusted images, oauth providers, image shortnames)\n", cfgDir)
	fmt.Fprintf(out, "  %s (sessions with their proxy and monitor databases, metrics, crash log)\n", stateDir)
	fmt.Fprint(out, "type YES to wipe: ")
	answer, err := bufio.NewReader(in).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	if strings.TrimSpace(strings.TrimSuffix(answer, "\n")) != "YES" {
		fmt.Fprintln(out, "\naborted, nothing removed")
		return nil
	}
	for _, dir := range []string{cfgDir, stateDir} {
		if err := os.RemoveAll(dir); err != nil {
			return fmt.Errorf("removing %s: %w", dir, err)
		}
	}
	fmt.Fprintln(out, "wiped hsh configuration and state")
	return nil
}
