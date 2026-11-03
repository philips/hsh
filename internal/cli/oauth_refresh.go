package cli

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/peterbourgon/ff/v4"

	"hsh/internal/config"
	"hsh/internal/oauthprov"
	"hsh/internal/secretstore"
	"hsh/internal/state"
)

func oauthRefreshCommand() *ff.Command {
	var secretDB string
	return newCommand("refresh", "hsh oauth refresh [FLAGS] NAME",
		"refresh a stored OAuth login now on the host and push the new token into the sessions using it", func(fs *ff.FlagSet) {
			fs.StringVar(&secretDB, 0, "secret-db", secretstore.DefaultPath(), "secret store")
		}, func(ctx context.Context, args []string) error {
			if len(args) != 1 {
				return fmt.Errorf("oauth refresh takes exactly one login name (see hsh secret ls)")
			}
			store, _, err := secretstore.Open(secretDB)
			if err != nil {
				return err
			}
			defer store.Close()
			out, err := refreshStoredLogin(ctx, store, args[0], &http.Client{Timeout: 30 * time.Second}, state.SessionsDir())
			fmt.Print(out)
			return err
		})
}

func refreshStoredLogin(ctx context.Context, store *secretstore.DB, name string, client *http.Client, sessionsDir string) (string, error) {
	cur, err := store.GetOAuth(name)
	if err != nil {
		return "", fmt.Errorf("no OAuth login named %s (see hsh secret ls): %w", name, err)
	}
	l, err := oauthprov.Load(oauthProvidersDir(), cur.Provider)
	if err != nil {
		return "", err
	}
	if l.Descriptor.Refresh == nil {
		return "", fmt.Errorf("provider %s has no refresh section; add one with: hsh oauth provider edit %s", cur.Provider, cur.Provider)
	}
	always := func(config.OAuthSecret) bool { return true }
	next, _, refreshErr := store.RefreshOAuth(ctx, name, always, refreshLogin(ctx, client, l.Descriptor))
	var b strings.Builder
	if refreshErr == nil {
		fmt.Fprintf(&b, "refreshed %s (%s); expires %s\n", name, cur.Provider, next.ExpiresAt.UTC().Format(time.RFC3339))
	}
	pushed, pushErr := pushLoginToSessions(store, name, sessionsDir)
	if len(pushed) > 0 {
		fmt.Fprintf(&b, "updated the running session(s) using it: %s\n", strings.Join(pushed, ", "))
	} else if refreshErr == nil {
		fmt.Fprintf(&b, "no running session uses it; sessions pick it up when they start\n")
	}
	if refreshErr != nil {
		after, _ := store.GetOAuth(name)
		if after.Failure != "" {
			return b.String(), fmt.Errorf("the token endpoint refused to refresh %s, which is now marked failed: %w\nlog in again inside a sandbox and run: hsh oauth promote --force %s %s", name, refreshErr, cur.Provider, name)
		}
		return b.String(), fmt.Errorf("refreshing %s failed (the login is unchanged; try again later): %w", name, refreshErr)
	}
	return b.String(), pushErr
}

func pushLoginToSessions(store *secretstore.DB, name, sessionsDir string) ([]string, error) {
	entries, err := os.ReadDir(sessionsDir)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	var pushed []string
	var firstErr error
	for _, e := range entries {
		dir := filepath.Join(sessionsDir, e.Name())
		if !e.IsDir() || !fileExists(filepath.Join(dir, "secrets.json")) {
			continue
		}
		cfg, err := config.Load(filepath.Join(dir, "config.json"))
		if err != nil {
			continue
		}
		uses := false
		for _, r := range config.OAuthSecretRefs(cfg, store) {
			uses = uses || r.StoreName == name
		}
		if sp, err := loadSessionPhantoms(dir); err == nil {
			for _, s := range sp.boundSecrets() {
				uses = uses || s == name
			}
		}
		if !uses {
			continue
		}
		if _, err := pushSessionSecrets(dir, store); err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("session %s: %w", e.Name(), err)
			}
			continue
		}
		pushed = append(pushed, e.Name())
	}
	sort.Strings(pushed)
	return pushed, firstErr
}
