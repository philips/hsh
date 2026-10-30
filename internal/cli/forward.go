package cli

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/peterbourgon/ff/v4"
	"golang.org/x/crypto/ssh"

	"hsh/internal/forward"
)

func connectSession(sessionID string) (*ssh.Client, error) {
	wsName := "hsh-" + sessionID + "-ws"
	if !containerRunning(wsName) {
		return nil, fmt.Errorf("session %s has no running workspace container; start it with: hsh resume %s", sessionID, sessionID)
	}
	cmd := exec.Command("podman", sessionSSHArgs(sessionID)...)
	cmd.Stderr = os.Stderr
	return forward.DialCommand(cmd)
}

func sshServerHint() string {
	fields := strings.Fields(os.Getenv("SSH_CONNECTION"))
	if len(fields) == 4 {
		return fields[2]
	}
	return ""
}

func announceForward(bind string, spec forward.Spec, auto bool) {
	fmt.Printf("forwarding %s -> the session's port %d\n", net.JoinHostPort(bind, fmt.Sprint(spec.Local)), spec.Remote)
	if server := sshServerHint(); server != "" {
		fmt.Printf("  from your computer: ssh -N -L %d:localhost:%d %s\n", spec.Local, spec.Local, server)
	}
}

func isLoopbackBind(bind string) bool {
	if bind == "localhost" {
		return true
	}
	ip := net.ParseIP(bind)
	return ip != nil && ip.IsLoopback()
}

func forwardCommand() *ff.Command {
	var session, bind string
	var list bool
	return newCommand("forward", "hsh forward [FLAGS] [PORT | LOCAL:PORT]...",
		"make servers listening inside a session reachable on this machine's loopback", func(fs *ff.FlagSet) {
			fs.StringVar(&session, 0, "session", "", "the session to reach (default: the current workspace's only session)")
			fs.StringVar(&bind, 0, "bind", "127.0.0.1", "local address to listen on")
			fs.BoolVar(&list, 0, "list", "list the ports the session is listening on and exit")
		}, func(ctx context.Context, args []string) error {
			specs, err := forward.ParseSpecs(args)
			if err != nil {
				return err
			}
			if list && len(specs) > 0 {
				return fmt.Errorf("--list takes no ports")
			}
			sessionID, err := resolveSession(session, "hsh forward --session <id>")
			if err != nil {
				return err
			}
			client, err := connectSession(sessionID)
			if err != nil {
				return err
			}
			defer client.Close()
			if list {
				listeners, err := forward.ListPorts(client)
				if err != nil {
					return err
				}
				if len(listeners) == 0 {
					fmt.Printf("session %s is not listening on any port\n", sessionID)
					return nil
				}
				for _, l := range listeners {
					fmt.Printf("%d\t%s\n", l.Port, l.Addr)
				}
				return nil
			}
			if !isLoopbackBind(bind) {
				fmt.Fprintf(os.Stderr, "hsh: warning: listening on %s exposes the session's servers to everything that can reach this machine\n", bind)
			}
			ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
			defer stop()
			auto := len(specs) == 0
			if auto {
				fmt.Printf("forwarding every port session %s listens on, as it starts (Ctrl-C to stop)\n", sessionID)
			}
			return forward.Run(ctx, client, forward.Options{
				Bind:      bind,
				Specs:     specs,
				Auto:      auto,
				OnForward: announceForward,
				OnRelease: func(bind string, spec forward.Spec) {
					fmt.Printf("the session stopped listening on port %d; released %s\n", spec.Remote, net.JoinHostPort(bind, fmt.Sprint(spec.Local)))
				},
				Logf: func(format string, args ...any) { fmt.Fprintf(os.Stderr, format, args...) },
			})
		})
}

func startRunForwards(wsName string, specs []forward.Spec) func() {
	for _, spec := range specs {
		fmt.Fprintf(os.Stderr, "hsh: forwarding 127.0.0.1:%d -> the session's port %d while this terminal is attached (use hsh forward once detached)\n", spec.Local, spec.Remote)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		for i := 0; i < 200 && !containerRunning(wsName); i++ {
			select {
			case <-ctx.Done():
				return
			case <-time.After(300 * time.Millisecond):
			}
		}
		client, err := connectSession(strings.TrimSuffix(strings.TrimPrefix(wsName, "hsh-"), "-ws"))
		if err != nil {
			fmt.Fprintf(os.Stderr, "hsh: forwarding: %v\n", err)
			return
		}
		defer client.Close()
		err = forward.Run(ctx, client, forward.Options{
			Specs: specs,
			Logf:  func(format string, args ...any) { fmt.Fprintf(os.Stderr, format, args...) },
		})
		if err != nil && ctx.Err() == nil {
			fmt.Fprintf(os.Stderr, "hsh: forwarding: %v\n", err)
		}
	}()
	return cancel
}
