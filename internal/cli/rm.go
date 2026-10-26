package cli

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// notFoundError marks a sandbox that does not exist (ignored with --ignore).
type notFoundError struct{ msg string }

func (e *notFoundError) Error() string { return e.msg }

// Rm implements `hsh rm`: remove hsh sandboxes, following `podman pod rm`.
func Rm(args []string) error {
	fs := flag.NewFlagSet("rm", flag.ContinueOnError)
	var all bool
	fs.BoolVar(&all, "all", false, "remove all hsh sandboxes")
	fs.BoolVar(&all, "a", false, "remove all hsh sandboxes")
	var force bool
	fs.BoolVar(&force, "force", false, "stop running containers first, then remove")
	fs.BoolVar(&force, "f", false, "stop running containers first, then remove")
	var ignore bool
	fs.BoolVar(&ignore, "ignore", false, "do not fail when a specified sandbox is missing")
	fs.BoolVar(&ignore, "i", false, "do not fail when a specified sandbox is missing")
	var timeout int
	fs.IntVar(&timeout, "time", 10, "seconds to wait for pod stop before killing")
	fs.IntVar(&timeout, "t", 10, "seconds to wait for pod stop before killing")
	var latest bool
	fs.BoolVar(&latest, "latest", false, "act on the most recently created sandbox")
	fs.BoolVar(&latest, "l", false, "act on the most recently created sandbox")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if !all && !latest && fs.NArg() == 0 {
		return fmt.Errorf("specify a sandbox, or --all/--latest")
	}

	pods, err := listSandboxes()
	if err != nil {
		return err
	}
	pods = sandboxPods(pods)
	targets, err := resolveTargets(fs.Args(), pods, all, latest)
	if err != nil {
		var nf *notFoundError
		if ignore && errors.As(err, &nf) {
			fmt.Fprintf(os.Stderr, "hsh: %v (ignored)\n", err)
			return nil
		}
		return err
	}

	failed := false
	for _, p := range targets {
		rmArgs := []string{"pod", "rm"}
		if force {
			rmArgs = append(rmArgs, "--force")
		}
		rmArgs = append(rmArgs, "--time", fmt.Sprint(timeout), p.Name)
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
			fmt.Printf("removed %d session state director(y/ies)\n", n)
		}
	}
	if failed {
		return fmt.Errorf("some sandboxes could not be removed")
	}
	return nil
}

// resolveTargets maps rm arguments to sandbox pods. An argument that is an
// existing directory resolves to the sandbox labeled with that workspace;
// otherwise it matches a sandbox by name or ID prefix.
func resolveTargets(args []string, pods []podInfo, all, latest bool) ([]podInfo, error) {
	if all {
		return pods, nil
	}
	if latest {
		if len(pods) == 0 {
			return nil, &notFoundError{"no hsh sandboxes found"}
		}
		best := pods[0]
		for _, p := range pods[1:] {
			if p.CreatedTime().After(best.CreatedTime()) {
				best = p
			}
		}
		return []podInfo{best}, nil
	}
	if len(args) == 0 {
		return nil, fmt.Errorf("specify a sandbox, or --all/--latest")
	}
	var targets []podInfo
	for _, arg := range args {
		if abs, err := filepath.Abs(arg); err == nil {
			if st, err := os.Stat(abs); err == nil && st.IsDir() {
				var matched []podInfo
				for _, p := range pods {
					if p.Workspace() == abs || p.Workspace() == arg {
						matched = append(matched, p)
					}
				}
				if len(matched) == 0 {
					return nil, &notFoundError{fmt.Sprintf("no hsh sandbox found for workspace %s", abs)}
				}
				targets = append(targets, matched...)
				continue
			}
		}
		var matched []podInfo
		for _, p := range pods {
			if p.Name == arg || strings.HasPrefix(p.ID, arg) {
				matched = append(matched, p)
			}
		}
		if len(matched) == 0 {
			return nil, &notFoundError{fmt.Sprintf("no hsh sandbox found matching %q", arg)}
		}
		if len(matched) > 1 {
			return nil, fmt.Errorf("ambiguous sandbox %q matches %d pods", arg, len(matched))
		}
		targets = append(targets, matched[0])
	}
	return targets, nil
}
