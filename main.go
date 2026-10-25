package main

import (
	"fmt"
	"os"
	"strings"

	"harnessh/internal/cli"
)

const usage = `harnessh - container sandbox for agent harness isolation

usage:
  harnessh [run] [--config config.harnessh]   start a sandboxed shell (default command)
  harnessh proxy [flags]                       egress proxy (runs inside the sandbox pod)
  harnessh workspace [flags]                   workspace entrypoint (runs inside the pod)
  harnessh workspace-inner [flags]             internal: workspace child in its own netns
  harnessh proxy-denied [--db PATH] [--all] [--dns]  list hosts denied by the egress proxy
                                               (--dns lists DNS query denials)
  harnessh proxy-auto-allow [--config PATH]    allow denied hosts in a config file

run flags:
  --config PATH     path to the config file (default: config.harnessh)
  --workspace PATH  workspace directory inside the container (default: first mount target)
  --denied-db PATH  proxy denial database (default: $XDG_STATE_HOME/harnessh/denied.db)
  --verbose         print progress information to stderr (or set HARNESSH_VERBOSE=1)
  --resume          reuse the sandbox for this workspace directory: reattach to the
                    existing container instead of creating a new one; the sandbox
                    is left in place on exit so a later --resume re-enters it
  --fresh           with --resume: discard the existing sandbox and start a new one

Progress information is suppressed by default so the sandbox starts with a
clean terminal prompt; warnings and errors are always printed.
`

func main() {
	args := os.Args[1:]
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		args = append([]string{"run"}, args...)
	}
	cmd := args[0]
	rest := args[1:]
	var err error
	switch cmd {
	case "run":
		err = cli.Run(rest)
	case "proxy":
		err = cli.Proxy(rest)
	case "workspace":
		err = cli.Workspace(rest)
	case "workspace-inner":
		err = cli.WorkspaceInner(rest)
	case "proxy-denied":
		err = cli.ProxyDenied(rest)
	case "proxy-auto-allow":
		err = cli.ProxyAutoAllow(rest)
	case "help", "-h", "--help":
		fmt.Print(usage)
		return
	default:
		fmt.Fprintf(os.Stderr, "harnessh: unknown command %q\n\n%s", cmd, usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "harnessh: %v\n", err)
		os.Exit(1)
	}
}
