package cli

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"

	"hsh/internal/config"
	"hsh/internal/oauthprov"
	"hsh/internal/secretstore"
	"hsh/internal/workspace"
)

const sessionPhantomsFile = "oauth-phantoms.json"

type phantomSlot struct {
	Secret string `json:"secret"`
	Field  string `json:"field"`
}

type sessionPhantoms struct {
	Primary map[string]map[string]string `json:"primary"`
	Bound   map[string]phantomSlot       `json:"bound"`
}

func loadSessionPhantoms(sessionDir string) (*sessionPhantoms, error) {
	sp := &sessionPhantoms{Primary: map[string]map[string]string{}, Bound: map[string]phantomSlot{}}
	data, err := os.ReadFile(filepath.Join(sessionDir, sessionPhantomsFile))
	if errors.Is(err, fs.ErrNotExist) {
		return sp, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, sp); err != nil {
		return nil, fmt.Errorf("%s: %w", sessionPhantomsFile, err)
	}
	if sp.Primary == nil {
		sp.Primary = map[string]map[string]string{}
	}
	if sp.Bound == nil {
		sp.Bound = map[string]phantomSlot{}
	}
	return sp, nil
}

func (sp *sessionPhantoms) save(sessionDir string) error {
	data, err := json.MarshalIndent(sp, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(sessionDir, sessionPhantomsFile), data, 0o600)
}

func writeFileAtomic(path string, data []byte, mode fs.FileMode) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, mode); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func newMarker() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "hsh_" + hex.EncodeToString(b), nil
}

func (sp *sessionPhantoms) primary(secret, field string) (string, bool, error) {
	if m, ok := sp.Primary[secret][field]; ok {
		return m, false, nil
	}
	m, err := newMarker()
	if err != nil {
		return "", false, err
	}
	if sp.Primary[secret] == nil {
		sp.Primary[secret] = map[string]string{}
	}
	sp.Primary[secret][field] = m
	return m, true, nil
}

func (sp *sessionPhantoms) boundSecrets() []string {
	set := map[string]bool{}
	for _, slot := range sp.Bound {
		set[slot.Secret] = true
	}
	out := make([]string, 0, len(set))
	for s := range set {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

type renderedSeed struct {
	Secret  string
	Path    string
	Mode    uint32
	Content []byte
}

type sessionOAuth struct {
	Secrets   []config.ResolvedOAuthSecret
	Providers []oauthprov.Resolved
	Seeds     []renderedSeed
}

func resolveSessionOAuth(sessionDir string, refs []config.OAuthSecretRef, store config.OAuthSecretStore, providersDir string, now time.Time) (*sessionOAuth, error) {
	sp, err := loadSessionPhantoms(sessionDir)
	if err != nil {
		return nil, err
	}
	byName := map[string]config.OAuthSecretRef{}
	var order []string
	for _, r := range refs {
		if _, ok := byName[r.StoreName]; !ok {
			order = append(order, r.StoreName)
		}
		byName[r.StoreName] = r
	}
	for _, name := range sp.boundSecrets() {
		if _, ok := byName[name]; !ok {
			byName[name] = config.OAuthSecretRef{StoreName: name}
			order = append(order, name)
		}
	}
	out := &sessionOAuth{}
	if len(order) == 0 {
		return out, nil
	}
	if store == nil {
		return nil, fmt.Errorf("the session uses OAuth logins %v but the secret store is not available", order)
	}
	changed := false
	for _, name := range order {
		ref := byName[name]
		s, err := store.GetOAuth(name)
		if errors.Is(err, config.ErrSecretNotFound) {
			fmt.Fprintf(os.Stderr, "hsh: warning: OAuth login %s is bound to this session but no longer in the secret store\n", name)
			continue
		}
		if err != nil {
			return nil, err
		}
		l, err := oauthprov.Load(providersDir, s.Provider)
		if err != nil {
			return nil, fmt.Errorf("OAuth login %s: %w", name, err)
		}
		prov, err := l.Descriptor.Resolve(s.Provider, ref.Hosts)
		if err != nil {
			return nil, fmt.Errorf("OAuth login %s: %w", name, err)
		}
		rs := config.ResolvedOAuthSecret{
			Name:        name,
			Provider:    s.Provider,
			AccessToken: s.Tokens["access_token"],
			ExpiresAt:   s.ExpiresAt.UTC().Format(time.RFC3339),
			RefreshedAt: s.RefreshedAt.UTC().Format(time.RFC3339Nano),
			Failure:     s.Failure,
			Markers:     map[string]string{},
			Presented:   map[string]string{},
			Meta:        s.Meta,
			Hosts:       prov.APIHosts,
		}
		for _, f := range prov.ResponseFields {
			marker, created, err := sp.primary(name, f.Path)
			if err != nil {
				return nil, err
			}
			changed = changed || created
			rs.Markers[marker] = f.Path
			rs.Presented[f.Path] = oauthprov.Present(f, s.Tokens[f.Path], marker)
		}
		for marker, slot := range sp.Bound {
			if slot.Secret == name {
				rs.Markers[marker] = slot.Field
			}
		}
		out.Secrets = append(out.Secrets, rs)
		out.Providers = append(out.Providers, prov)
		for _, seedName := range ref.Seed {
			seed, ok := l.Descriptor.Seeds[seedName]
			if !ok {
				return nil, fmt.Errorf("OAuth login %s: provider %s has no seed %q", name, s.Provider, seedName)
			}
			data := map[string]any{
				"expires_at":        s.ExpiresAt.UTC().Format(time.RFC3339),
				"expires_at_millis": s.ExpiresAt.UnixMilli(),
				"now":               now.UTC().Format(time.RFC3339Nano),
				"meta":              s.Meta,
			}
			for field, v := range rs.Presented {
				data[field] = v
			}
			content, err := oauthprov.RenderSeed(seedName, seed, data)
			if err != nil {
				return nil, fmt.Errorf("OAuth login %s: %w", name, err)
			}
			out.Seeds = append(out.Seeds, renderedSeed{Secret: name, Path: seed.Path, Mode: seed.FileMode(), Content: content})
		}
	}
	if changed {
		if err := sp.save(sessionDir); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func sessionSeedDir(sessionDir string) string {
	return filepath.Join(sessionDir, "seed")
}

func writeSessionSeeds(sessionDir string, seeds []renderedSeed) error {
	dir := sessionSeedDir(sessionDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	var manifest []workspace.SeedEntry
	for i, s := range seeds {
		file := strconv.Itoa(i)
		if err := writeFileAtomic(filepath.Join(dir, file), s.Content, 0o600); err != nil {
			return err
		}
		manifest = append(manifest, workspace.SeedEntry{File: file, Path: s.Path, Mode: s.Mode})
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(dir, workspace.SeedManifest), data, 0o600)
}

func mergeProviders(named, extra []oauthprov.Resolved) []oauthprov.Resolved {
	out := append([]oauthprov.Resolved(nil), named...)
	have := map[string]bool{}
	for _, p := range named {
		have[p.Name] = true
	}
	for _, p := range extra {
		if !have[p.Name] {
			out = append(out, p)
			have[p.Name] = true
		}
	}
	return out
}

type sessionResolution struct {
	Secrets  config.ResolvedSecrets
	Missing  []string
	Warnings []string
}

func resolveSessionSecrets(sessionDir string, cfg *config.Config, store config.SecretStore, now time.Time) (*sessionResolution, error) {
	rs, missing, err := config.ResolveSecrets(cfg, store)
	if err != nil {
		return nil, err
	}
	providers, warnings, err := config.ResolveOAuth(cfg, oauthProvidersDir())
	if err != nil {
		return nil, err
	}
	var oauthStore config.OAuthSecretStore
	if s, ok := store.(config.OAuthSecretStore); ok {
		oauthStore = s
	}
	so, err := resolveSessionOAuth(sessionDir, config.OAuthSecretRefs(cfg, store), oauthStore, oauthProvidersDir(), now)
	if err != nil {
		return nil, err
	}
	rs.OAuth = mergeProviders(providers, so.Providers)
	rs.OAuthSecrets = so.Secrets
	if err := writeSessionSeeds(sessionDir, so.Seeds); err != nil {
		return nil, err
	}
	return &sessionResolution{Secrets: rs, Missing: missing, Warnings: warnings}, nil
}

func configUsesStore(c *config.Config) bool {
	for _, s := range c.Secrets {
		if scheme, _, err := config.ParseSecretRef(s.From); err == nil && scheme == "hsh" {
			return true
		}
	}
	return false
}

type loginLister interface {
	config.SecretStore
	List() ([]secretstore.Entry, error)
}

func unusedLoginHints(cfg *config.Config, store loginLister) []string {
	named := map[string]bool{}
	for _, p := range cfg.OAuth.Providers {
		named[p] = true
	}
	if len(named) == 0 {
		return nil
	}
	used := map[string]bool{}
	for _, r := range config.OAuthSecretRefs(cfg, store) {
		used[r.StoreName] = true
	}
	entries, err := store.List()
	if err != nil {
		return nil
	}
	var hints []string
	for _, e := range entries {
		if e.Kind != "oauth" || !named[e.Provider] || used[e.Name] {
			continue
		}
		seed := ""
		if l, err := oauthprov.Load(oauthProvidersDir(), e.Provider); err == nil {
			names := make([]string, 0, len(l.Descriptor.Seeds))
			for n := range l.Descriptor.Seeds {
				names = append(names, n)
			}
			sort.Strings(names)
			if len(names) > 0 {
				b, _ := json.Marshal(names)
				seed = fmt.Sprintf(", \"seed\": %s", b)
			}
		}
		hints = append(hints, fmt.Sprintf("the OAuth login hsh://%s (%s) is in the secret store, but this session's config does not use it, so the session starts logged out; rebuild the image if its label predates logins, or add to a config: \"secrets\": {\"LOGIN\": {\"from\": \"hsh://%s\"%s}}", e.Name, e.Provider, e.Name, seed))
	}
	return hints
}
