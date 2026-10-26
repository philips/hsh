package cli

import (
	"encoding/json"
	"flag"

	"hsh/internal/netsetup"
	"hsh/internal/workspace"
)

func Workspace(args []string) error {
	fs := flag.NewFlagSet("workspace", flag.ContinueOnError)
	shell := fs.String("shell", "/bin/sh", "shell to execute")
	execJSON := fs.String("exec-json", "", "JSON array argv to execute instead of the shell")
	caCert := fs.String("ca-cert", "/hsh-run/ca.crt", "CA certificate path")
	proxyAddr := fs.String("proxy-addr", netsetup.ProxyAddr(), "egress proxy address")
	workspaceDir := fs.String("workspace", "/workspace", "workspace directory")
	home := fs.String("home", "/home/hsh", "home directory")
	innerExe := fs.String("inner-exe", "", "path to the hsh-workspace-inner binary (empty: re-exec self with the workspace-inner subcommand)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	var exec []string
	if *execJSON != "" {
		if err := json.Unmarshal([]byte(*execJSON), &exec); err != nil {
			return err
		}
	}
	return workspace.RunParent(workspace.ParentOptions{
		Shell:     *shell,
		Exec:      exec,
		Workspace: *workspaceDir,
		Home:      *home,
		CACert:    *caCert,
		ProxyAddr: *proxyAddr,
		InnerExe:  *innerExe,
	})
}

func WorkspaceInner(args []string) error {
	fs := flag.NewFlagSet("workspace-inner", flag.ContinueOnError)
	shell := fs.String("shell", "/bin/sh", "shell to execute")
	execJSON := fs.String("exec-json", "", "JSON array argv to execute instead of the shell")
	caCert := fs.String("ca-cert", "/hsh-run/ca.crt", "CA certificate path")
	proxyAddr := fs.String("proxy-addr", netsetup.ProxyAddr(), "egress proxy address")
	workspaceDir := fs.String("workspace", "/workspace", "workspace directory")
	home := fs.String("home", "/home/hsh", "home directory")
	if err := fs.Parse(args); err != nil {
		return err
	}
	var exec []string
	if *execJSON != "" {
		if err := json.Unmarshal([]byte(*execJSON), &exec); err != nil {
			return err
		}
	}
	return workspace.RunInner(workspace.InnerOptions{
		Shell:     *shell,
		Exec:      exec,
		Workspace: *workspaceDir,
		Home:      *home,
		CACert:    *caCert,
		ProxyAddr: *proxyAddr,
	})
}
