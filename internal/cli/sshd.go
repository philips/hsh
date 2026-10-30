package cli

import (
	"flag"
	"fmt"
	"os"

	"hsh/internal/sshd"
	"hsh/internal/workspace"
)

const sshdPath = "/hsh-sshd"

func SSHD(args []string) error {
	fs := flag.NewFlagSet("hsh-sshd", flag.ContinueOnError)
	sessionShell := fs.Bool("session-shell", false, "internal: replace this process with the session's shell (environment and working directory of PID 1)")
	pty := fs.Bool("pty", false, "internal: make fd 0 the controlling terminal of the shell")
	term := fs.String("term", "", "internal: TERM for the shell")
	command := fs.String("command", "", "internal: command to run with the session's shell instead of an interactive shell")
	deliver := fs.String("deliver", "", "deliver the raw HTTP request from --request to 127.0.0.1:PORT in the sandbox and print the response")
	request := fs.String("request", "", "raw HTTP request for --deliver")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected arguments %v", fs.Args())
	}
	if err := workspace.RestrictPrivileges(); err != nil {
		return err
	}
	if *sessionShell {
		return workspace.RunDirectSSHSession(workspace.SSHSessionOptions{Pty: *pty, Term: *term, Command: *command})
	}
	if *deliver != "" {
		return workspace.DeliverLocal(*deliver, *request)
	}
	self, err := os.Executable()
	if err != nil {
		return err
	}
	return sshd.Serve(sshd.Stdio(), sshd.Config{
		Dial: workspace.DialLoopbackPort,
		List: func() ([]sshd.Listener, error) {
			ls, err := workspace.InnerListeners()
			if err != nil {
				return nil, err
			}
			out := make([]sshd.Listener, len(ls))
			for i, l := range ls {
				out[i] = sshd.Listener{Port: l.Port, Addr: l.Addr}
			}
			return out, nil
		},
		Spawn: func(r sshd.SpawnRequest) (sshd.SpawnResult, error) {
			return spawnSession(self, []string{"--session-shell"}, r)
		},
	})
}
