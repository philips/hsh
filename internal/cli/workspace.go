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
	launchJSON := fs.String("launch-json", "", "JSON array argv launched from the interactive shell (agent harness)")
	contextJSON := fs.String("context-files-json", "", "JSON array of instruction files that get the network notice block")
	if err := fs.Parse(args); err != nil {
		return err
	}
	var exec []string
	if *execJSON != "" {
		if err := json.Unmarshal([]byte(*execJSON), &exec); err != nil {
			return err
		}
	}
	var launch []string
	if *launchJSON != "" {
		if err := json.Unmarshal([]byte(*launchJSON), &launch); err != nil {
			return err
		}
	}
	var contextFiles []string
	if *contextJSON != "" {
		if err := json.Unmarshal([]byte(*contextJSON), &contextFiles); err != nil {
			return err
		}
	}
	return workspace.RunParent(workspace.ParentOptions{
		Shell:        *shell,
		Exec:         exec,
		Launch:       launch,
		ContextFiles: contextFiles,
		Workspace:    *workspaceDir,
		Home:         *home,
		CACert:       *caCert,
		ProxyAddr:    *proxyAddr,
		InnerExe:     *innerExe,
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
	launchJSON := fs.String("launch-json", "", "JSON array argv launched from the interactive shell (agent harness)")
	contextJSON := fs.String("context-files-json", "", "JSON array of instruction files that get the network notice block")
	callbackRelay := fs.String("callback-relay", "", "internal: run the OAuth callback relay on this address")
	oauthDeliver := []string{}
	fs.Func("oauth-deliver", "internal: relay ADDR, target PORT, and raw HTTP REQUEST", func(v string) error {
		oauthDeliver = append(oauthDeliver, v)
		return nil
	})
	sshStdio := fs.Bool("ssh-stdio", false, "internal: serve SSH port forwards into the session on stdin/stdout")
	httpEcho := fs.String("http-echo", "", "internal: listen once on 127.0.0.1:PORT, print the request line, answer 200")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *callbackRelay != "" {
		return workspace.CallbackRelay(*callbackRelay)
	}
	if len(oauthDeliver) == 3 {
		return workspace.OAuthDeliver(oauthDeliver[0], oauthDeliver[1], oauthDeliver[2])
	}
	if *sshStdio {
		return serveSSHStdio()
	}
	if *httpEcho != "" {
		return workspace.HTTPEcho(*httpEcho)
	}
	var exec []string
	if *execJSON != "" {
		if err := json.Unmarshal([]byte(*execJSON), &exec); err != nil {
			return err
		}
	}
	var launch []string
	if *launchJSON != "" {
		if err := json.Unmarshal([]byte(*launchJSON), &launch); err != nil {
			return err
		}
	}
	var contextFiles []string
	if *contextJSON != "" {
		if err := json.Unmarshal([]byte(*contextJSON), &contextFiles); err != nil {
			return err
		}
	}
	return workspace.RunInner(workspace.InnerOptions{
		Shell:        *shell,
		Exec:         exec,
		Launch:       launch,
		ContextFiles: contextFiles,
		Workspace:    *workspaceDir,
		Home:         *home,
		CACert:       *caCert,
		ProxyAddr:    *proxyAddr,
	})
}
