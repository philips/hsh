package cli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/peterbourgon/ff/v4"
)

var sshProxyCmd *ff.Command

func sshCommand() *ff.Command {
	var cmd *ff.Command
	sshProxyCmd = sshProxyCommand()
	cmd = &ff.Command{
		Name:      "ssh",
		Usage:     "hsh ssh SUBCOMMAND",
		ShortHelp: "reach servers inside a session with your ssh client",
		Flags:     ff.NewFlagSet("ssh"),
		Subcommands: []*ff.Command{
			sshConfigCommand(),
			sshProxyCmd,
		},
	}
	cmd.Exec = groupExec(&cmd)
	return cmd
}

func sshProxyCommand() *ff.Command {
	var session string
	return newCommand("proxy", "hsh ssh proxy [FLAGS] [hsh-SESSION]",
		"speak SSH on stdin/stdout for port forwards and shells into a session (use as an ssh ProxyCommand)", func(fs *ff.FlagSet) {
			fs.StringVar(&session, 0, "session", "", "the session to connect to (default: the host name argument, else the current workspace's only session)")
		}, func(ctx context.Context, args []string) error {
			if len(args) > 1 {
				return fmt.Errorf("ssh proxy takes at most one session name")
			}
			if isTerminal(os.Stdout) {
				return fmt.Errorf("ssh proxy speaks the SSH protocol on stdin/stdout; use it as a ProxyCommand (see: hsh ssh config)")
			}
			if len(args) == 1 {
				if session != "" {
					return fmt.Errorf("give the session as an argument or with --session, not both")
				}
				session = args[0]
			}
			sessionID, err := resolveSession(session, "hsh ssh proxy --session <id>")
			if err != nil {
				return err
			}
			wsName := "hsh-" + sessionID + "-ws"
			if !containerRunning(wsName) {
				return fmt.Errorf("session %s has no running workspace container; start it with: hsh resume %s", sessionID, sessionID)
			}
			sshArgs, err := sessionSSHArgs(sessionID)
			if err != nil {
				return err
			}
			cmd := exec.CommandContext(ctx, "podman", sshArgs...)
			cmd.Stdin = os.Stdin
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr
			return cmd.Run()
		})
}

func sessionSSHArgs(sessionID string) ([]string, error) {
	wsName := "hsh-" + sessionID + "-ws"
	if err := checkSSHDMount(sessionID); err != nil {
		return nil, err
	}
	return []string{"exec", "-i", wsName, sshdPath}, nil
}

func checkSSHDMount(sessionID string) error {
	out, err := podman("container", "inspect", "--format", `{{range .Mounts}}{{.Destination}}{{"\n"}}{{end}}`, "hsh-"+sessionID+"-ws")
	if err != nil {
		return fmt.Errorf("inspect session %s: %w\n%s", sessionID, err, out)
	}
	if !hasMount(out, sshdPath) {
		return legacySessionError(sessionID)
	}
	return nil
}

func hasMount(inspect, dest string) bool {
	for _, line := range strings.Split(inspect, "\n") {
		if strings.TrimSpace(line) == dest {
			return true
		}
	}
	return false
}

func legacySessionError(sessionID string) error {
	return fmt.Errorf("session %s was created by an older hsh and cannot serve hsh forward, ssh, or oauth callback;\nremove it with: hsh rm %s   then start a new one with: hsh run", sessionID, sessionID)
}

func sshConfigCommand() *ff.Command {
	var host, hshPath string
	return newCommand("config", "hsh ssh config [FLAGS]",
		"print the ~/.ssh/config stanza that makes `ssh hsh-SESSION` reach a session", func(fs *ff.FlagSet) {
			fs.StringVar(&host, 0, "host", "", "ssh destination of the machine that runs hsh (default: hsh runs on this machine)")
			fs.StringVar(&hshPath, 0, "hsh-path", "hsh", "path of the hsh binary on the machine that runs it")
		}, func(ctx context.Context, args []string) error {
			if len(args) > 0 {
				return fmt.Errorf("ssh config takes no arguments")
			}
			fmt.Print(sshConfigStanza(host, hshPath))
			return nil
		})
}

func sshConfigStanza(host, hshPath string) string {
	proxy := hshPath + " ssh proxy %h"
	if host != "" {
		proxy = "ssh -T -q " + host + " " + hshPath + " ssh proxy %h"
	}
	var b strings.Builder
	b.WriteString("Host hsh-*\n")
	fmt.Fprintf(&b, "    ProxyCommand %s\n", proxy)
	b.WriteString("    StrictHostKeyChecking no\n")
	b.WriteString("    UserKnownHostsFile /dev/null\n")
	b.WriteString("    LogLevel ERROR\n")
	b.WriteString("    ForwardAgent no\n")
	b.WriteString("    ForwardX11 no\n")
	return b.String()
}
