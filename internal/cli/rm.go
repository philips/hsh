package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/peterbourgon/ff/v4"
)

type notFoundError struct{ msg string }

func (e *notFoundError) Error() string { return e.msg }

func rmCommand() *ff.Command {
	var all, force, ignore, latest bool
	var timeout int
	return newCommand("rm", "hsh rm [FLAGS] SANDBOX...", "remove hsh sandboxes, like podman pod rm", func(fs *ff.FlagSet) {
		fs.BoolVar(&all, 'a', "all", "remove all hsh sandboxes")
		fs.BoolVar(&force, 'f', "force", "stop running containers first, then remove")
		fs.BoolVar(&ignore, 'i', "ignore", "do not fail when a specified sandbox is missing")
		fs.IntVar(&timeout, 't', "time", 10, "seconds to wait for pod stop before killing")
		fs.BoolVar(&latest, 'l', "latest", "act on the most recently created sandbox")
	}, func(ctx context.Context, args []string) error {
		if !all && !latest && len(args) == 0 {
			return fmt.Errorf("specify a sandbox, or --all/--latest")
		}
		pods, err := listSandboxes()
		if err != nil {
			return err
		}
		pods = sandboxPods(pods)
		targets, err := resolveTargets(args, pods, all, latest, ignore)
		if err != nil {
			return err
		}
		failed := false
		for _, p := range targets {
			rmArgs := []string{"pod", "rm"}
			if force {
				rmArgs = append(rmArgs, "--force", "--time", fmt.Sprint(timeout))
			}
			rmArgs = append(rmArgs, p.Name)
			out, err := podman(rmArgs...)
			if err != nil {
				fmt.Fprintf(os.Stderr, "hsh: removing %s: %v\n%s", p.Name, err, out)
				failed = true
				continue
			}
			removed, err := removeSessionDir(p.Name)
			if err != nil {
				fmt.Fprintf(os.Stderr, "hsh: removing session state for %s: %v\n", p.Name, err)
				failed = true
				continue
			}
			if removed {
				fmt.Printf("removed %s (and its session state)\n", p.Name)
			} else {
				fmt.Printf("removed %s\n", p.Name)
			}
		}
		if all {
			n, err := sweepSessionDirs()
			if err != nil {
				fmt.Fprintf(os.Stderr, "hsh: sweeping session state: %v\n", err)
				failed = true
			} else if n > 0 {
				fmt.Printf("removed %d session state director%s\n", n, pluralY(n))
			}
		}
		if failed {
			return fmt.Errorf("some sandboxes could not be removed")
		}
		return nil
	})
}

func pluralY(n int) string {
	if n == 1 {
		return "y"
	}
	return "ies"
}

func resolveTargets(args []string, pods []podInfo, all, latest, ignore bool) ([]podInfo, error) {
	if all {
		return pods, nil
	}
	if latest {
		if p, ok := latestPod(pods); ok {
			return []podInfo{p}, nil
		}
		return nil, notFound("no hsh sandboxes found", ignore)
	}
	var targets []podInfo
	for _, arg := range args {
		matched, err := matchPods(arg, pods)
		if err != nil {
			return nil, err
		}
		if len(matched) == 0 {
			return nil, notFound(matchError(arg), ignore)
		}
		targets = append(targets, matched...)
	}
	return targets, nil
}

func matchError(arg string) string {
	if abs, err := filepath.Abs(arg); err == nil {
		if st, err := os.Stat(abs); err == nil && st.IsDir() {
			return fmt.Sprintf("no hsh sandbox found for workspace %s", abs)
		}
	}
	return fmt.Sprintf("no hsh sandbox found matching %q", arg)
}

func matchPods(arg string, pods []podInfo) ([]podInfo, error) {
	if abs, err := filepath.Abs(arg); err == nil {
		if st, err := os.Stat(abs); err == nil && st.IsDir() {
			var matched []podInfo
			for _, p := range pods {
				if p.Workspace() == abs || p.Workspace() == arg {
					matched = append(matched, p)
				}
			}
			return matched, nil
		}
	}
	var matched []podInfo
	for _, p := range pods {
		if p.Name == arg || p.Session() == arg || strings.HasPrefix(p.ID, arg) {
			matched = append(matched, p)
		}
	}
	if len(matched) > 1 {
		return nil, fmt.Errorf("ambiguous sandbox %q matches %d pods", arg, len(matched))
	}
	return matched, nil
}

func notFound(msg string, ignore bool) error {
	err := &notFoundError{msg}
	if ignore {
		fmt.Fprintf(os.Stderr, "hsh: %v (ignored)\n", err)
		return nil
	}
	return err
}
