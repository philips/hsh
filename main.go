package main

import (
	"fmt"
	"os"
	"strings"

	"hsh/internal/cli"
)

const usage = `hsh - container sandbox for agent harness isolation

usage:
  hsh [run] [--config config.hsh]   start a sandboxed shell (default command)
  hsh proxy [flags]                       egress proxy (runs inside the sandbox pod)
  hsh workspace [flags]                   workspace entrypoint (runs inside the pod)
  hsh workspace-inner [flags]             internal: workspace child in its own netns
  hsh proxy-denied [--db PATH] [--all] [--dns]  list hosts denied by the egress proxy
                                               (--dns lists DNS query denials)
  hsh proxy-auto-allow [--config PATH]    allow denied hosts in a config file
  hsh proxy-watch [--config PATH] [--db PATH] [--all]
                                               interactive review of denied hosts (TUI):
                                               approve or ignore them one by one
  hsh ls [flags]                               list hsh sandboxes (pods), like podman pod ls
  hsh rm [flags] SANDBOX...                    remove hsh sandboxes, like podman pod rm
                                               (-a/--all, -f/--force, -i/--ignore, -t/--time, -l/--latest;
                                               an existing directory argument resolves to its workspace's sandbox)

run flags:
  hsh run [IMAGE] [flags]                      IMAGE is a positional alias for --image
  --config PATH     path to the config file (default: config.hsh)
  --image IMAGE     workspace image; without a config file the image's
                    org.ifup.hsh label provides the default config
  --tasks PATH      newline-delimited tasks (or - for stdin); runs the
                    image's agent headless with the tasks as its prompt
  --workspace PATH  workspace directory inside the container (default: first mount target)
  --denied-db PATH  proxy denial database (default: $XDG_STATE_HOME/hsh/denied.db)
  --verbose         print progress information to stderr (or set HSH_VERBOSE=1)
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
	case "proxy-watch":
		err = cli.ProxyWatch(rest)
	case "ls":
		err = cli.Ls(rest)
	case "rm":
		err = cli.Rm(rest)
	case "help", "-h", "--help":
		fmt.Print(usage)
		return
	default:
		fmt.Fprintf(os.Stderr, "hsh: unknown command %q\n\n%s", cmd, usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "hsh: %v\n", err)
		os.Exit(1)
	}
}
