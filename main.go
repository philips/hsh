package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"hsh/internal/cli"
)

func main() {
	args := os.Args[1:]
	var err error
	switch strings.TrimSuffix(filepath.Base(os.Args[0]), ".exe") {
	case "hsh-proxy":
		err = cli.Proxy(args)
	case "hsh-workspace":
		err = cli.Workspace(args)
	case "hsh-workspace-inner":
		err = cli.WorkspaceInner(args)
	default:
		err = hostCommand(args)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "hsh: %v\n", err)
		if cli.IsUnknownCommand(err) {
			os.Exit(2)
		}
		os.Exit(1)
	}
}

func hostCommand(args []string) error {
	return cli.Run(args)
}
