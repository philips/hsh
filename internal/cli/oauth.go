package cli

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/peterbourgon/ff/v4"

	"hsh/internal/netsetup"
)

func oauthCommand() *ff.Command {
	var cmd *ff.Command
	cmd = &ff.Command{
		Name:      "oauth",
		Usage:     "hsh oauth SUBCOMMAND",
		ShortHelp: "help browser-redirect OAuth logins reach the sandbox",
		Flags:     ff.NewFlagSet("oauth"),
		Subcommands: []*ff.Command{
			oauthCallbackCommand(),
		},
	}
	cmd.Exec = groupExec(&cmd)
	return cmd
}

func oauthCallbackCommand() *ff.Command {
	var session string
	return newCommand("callback", "hsh oauth callback [FLAGS] URL",
		"replay a loopback OAuth callback URL into the session that is waiting for it", func(fs *ff.FlagSet) {
			fs.StringVar(&session, 0, "session", "", "the session to deliver to (default: the current workspace's only session)")
		}, func(ctx context.Context, args []string) error {
			if len(args) != 1 {
				return fmt.Errorf("oauth callback takes exactly one URL")
			}
			u, err := url.Parse(args[0])
			if err != nil {
				return fmt.Errorf("parse %q: %w", args[0], err)
			}
			if u.Scheme != "http" || !isLoopbackHost(u.Hostname()) {
				return fmt.Errorf("%q is not a loopback callback (want http://127.0.0.1:PORT/...)", args[0])
			}
			port := u.Port()
			if port == "" {
				port = "80"
			}
			sessionID, err := resolveCallbackSession(session)
			if err != nil {
				return err
			}
			wsName := "hsh-" + sessionID + "-ws"
			if !containerExists(wsName) {
				return fmt.Errorf("session %s has no running workspace container", sessionID)
			}
			request := fmt.Sprintf("GET %s HTTP/1.1\r\nHost: %s\r\nConnection: close\r\n\r\n", u.RequestURI(), u.Host)
			out, err := podman("exec", wsName, "/hsh-workspace-inner",
				"--oauth-deliver", netsetup.CallbackRelayAddr(),
				"--oauth-deliver", port,
				"--oauth-deliver", request)
			if err != nil {
				return fmt.Errorf("delivering the callback to session %s: %w\n%s", sessionID, err, out)
			}
			status := strings.TrimSpace(strings.SplitN(strings.TrimSpace(out), "\n", 2)[0])
			if strings.HasPrefix(status, "ERR") {
				return fmt.Errorf("session %s: %s (is the login still waiting on port %s?)", sessionID, status, port)
			}
			fmt.Printf("delivered the callback to session %s: %s\n", sessionID, status)
			return nil
		})
}

func isLoopbackHost(host string) bool {
	return host == "127.0.0.1" || host == "localhost" || host == "::1" || host == "[::1]"
}

func resolveCallbackSession(explicit string) (string, error) {
	if explicit != "" {
		id := strings.TrimPrefix(explicit, "hsh-")
		if !podExists("hsh-" + id) {
			return "", fmt.Errorf("session %s does not exist", id)
		}
		return id, nil
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	pods, err := listSandboxes("label=hsh.workspace=" + cwd)
	if err != nil {
		return "", err
	}
	switch len(pods) {
	case 0:
		return "", fmt.Errorf("no session for workspace %s; start one with hsh run (or pass --session)", filepath.Base(cwd))
	case 1:
		return strings.TrimPrefix(pods[0].Name, "hsh-"), nil
	default:
		var ids []string
		for _, p := range pods {
			ids = append(ids, strings.TrimPrefix(p.Name, "hsh-"))
		}
		return "", fmt.Errorf("multiple sessions for workspace %s:\n  %s\npick one with: hsh oauth callback --session <id> URL", cwd, strings.Join(ids, "\n  "))
	}
}
