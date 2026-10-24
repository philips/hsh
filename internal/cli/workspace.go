package cli

import (
	"flag"

	"harnessh/internal/netsetup"
	"harnessh/internal/workspace"
)

func Workspace(args []string) error {
	fs := flag.NewFlagSet("workspace", flag.ContinueOnError)
	shell := fs.String("shell", "/bin/sh", "shell to execute")
	caCert := fs.String("ca-cert", "/harnessh-run/ca.crt", "CA certificate path")
	proxyAddr := fs.String("proxy-addr", netsetup.ProxyAddr(), "egress proxy address")
	workspaceDir := fs.String("workspace", "/workspace", "workspace directory")
	home := fs.String("home", "/home/harnessh", "home directory")
	if err := fs.Parse(args); err != nil {
		return err
	}
	return workspace.RunParent(workspace.ParentOptions{
		Shell:     *shell,
		Workspace: *workspaceDir,
		Home:      *home,
		CACert:    *caCert,
		ProxyAddr: *proxyAddr,
	})
}

func WorkspaceInner(args []string) error {
	fs := flag.NewFlagSet("workspace-inner", flag.ContinueOnError)
	shell := fs.String("shell", "/bin/sh", "shell to execute")
	caCert := fs.String("ca-cert", "/harnessh-run/ca.crt", "CA certificate path")
	proxyAddr := fs.String("proxy-addr", netsetup.ProxyAddr(), "egress proxy address")
	workspaceDir := fs.String("workspace", "/workspace", "workspace directory")
	home := fs.String("home", "/home/harnessh", "home directory")
	if err := fs.Parse(args); err != nil {
		return err
	}
	return workspace.RunInner(workspace.InnerOptions{
		Shell:     *shell,
		Workspace: *workspaceDir,
		Home:      *home,
		CACert:    *caCert,
		ProxyAddr: *proxyAddr,
	})
}
