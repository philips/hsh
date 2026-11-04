package cli

import (
	"context"
	"encoding/json"
	"errors"
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

type capturedToken struct {
	Provider  string            `json:"provider"`
	Field     string            `json:"field"`
	Value     string            `json:"value"`
	Created   string            `json:"created"`
	ExpiresAt string            `json:"expires_at"`
	Meta      map[string]string `json:"meta"`
}

type sessionCapture struct {
	Tokens    map[string]string
	Markers   map[string]string
	ExpiresAt time.Time
	Meta      map[string]string
}

func latestCapture(storePath, provider string) (*sessionCapture, error) {
	data, err := state.ReadUntrusted(storePath, state.MaxUntrustedFile)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("no captured tokens in %s (log in inside the session first)", storePath)
		}
		return nil, err
	}
	var m map[string]capturedToken
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parse %s: %w", storePath, err)
	}
	c := &sessionCapture{Tokens: map[string]string{}, Markers: map[string]string{}}
	newest := map[string]time.Time{}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, key := range keys {
		t := m[key]
		if t.Provider != provider {
			continue
		}
		if marker := oauthprov.MarkerPattern.FindString(key); marker != "" {
			c.Markers[marker] = t.Field
		}
		created, _ := time.Parse(time.RFC3339Nano, t.Created)
		if prev, ok := newest[t.Field]; ok && !created.After(prev) {
			continue
		}
		newest[t.Field] = created
		c.Tokens[t.Field] = t.Value
		if t.Field == "access_token" {
			c.ExpiresAt, _ = time.Parse(time.RFC3339, t.ExpiresAt)
			c.Meta = t.Meta
		}
	}
	if c.Tokens["access_token"] == "" || c.Tokens["refresh_token"] == "" {
		return nil, fmt.Errorf("%s has no captured %s access and refresh token (log in inside the session first; captured fields: %v)", storePath, provider, fieldNames(c.Tokens))
	}
	if c.ExpiresAt.IsZero() {
		if exp, ok := oauthprov.JWTExpiry(c.Tokens["access_token"]); ok {
			c.ExpiresAt = exp
		}
	}
	return c, nil
}

func capturedProviders(storePath string) ([]string, error) {
	data, err := state.ReadUntrusted(storePath, state.MaxUntrustedFile)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var m map[string]capturedToken
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parse %s: %w", storePath, err)
	}
	set := map[string]bool{}
	for _, t := range m {
		if t.Field == "refresh_token" {
			set[t.Provider] = true
		}
	}
	out := make([]string, 0, len(set))
	for p := range set {
		out = append(out, p)
	}
	sort.Strings(out)
	return out, nil
}

func choosePromoteProvider(requested string, captured []string, session string) (string, error) {
	if len(captured) == 0 {
		return "", fmt.Errorf("session %s has no captured login (log in inside it first, e.g. claude auth login or codex login --device-auth)", session)
	}
	if requested == "" {
		if len(captured) == 1 {
			return captured[0], nil
		}
		return "", fmt.Errorf("session %s captured logins for %s; name one: hsh oauth promote PROVIDER", session, strings.Join(captured, ", "))
	}
	for _, c := range captured {
		if c == requested {
			return requested, nil
		}
	}
	return "", fmt.Errorf("session %s has no captured %s login; it captured %s (try: hsh oauth promote %s)", session, requested, strings.Join(captured, ", "), captured[0])
}

func fieldNames(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func oauthPromoteCommand() *ff.Command {
	var session, secretDB string
	var force bool
	return newCommand("promote", "hsh oauth promote [FLAGS] [PROVIDER [NAME]]",
		"move a login captured in a session into the secret store, refreshed by the host and usable by every session", func(fs *ff.FlagSet) {
			fs.StringVar(&session, 0, "session", "", "the session that holds the login (default: the current workspace's only session)")
			fs.StringVar(&secretDB, 0, "secret-db", secretstore.DefaultPath(), "secret store")
			fs.BoolVar(&force, 0, "force", "replace an existing login with this name")
		}, func(ctx context.Context, args []string) error {
			if len(args) > 2 {
				return fmt.Errorf("oauth promote takes [PROVIDER [NAME]]")
			}
			provider, name := "", ""
			if len(args) >= 1 {
				provider = args[0]
			}
			if len(args) == 2 {
				name = args[1]
			}
			sessionID, err := resolveSession(session, "hsh oauth promote --session <id> [PROVIDER [NAME]]")
			if err != nil {
				return err
			}
			store, _, err := secretstore.Open(secretDB)
			if err != nil {
				return err
			}
			defer store.Close()
			out, err := promoteLogin(ctx, promoteOptions{
				sessionDir: state.SessionDir(sessionID),
				provider:   provider,
				name:       name,
				force:      force,
				store:      store,
				client:     &http.Client{Timeout: 30 * time.Second},
				settle:     2500 * time.Millisecond,
			})
			if err != nil {
				return err
			}
			fmt.Print(out)
			return nil
		})
}

type promoteOptions struct {
	sessionDir string
	provider   string
	name       string
	force      bool
	store      *secretstore.DB
	client     *http.Client
	settle     time.Duration
	afterBind  func()
}

func promoteLogin(ctx context.Context, o promoteOptions) (string, error) {
	storePath := filepath.Join(o.sessionDir, "proxy", "oauth-tokens.json")
	captured, err := capturedProviders(storePath)
	if err != nil {
		return "", err
	}
	if o.provider, err = choosePromoteProvider(o.provider, captured, filepath.Base(o.sessionDir)); err != nil {
		return "", err
	}
	if o.name == "" {
		o.name = o.provider + "-login"
	}
	l, err := oauthprov.Load(oauthProvidersDir(), o.provider)
	if err != nil {
		return "", err
	}
	if l.Descriptor.Refresh == nil {
		return "", fmt.Errorf("provider %s has no refresh section, so the host cannot keep the login alive; add one with: hsh oauth provider edit %s", o.provider, o.provider)
	}
	capture, err := latestCapture(storePath, o.provider)
	if err != nil {
		return "", err
	}
	if !o.force {
		if _, err := o.store.GetOAuth(o.name); err == nil {
			return "", fmt.Errorf("an OAuth login named %s already exists (use --force to replace it)", o.name)
		}
	}
	expires := capture.ExpiresAt
	if expires.IsZero() {
		expires = time.Now()
	}
	login := config.OAuthSecret{Name: o.name, Provider: o.provider, Tokens: capture.Tokens, Meta: capture.Meta, ExpiresAt: expires}
	if err := o.store.PutOAuth(login, true); err != nil {
		return "", err
	}
	sp, err := loadSessionPhantoms(o.sessionDir)
	if err != nil {
		return "", err
	}
	for marker, field := range capture.Markers {
		sp.Bound[marker] = phantomSlot{Secret: o.name, Field: field}
	}
	if err := sp.save(o.sessionDir); err != nil {
		return "", err
	}
	active, err := pushSessionSecrets(o.sessionDir, o.store)
	if err != nil {
		return "", err
	}
	if active && o.settle > 0 {
		time.Sleep(o.settle)
	}
	if o.afterBind != nil {
		o.afterBind()
	}
	always := func(config.OAuthSecret) bool { return true }
	refreshed, _, err := o.store.RefreshOAuth(ctx, o.name, always, refreshLogin(ctx, o.client, l.Descriptor))
	var re *oauthprov.RefreshError
	if errors.As(err, &re) && re.Permanent {
		newer, cerr := latestCapture(storePath, o.provider)
		if cerr == nil && newer.Tokens["refresh_token"] != capture.Tokens["refresh_token"] {
			login.Tokens, login.Meta, login.Failure = newer.Tokens, newer.Meta, ""
			if err := o.store.PutOAuth(login, true); err != nil {
				return "", err
			}
			refreshed, _, err = o.store.RefreshOAuth(ctx, o.name, always, refreshLogin(ctx, o.client, l.Descriptor))
		}
	}
	if _, perr := pushSessionSecrets(o.sessionDir, o.store); perr != nil && err == nil {
		err = perr
	}
	if err != nil {
		return "", fmt.Errorf("stored the login as %s, but the host could not refresh it: %w", o.name, err)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "promoted the %s login from session %s to hsh://%s (refreshed by the host; expires %s)\n", o.provider, filepath.Base(o.sessionDir), o.name, refreshed.ExpiresAt.UTC().Format(time.RFC3339))
	fmt.Fprintf(&b, "the session's phantoms now resolve through the stored login; its captured refresh token is dropped\n")
	seeds := make([]string, 0, len(l.Descriptor.Seeds))
	for s := range l.Descriptor.Seeds {
		seeds = append(seeds, s)
	}
	sort.Strings(seeds)
	seedJSON, _ := json.Marshal(seeds)
	fmt.Fprintf(&b, "use it in other sessions with a secrets entry (seeds write phantom credential files for %v):\n", seeds)
	fmt.Fprintf(&b, "  \"secrets\": {\"%s\": {\"from\": \"hsh://%s\", \"seed\": %s}}\n", strings.ToUpper(strings.NewReplacer("-", "_", ".", "_").Replace(o.name)), o.name, seedJSON)
	return b.String(), nil
}

func pushSessionSecrets(sessionDir string, store *secretstore.DB) (bool, error) {
	secretsPath := filepath.Join(sessionDir, "secrets.json")
	if !fileExists(secretsPath) {
		return false, nil
	}
	cfg, err := config.Load(filepath.Join(sessionDir, "config.json"))
	if err != nil {
		return true, err
	}
	res, err := resolveSessionSecrets(sessionDir, cfg, store, time.Now())
	if err != nil {
		return true, err
	}
	return true, writeSecretsIfChanged(secretsPath, res.Secrets)
}
