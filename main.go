package main

import (
	"fmt"
	"os"

	"harnessh/internal/cli"
)

const usage = `harnessh - container sandbox for agent harness isolation

usage:
  harnessh [run] [--config config.harnessh]   start a sandboxed shell (default command)
  harnessh proxy [flags]                       egress proxy (runs inside the sandbox pod)
  harnessh workspace [flags]                   workspace entrypoint (runs inside the pod)
  harnessh workspace-inner [flags]             internal: workspace child in its own netns

run flags:
  --config PATH     path to the config file (default: config.harnessh)
  --workspace PATH  workspace directory inside the container (default: first mount target)
`

func main() {
	args := os.Args[1:]
	if len(args) == 0 {
		args = []string{"run"}
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
