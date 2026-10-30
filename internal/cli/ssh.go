package cli

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
	"syscall"

	"github.com/peterbourgon/ff/v4"

	"hsh/internal/netsetup"
	"hsh/internal/sshd"
	"hsh/internal/state"
	"hsh/internal/workspace"
)

func serveSSHStdio() error {
	relayAddr := netsetup.CallbackRelayAddr()
	return sshd.Serve(sshd.Stdio(), sshd.Config{
		Dial: func(port int) (net.Conn, error) {
			return workspace.DialRelay(relayAddr, port)
		},
		List: func() ([]sshd.Listener, error) {
			ls, err := workspace.ListRelay(relayAddr)
			if err != nil {
				return nil, err
			}
			out := make([]sshd.Listener, len(ls))
			for i, l := range ls {
				out[i] = sshd.Listener{Port: l.Port, Addr: l.Addr}
			}
			return out, nil
		},
		Spawn: spawnSSHSession,
	})
}

func spawnSSHSession(r sshd.SpawnRequest) (sshd.SpawnResult, error) {
	self, err := os.Executable()
	if err != nil {
		return sshd.SpawnResult{}, err
	}
	return spawnSession(self, []string{"--ssh-session"}, r)
}

func spawnSession(self string, base []string, r sshd.SpawnRequest) (sshd.SpawnResult, error) {
	args := append([]string(nil), base...)
	if r.Pty {
		args = append(args, "--pty")
	}
	if r.Term != "" {
		args = append(args, "--term", r.Term)
	}
	if r.Command != "" {
		args = append(args, "--command", r.Command)
	}
	cmd := exec.Command(self, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if r.Pty {
		cmd.Stdin = r.Slave
		cmd.Stdout = r.Slave
		cmd.Stderr = r.Slave
	} else {
		cmd.Stdin = r.Stdin
		cmd.Stdout = r.Stdout
		cmd.Stderr = r.Stderr
	}
	if err := cmd.Start(); err != nil {
		return sshd.SpawnResult{}, err
	}
	pgid := cmd.Process.Pid
	return sshd.SpawnResult{
		Signal: func(sig syscall.Signal) error { return syscall.Kill(-pgid, sig) },
		Wait: func() int {
			return sshd.ExitCode(cmd.Wait())
		},
	}, nil
}

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
				return fmt.Errorf("session %s has no running workspace container; start or resume it with hsh run", sessionID)
			}
			cmd := exec.CommandContext(ctx, "podman", sessionSSHArgs(sessionID)...)
			cmd.Stdin = os.Stdin
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr
			return cmd.Run()
		})
}

func sessionSSHArgs(sessionID string) []string {
	wsName := "hsh-" + sessionID + "-ws"
	if readSessionRuntime(state.SessionDir(sessionID)) == runtimeGVisor {
		return []string{"exec", "-i", wsName, sshdPath}
	}
	return []string{"exec", "-i", wsName, "/hsh-workspace-inner", "--ssh-stdio"}
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
	return b.String()
}
