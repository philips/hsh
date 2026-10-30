package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/peterbourgon/ff/v4"

	"hsh/internal/monitordb"
	"hsh/internal/secretstore"
)

func resumeCommand() *ff.Command {
	ro := runOptions{
		monitorDB: monitordb.DefaultPath(),
		secretDB:  secretstore.DefaultPath(),
		pull:      "never",
		resume:    true,
	}
	fs := ff.NewFlagSet("resume")
	fs.StringVar(&ro.monitorDB, 0, "monitor-db", monitordb.DefaultPath(), "monitor database")
	fs.StringVar(&ro.secretDB, 0, "secret-db", secretstore.DefaultPath(), "secret store for hsh:// secret references")
	fs.BoolVar(&ro.verbose, 0, "verbose", "print progress information to stderr (or set HSH_VERBOSE=1)")
	fs.BoolVar(&ro.takeOver, 0, "take-over", "detach whatever terminal is attached to the session first")
	fs.Value(0, "forward", &ro.forwards, "while attached, make the session's port reachable on 127.0.0.1: PORT or LOCAL:PORT (repeatable; see hsh forward)")
	return &ff.Command{
		Name:      "resume",
		Usage:     "hsh resume [FLAGS] [SESSION | WORKSPACE-DIR]",
		ShortHelp: "re-enter an existing session (attach to its shell, or restart it if it exited)",
		Flags:     fs,
		Exec: func(ctx context.Context, args []string) error {
			if len(args) > 1 {
				return fmt.Errorf("unexpected arguments %v: at most one positional (a session id or a workspace directory) is accepted, and flags must come before it", args)
			}
			arg := ""
			if len(args) == 1 {
				arg = args[0]
			}
			cwd, err := os.Getwd()
			if err != nil {
				return err
			}
			pods, err := listSandboxes()
			if err != nil {
				return err
			}
			id, err := resolveResumeTarget(arg, cwd, sandboxPods(pods))
			if err != nil {
				return err
			}
			ro.sessionID = id
			return run(ctx, ro, "", "")
		},
	}
}

func isDirectory(path string) (string, bool) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", false
	}
	st, err := os.Stat(abs)
	if err != nil || !st.IsDir() {
		return "", false
	}
	return abs, true
}

func resolveResumeTarget(arg, cwd string, pods []podInfo) (string, error) {
	workspace := ""
	if arg == "" {
		workspace = cwd
	} else if abs, ok := isDirectory(arg); ok {
		workspace = abs
	}
	if workspace != "" {
		var matched []podInfo
		for _, p := range pods {
			if p.Workspace() == workspace {
				matched = append(matched, p)
			}
		}
		switch len(matched) {
		case 0:
			return "", fmt.Errorf("no session for workspace %s; start one with: hsh run IMAGE %s", workspace, workspace)
		case 1:
			return matched[0].Session(), nil
		}
		var b strings.Builder
		fmt.Fprintf(&b, "multiple sessions for workspace %s:", workspace)
		for _, p := range matched {
			created := ""
			if t := p.CreatedTime(); !t.IsZero() {
				created = t.Local().Format("2006-01-02 15:04")
			}
			fmt.Fprintf(&b, "\n  %s  %s  %s", p.Session(), sessionStatus(p), created)
		}
		b.WriteString("\npick one with: hsh resume <session id>")
		return "", fmt.Errorf("%s", b.String())
	}
	matched, err := matchPods(arg, pods)
	if err != nil {
		return "", err
	}
	if len(matched) == 0 {
		return "", fmt.Errorf("no session matching %q (see: hsh ls)", arg)
	}
	return matched[0].Session(), nil
}
