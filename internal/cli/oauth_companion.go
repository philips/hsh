package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"time"

	"hsh/internal/config"
	"hsh/internal/oauthprov"
	"hsh/internal/secretstore"
)

const (
	defaultRefreshMargin  = 15 * time.Minute
	defaultCompanionTick  = 20 * time.Second
	forcedRefreshInterval = 2 * time.Minute
)

func envDuration(name string, def time.Duration) time.Duration {
	if v := os.Getenv(name); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return def
}

func refreshLogin(ctx context.Context, client *http.Client, desc oauthprov.Descriptor) func(config.OAuthSecret) (config.OAuthSecret, error) {
	return func(cur config.OAuthSecret) (config.OAuthSecret, error) {
		now := time.Now()
		res, err := desc.RefreshTokens(ctx, client, cur.Tokens["refresh_token"], now)
		if err != nil {
			var re *oauthprov.RefreshError
			if errors.As(err, &re) && re.Permanent {
				next := cur
				next.Failure = re.Error()
				next.RefreshedAt = now
				return next, err
			}
			return config.OAuthSecret{}, err
		}
		next := cur
		next.Tokens = map[string]string{}
		for k, v := range cur.Tokens {
			next.Tokens[k] = v
		}
		for k, v := range res.Tokens {
			next.Tokens[k] = v
		}
		next.Meta = map[string]string{}
		for k, v := range cur.Meta {
			next.Meta[k] = v
		}
		for k, v := range res.Meta {
			next.Meta[k] = v
		}
		next.ExpiresAt = res.ExpiresAt
		if next.ExpiresAt.IsZero() {
			next.ExpiresAt = now.Add(time.Hour)
		}
		next.RefreshedAt = now
		next.Failure = ""
		return next, nil
	}
}

func refreshWantedPath(sessionDir, secret string) string {
	return filepath.Join(sessionDir, "proxy", "refresh-wanted", url.PathEscape(secret))
}

type oauthCompanion struct {
	sessionDir    string
	sessionConfig string
	secretDB      string
	client        *http.Client
	margin        time.Duration
	logf          func(format string, args ...any)
}

func newOAuthCompanion(sessionDir, sessionConfig, secretDB string) *oauthCompanion {
	c := &oauthCompanion{
		sessionDir:    sessionDir,
		sessionConfig: sessionConfig,
		secretDB:      secretDB,
		client:        &http.Client{Timeout: 30 * time.Second},
		margin:        envDuration("HSH_OAUTH_REFRESH_MARGIN", defaultRefreshMargin),
	}
	c.logf = func(format string, args ...any) {
		f, err := os.OpenFile(filepath.Join(sessionDir, "oauth-companion.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			return
		}
		defer f.Close()
		fmt.Fprintf(f, "%s %s\n", time.Now().UTC().Format(time.RFC3339), fmt.Sprintf(format, args...))
	}
	return c
}

func (c *oauthCompanion) run(ctx context.Context) {
	tick := envDuration("HSH_OAUTH_COMPANION_TICK", defaultCompanionTick)
	for {
		if err := c.tick(ctx); err != nil {
			c.logf("%v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(tick):
		}
	}
}

func (c *oauthCompanion) tick(ctx context.Context) error {
	secretsPath := filepath.Join(c.sessionDir, "secrets.json")
	if !fileExists(secretsPath) {
		return nil
	}
	cfg, err := config.Load(c.sessionConfig)
	if err != nil {
		return err
	}
	sp, err := loadSessionPhantoms(c.sessionDir)
	if err != nil {
		return err
	}
	if !configUsesStore(cfg) && len(sp.Bound) == 0 {
		return nil
	}
	store, _, err := secretstore.Open(c.secretDB)
	if err != nil {
		return err
	}
	defer store.Close()
	names := map[string]bool{}
	for _, r := range config.OAuthSecretRefs(cfg, store) {
		names[r.StoreName] = true
	}
	for _, n := range sp.boundSecrets() {
		names[n] = true
	}
	sorted := make([]string, 0, len(names))
	for n := range names {
		sorted = append(sorted, n)
	}
	sort.Strings(sorted)
	for _, name := range sorted {
		c.refreshIfNeeded(ctx, store, name)
	}
	if !fileExists(secretsPath) {
		return nil
	}
	res, err := resolveSessionSecrets(c.sessionDir, cfg, store, time.Now())
	if err != nil {
		return err
	}
	return writeSecretsIfChanged(secretsPath, res.Secrets)
}

func (c *oauthCompanion) refreshIfNeeded(ctx context.Context, store *secretstore.DB, name string) {
	s, err := store.GetOAuth(name)
	if err != nil {
		return
	}
	l, err := oauthprov.Load(oauthProvidersDir(), s.Provider)
	if err != nil {
		c.logf("login %s: %v", name, err)
		return
	}
	marker := refreshWantedPath(c.sessionDir, name)
	wanted := fileExists(marker)
	needed := func(cur config.OAuthSecret) bool {
		if cur.Failure != "" {
			return false
		}
		if time.Until(cur.ExpiresAt) < c.margin {
			return true
		}
		return wanted && time.Since(cur.RefreshedAt) > forcedRefreshInterval
	}
	next, changed, err := store.RefreshOAuth(ctx, name, needed, refreshLogin(ctx, c.client, l.Descriptor))
	switch {
	case err != nil && !changed:
		c.logf("login %s: refresh failed, will retry: %v", name, err)
		return
	case err != nil:
		c.logf("login %s: refresh refused, the login needs a new promote: %v", name, err)
	case changed:
		c.logf("login %s: refreshed (expires %s)", name, next.ExpiresAt.UTC().Format(time.RFC3339))
	}
	if wanted {
		_ = os.Remove(marker)
	}
}

func sortedResolved(rs config.ResolvedSecrets) config.ResolvedSecrets {
	sort.Slice(rs.Secrets, func(i, j int) bool { return rs.Secrets[i].Name < rs.Secrets[j].Name })
	return rs
}

func writeSecretsIfChanged(path string, rs config.ResolvedSecrets) error {
	rs = sortedResolved(rs)
	data, err := json.Marshal(rs)
	if err != nil {
		return err
	}
	if cur, err := os.ReadFile(path); err == nil && bytes.Equal(cur, data) {
		return nil
	}
	return config.WriteSecretsFile(path, rs)
}
