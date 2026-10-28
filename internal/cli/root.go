package cli

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/peterbourgon/ff/v4"
	"github.com/peterbourgon/ff/v4/ffhelp"

	"hsh/internal/denieddb"
	"hsh/internal/secretstore"
	"hsh/internal/version"
	"hsh/internal/vt"
)

func newCommand(name, usage, shortHelp string, define func(*ff.FlagSet), run func(context.Context, []string) error) *ff.Command {
	fs := ff.NewFlagSet(name)
	help := fs.Bool('h', "help", "show help")
	if define != nil {
		define(fs)
	}
	cmd := &ff.Command{
		Name:      name,
		Usage:     usage,
		ShortHelp: shortHelp,
		Flags:     fs,
	}
	cmd.Exec = func(ctx context.Context, args []string) error {
		if *help {
			fmt.Println(ffhelp.Command(cmd))
			return nil
		}
		return run(ctx, args)
	}
	return cmd
}

type unknownCommandError struct{ cmd string }

func (e *unknownCommandError) Error() string {
	return fmt.Sprintf("unknown command %q", e.cmd)
}

func IsUnknownCommand(err error) bool {
	var u *unknownCommandError
	return errors.As(err, &u)
}

var (
	verbose = os.Getenv("HSH_VERBOSE") != ""
)

func infof(format string, args ...any) {
	if verbose {
		fmt.Fprintf(os.Stderr, format, args...)
	}
}

func Root() *ff.Command {
	var root *ff.Command
	root = &ff.Command{
		Name:      "hsh",
		Usage:     "hsh [FLAGS] COMMAND [FLAGS] [ARG]...",
		ShortHelp: "container sandbox for agent harness isolation",
		Exec: func(ctx context.Context, args []string) error {
			if len(args) == 0 {
				fmt.Print(ffhelp.Command(root))
				return nil
			}
			fmt.Fprint(os.Stderr, ffhelp.Command(root))
			return &unknownCommandError{cmd: args[0]}
		},
		Subcommands: []*ff.Command{
			runCommand(),
			secretCommand(),
			proxyCommand(),
			monitorCommand(),
			oauthCommand(),
			lsCommand(),
			rmCommand(),
			gcCommand(),
			detachCommand(),
			versionCommand(),
		},
	}
	root.Subcommands = append(root.Subcommands, helpCommand(root))
	return root
}

func Run(args []string) error {
	root := Root()
	err := root.ParseAndRun(context.Background(), args)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, ff.ErrHelp):
		if sel := root.GetSelected(); sel != nil {
			fmt.Print(ffhelp.Command(sel))
			return nil
		}
	case IsUnknownCommand(err):
		fmt.Fprint(os.Stderr, ffhelp.Command(root))
	}
	return err
}

func groupExec(self **ff.Command) func(context.Context, []string) error {
	return func(ctx context.Context, args []string) error {
		if len(args) == 0 {
			fmt.Print(ffhelp.Command(*self))
			return nil
		}
		fmt.Fprint(os.Stderr, ffhelp.Command(*self))
		return fmt.Errorf("%s: unknown subcommand %q", (*self).Name, args[0])
	}
}

func versionCommand() *ff.Command {
	return newCommand("version", "hsh version", "print the hsh version, git commit, and build platform", nil, func(ctx context.Context, args []string) error {
		if len(args) > 0 {
			return fmt.Errorf("version takes no arguments")
		}
		fmt.Println(version.Get())
		if vt.Available() {
			fmt.Printf("terminal emulator: %s (inline proxy review enabled)\n", vt.Backend)
		} else {
			fmt.Println("terminal emulator: none (built without GOEXPERIMENT=simd; inline proxy review disabled)")
		}
		return nil
	})
}

func helpCommand(root *ff.Command) *ff.Command {
	return newCommand("help", "hsh help [COMMAND]", "show help for a command", nil, func(ctx context.Context, args []string) error {
		if len(args) == 0 {
			if err := root.Reset(); err != nil {
				return err
			}
			fmt.Print(ffhelp.Command(root))
			return nil
		}
		var found *ff.Command
		for _, sub := range root.Subcommands {
			if sub.Name == args[0] {
				found = sub
				break
			}
		}
		if found == nil {
			return fmt.Errorf("no such command %q", args[0])
		}
		if err := found.Reset(); err != nil {
			return err
		}
		fmt.Print(ffhelp.Command(found))
		return nil
	})
}

func sessionFilterFlags(fs *ff.FlagSet, f *denieddb.Filter) {
	fs.StringVar(&f.Session, 0, "session", "", "only this session (session id)")
	fs.StringVar(&f.Workspace, 0, "workspace", "", "only this workspace (directory path)")
}

func secretDBFlag(fs *ff.FlagSet) *string {
	return fs.String(0, "db", secretstore.DefaultPath(), "path to the secret store")
}
