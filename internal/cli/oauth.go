package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/peterbourgon/ff/v4"

	"hsh/internal/state"
)

func oauthCommand() *ff.Command {
	var cmd *ff.Command
	cmd = &ff.Command{
		Name:      "oauth",
		Usage:     "hsh oauth SUBCOMMAND",
		ShortHelp: "OAuth logins in the sandbox: callback delivery, captured tokens, provider descriptors",
		Flags:     ff.NewFlagSet("oauth"),
		Subcommands: []*ff.Command{
			oauthCallbackCommand(),
			oauthStatusCommand(),
			oauthProviderCommand(),
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
			sessionID, err := resolveSession(session, "hsh oauth callback --session <id> URL")
			if err != nil {
				return err
			}
			wsName := "hsh-" + sessionID + "-ws"
			if !containerExists(wsName) {
				return fmt.Errorf("session %s has no running workspace container", sessionID)
			}
			request := fmt.Sprintf("GET %s HTTP/1.1\r\nHost: %s\r\nConnection: close\r\n\r\n", u.RequestURI(), u.Host)
			if err := checkSSHDMount(sessionID); err != nil {
				return err
			}
			out, err := podman("exec", wsName, sshdPath, "--deliver", port, "--request", request)
			if err != nil {
				return fmt.Errorf("delivering the callback to session %s: %w\n%s", sessionID, err, out)
			}
			status := stripControl(strings.TrimSpace(strings.SplitN(strings.TrimSpace(out), "\n", 2)[0]))
			if strings.HasPrefix(status, "ERR") {
				return fmt.Errorf("session %s: %s (is the login still waiting on port %s?)", sessionID, status, port)
			}
			if code := u.Query().Get("code"); code != "" {
				fmt.Printf("delivered the callback to session %s: %s (code %s, %d chars)\n", sessionID, status, oauthFingerprint(code), len(code))
			} else {
				fmt.Printf("delivered the callback to session %s: %s\n", sessionID, status)
			}
			return nil
		})
}

func oauthStatusCommand() *ff.Command {
	var store, session string
	return newCommand("status", "hsh oauth status [FLAGS]",
		"show captured OAuth providers and phantom counts per session (never token values)", func(fs *ff.FlagSet) {
			fs.StringVar(&session, 0, "session", "", "only this session (default: every session with captures)")
			fs.StringVar(&store, 0, "store", "", "read this oauth token store file instead")
		}, func(ctx context.Context, args []string) error {
			if store != "" {
				return printOAuthStore(store)
			}
			if session != "" {
				id := strings.TrimPrefix(session, "hsh-")
				return printOAuthStore(sessionOAuthStore(id))
			}
			entries, err := os.ReadDir(state.SessionsDir())
			if err != nil && !os.IsNotExist(err) {
				return err
			}
			found := 0
			for _, e := range entries {
				path := sessionOAuthStore(e.Name())
				if !e.IsDir() || !fileExists(path) {
					continue
				}
				found++
				fmt.Printf("session %s\n", e.Name())
				if err := printOAuthStore(path); err != nil {
					return err
				}
			}
			if found == 0 {
				fmt.Println("no session has captured tokens (no login has completed yet)")
			}
			return nil
		})
}

func sessionOAuthStore(sessionID string) string {
	return filepath.Join(state.SessionDir(sessionID), "proxy", "oauth-tokens.json")
}

func printOAuthStore(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Printf("%s: no captured tokens (no login has completed yet)\n", path)
			return nil
		}
		return err
	}
	var m map[string]storedPhantom
	if err := json.Unmarshal(data, &m); err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}
	type key struct{ provider, field string }
	counts := map[key]int{}
	newest := map[key]string{}
	for _, t := range m {
		k := key{t.Provider, t.Field}
		counts[k]++
		if t.Created > newest[k] {
			newest[k] = t.Created
		}
	}
	if len(counts) == 0 {
		fmt.Printf("%s: no captured tokens\n", path)
		return nil
	}
	fmt.Printf("%s: %d phantom(s)\n", path, len(m))
	keys := make([]key, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].provider != keys[j].provider {
			return keys[i].provider < keys[j].provider
		}
		return keys[i].field < keys[j].field
	})
	last := ""
	for _, k := range keys {
		if k.provider != last {
			fmt.Printf("  %s:\n", k.provider)
			last = k.provider
		}
		fmt.Printf("    %-14s %d  newest captured %s\n", k.field, counts[k], newest[k])
	}
	return nil
}

type storedPhantom struct {
	Provider string `json:"provider"`
	Field    string `json:"field"`
	Created  string `json:"created"`
}

func oauthFingerprint(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])[:12]
}

func stripControl(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) {
			return -1
		}
		return r
	}, s)
}

func isLoopbackHost(host string) bool {
	return host == "127.0.0.1" || host == "localhost" || host == "::1" || host == "[::1]"
}
