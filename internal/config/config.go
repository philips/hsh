package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"hsh/internal/oauthprov"
)

type Secret struct {
	From     string   `json:"from"`
	Hosts    []string `json:"hosts"`
	Header   string   `json:"header"`
	Prefix   string   `json:"prefix"`
	Seed     []string `json:"seed,omitempty"`
	Optional bool     `json:"optional,omitempty"`
}

type Network struct {
	Mode         string   `json:"mode"`
	Allow        []string `json:"allow"`
	ApprovalWait string   `json:"approval_wait,omitempty"`
	SuspendDelay string   `json:"suspend_delay,omitempty"`
	Notice       *bool    `json:"notice,omitempty"`
}

func (n Network) NoticeEnabled() bool {
	return n.Notice == nil || *n.Notice
}

const (
	DefaultApprovalWait   time.Duration = 30 * time.Second
	SuggestedApprovalWait               = 30 * time.Second
	MaxApprovalWait                     = 5 * time.Minute
	DefaultSuspendDelay                 = 3 * time.Second
	MaxSuspendDelay                     = time.Minute
)

func ParseSuspendDelay(v string) (time.Duration, error) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "":
		return DefaultSuspendDelay, nil
	case "off", "0", "0s":
		return 0, nil
	}
	d, err := time.ParseDuration(strings.TrimSpace(v))
	if err != nil {
		return 0, fmt.Errorf("invalid network.suspend_delay %q (want a duration such as \"1s\", or \"off\" to never suspend)", v)
	}
	if d < 0 {
		return 0, fmt.Errorf("invalid network.suspend_delay %q: negative", v)
	}
	if d > MaxSuspendDelay {
		return 0, fmt.Errorf("invalid network.suspend_delay %q: more than %s", v, MaxSuspendDelay)
	}
	return d, nil
}

func (n Network) SuspendDelayDuration() time.Duration {
	d, err := ParseSuspendDelay(n.SuspendDelay)
	if err != nil {
		return DefaultSuspendDelay
	}
	return d
}

func (n Network) ValidateSuspendDelay() error {
	_, err := ParseSuspendDelay(n.SuspendDelay)
	return err
}

func ParseApprovalWait(v string) (time.Duration, error) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "":
		return DefaultApprovalWait, nil
	case "off", "0", "0s":
		return 0, nil
	}
	d, err := time.ParseDuration(strings.TrimSpace(v))
	if err != nil {
		return 0, fmt.Errorf("invalid network.approval_wait %q (want a duration such as \"30s\", or \"off\")", v)
	}
	if d < 0 {
		return 0, fmt.Errorf("invalid network.approval_wait %q: negative", v)
	}
	if d > MaxApprovalWait {
		return 0, fmt.Errorf("invalid network.approval_wait %q: more than %s", v, MaxApprovalWait)
	}
	return d, nil
}

func (n Network) ApprovalWaitDuration() time.Duration {
	d, err := ParseApprovalWait(n.ApprovalWait)
	if err != nil {
		return DefaultApprovalWait
	}
	return d
}

func (n Network) ModeOrDefault() string {
	if n.Mode == "" {
		return "https"
	}
	return n.Mode
}

func (n Network) HoldSummary() string {
	d := n.ApprovalWaitDuration()
	if d == 0 {
		return "holding: off"
	}
	return "holding: " + d.String()
}

func (n Network) ValidateApprovalWait() error {
	_, err := ParseApprovalWait(n.ApprovalWait)
	return err
}

func (n Network) ValidateAllow() error {
	for _, h := range n.Allow {
		if _, err := CanonicalPattern(h); err != nil {
			return fmt.Errorf("network.allow: %w", err)
		}
	}
	return nil
}

func (n Network) ValidateMode() error {
	switch n.Mode {
	case "", "https", "allowlist", "report", "none":
		return nil
	default:
		return fmt.Errorf("invalid network mode %q (want https, allowlist, report, or none)", n.Mode)
	}
}

type OAuth struct {
	Providers []string            `json:"providers,omitempty"`
	APIHosts  map[string][]string `json:"api_hosts,omitempty"`
}

func (o *OAuth) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("oauth: %w", err)
	}
	var inline []string
	for k := range raw {
		if k != "providers" && k != "api_hosts" {
			inline = append(inline, k)
		}
	}
	if len(inline) > 0 {
		sort.Strings(inline)
		return fmt.Errorf("oauth: unknown field(s) %s; a config names OAuth providers instead of defining them: \"oauth\": {\"providers\": [%q]} (see hsh oauth provider ls; define your own with hsh oauth provider edit NAME --full)", strings.Join(inline, ", "), inline[0])
	}
	type plain OAuth
	var p plain
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		return fmt.Errorf("oauth: %w", err)
	}
	*o = OAuth(p)
	return nil
}

func (o OAuth) validate() error {
	seen := map[string]bool{}
	for _, name := range o.Providers {
		if err := oauthprov.ValidName(name); err != nil {
			return fmt.Errorf("oauth.providers: %w", err)
		}
		if seen[name] {
			return fmt.Errorf("oauth.providers: %q is listed twice", name)
		}
		seen[name] = true
	}
	for name := range o.APIHosts {
		if !seen[name] {
			return fmt.Errorf("oauth.api_hosts: %q is not in oauth.providers", name)
		}
	}
	return nil
}

const (
	Version    = 3
	MinVersion = 1
)

type VersionKind int

const (
	VersionMissing VersionKind = iota
	VersionNewer
	VersionOlder
)

type VersionError struct {
	Source string
	Kind   VersionKind
	Have   int
}

func (e *VersionError) Error() string {
	switch e.Kind {
	case VersionMissing:
		return fmt.Sprintf("%s: no \"version\" field (this hsh reads version %d)", e.Source, Version)
	case VersionNewer:
		return fmt.Sprintf("%s: config version %d is newer than this hsh reads (version %d); update hsh (hsh update)", e.Source, e.Have, Version)
	default:
		return fmt.Sprintf("%s: config version %d is no longer supported (this hsh reads version %d); regenerate it", e.Source, e.Have, Version)
	}
}

func (e *VersionError) Stale() bool { return e.Kind != VersionNewer }

type Config struct {
	Version int               `json:"version"`
	Image   string            `json:"image"`
	Shell   string            `json:"shell"`
	Mounts  []string          `json:"mounts"`
	Secrets map[string]Secret `json:"secrets"`
	Network Network           `json:"network"`
	OAuth   OAuth             `json:"oauth"`
	Agent   Agent             `json:"agent"`

	DetachKeys string  `json:"detach_keys,omitempty"`
	Monitor    Monitor `json:"monitor"`
}

type Monitor struct {
	Enabled    bool   `json:"enabled"`
	StatusLine bool   `json:"status_line"`
	Key        string `json:"key"`
}

const DefaultMonitorKey = "ctrl-space"

type Agent struct {
	Command      []string `json:"command"`
	Launch       []string `json:"launch"`
	ContextFiles []string `json:"context_files,omitempty"`
}

func ParseTasks(data []byte) []string {
	var tasks []string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		tasks = append(tasks, line)
	}
	return tasks
}

func ComposePrompt(tasks []string) string {
	var b strings.Builder
	b.WriteString("Complete the following tasks:\n\n")
	for i, t := range tasks {
		fmt.Fprintf(&b, "%d. %s\n", i+1, t)
	}
	return b.String()
}

type Mount struct {
	Source  string
	Target  string
	Options string
}

func Default() *Config {
	return &Config{
		Version: Version,
		Image:   "docker.io/library/alpine:latest",
		Shell:   "/bin/sh",
		Mounts:  []string{".:/workspace"},

		Monitor: Monitor{Enabled: true, StatusLine: true, Key: DefaultMonitorKey},
	}
}

func (c *Config) ParsedMounts(configDir string) ([]Mount, error) {
	var out []Mount
	for _, m := range c.Mounts {
		parts := strings.Split(m, ":")
		if len(parts) < 2 || len(parts) > 3 {
			return nil, fmt.Errorf("invalid mount %q (want src:dst[:opts])", m)
		}
		src := parts[0]
		if !filepath.IsAbs(src) {
			src = filepath.Join(configDir, src)
		}
		mnt := Mount{Source: src, Target: parts[1]}
		if len(parts) == 3 {
			mnt.Options = parts[2]
		}
		out = append(out, mnt)
	}
	return out, nil
}

func (s Secret) HeaderName(name string) string {
	if s.Header != "" {
		return s.Header
	}
	return strings.ReplaceAll(name, "_", "-")
}

func ParseSecretRef(from string) (scheme, name string, err error) {
	if from == "" {
		return "", "", fmt.Errorf("empty secret reference")
	}
	if before, after, found := strings.Cut(from, "://"); found {
		scheme, name = before, after
		if name == "" {
			return "", "", fmt.Errorf("secret reference %q has no name", from)
		}
		switch scheme {
		case "env", "hsh":
			return scheme, name, nil
		default:
			return "", "", fmt.Errorf("unknown secret reference scheme in %q (want env://VAR or hsh://NAME)", from)
		}
	}
	return "env", from, nil
}

func (c *Config) validateSecretRefs() error {
	names := make([]string, 0, len(c.Secrets))
	for name := range c.Secrets {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		scheme, ref, err := ParseSecretRef(c.Secrets[name].From)
		if err != nil {
			return fmt.Errorf("secret %s: %w", name, err)
		}
		if scheme == "env" {
			return fmt.Errorf("secret %s: %w (%q): they let a config send any variable of your environment to any host; store the value with: hsh secret add %s --host HOST, and reference it as \"hsh://%s\"", name, ErrEnvReference, c.Secrets[name].From, storeNameFor(ref), storeNameFor(ref))
		}
	}
	return nil
}

var ErrEnvReference = errors.New("environment references are not supported")

func storeNameFor(envVar string) string {
	return strings.ReplaceAll(strings.ToLower(envVar), "_", "-")
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return decode(data, path)
}

func peekVersion(data []byte, source string) error {
	var head struct {
		Version json.RawMessage `json:"version"`
	}
	if err := json.Unmarshal(data, &head); err != nil {
		return fmt.Errorf("%s: %w", source, err)
	}
	if len(head.Version) == 0 {
		return &VersionError{Source: source, Kind: VersionMissing}
	}
	var n int
	if err := json.Unmarshal(head.Version, &n); err != nil {
		return fmt.Errorf("%s: \"version\" must be an integer, got %s", source, head.Version)
	}
	switch {
	case n > Version:
		return &VersionError{Source: source, Kind: VersionNewer, Have: n}
	case n < MinVersion:
		return &VersionError{Source: source, Kind: VersionOlder, Have: n}
	}
	return nil
}

func decode(data []byte, source string) (*Config, error) {
	if err := peekVersion(data, source); err != nil {
		return nil, err
	}
	cfg := Default()
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(cfg); err != nil {
		return nil, fmt.Errorf("%s: %w", source, err)
	}
	if err := cfg.Network.ValidateMode(); err != nil {
		return nil, fmt.Errorf("%s: %w", source, err)
	}
	if err := cfg.Network.ValidateSuspendDelay(); err != nil {
		return nil, fmt.Errorf("%s: %w", source, err)
	}
	if err := cfg.Network.ValidateApprovalWait(); err != nil {
		return nil, fmt.Errorf("%s: %w", source, err)
	}
	if err := cfg.Network.ValidateAllow(); err != nil {
		return nil, fmt.Errorf("%s: %w", source, err)
	}
	if err := cfg.validateSecretRefs(); err != nil {
		return nil, fmt.Errorf("%s: %w", source, err)
	}
	if err := cfg.OAuth.validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", source, err)
	}
	return cfg, nil
}

func FromImageLabel(label string) (*Config, error) {
	if label == "" {
		return Default(), nil
	}
	cfg, err := decode([]byte(label), "org.ifup.hsh image label")
	var ve *VersionError
	if errors.As(err, &ve) && ve.Stale() {
		return nil, fmt.Errorf("%w; rebuild the image", err)
	}
	return cfg, err
}

type ResolvedSecret struct {
	Name   string   `json:"name"`
	Value  string   `json:"value"`
	Header string   `json:"header"`
	Prefix string   `json:"prefix"`
	Hosts  []string `json:"hosts"`
}

type ResolvedSecrets struct {
	Secrets      []ResolvedSecret      `json:"secrets"`
	OAuth        []oauthprov.Resolved  `json:"oauth,omitempty"`
	OAuthSecrets []ResolvedOAuthSecret `json:"oauth_secrets,omitempty"`
}

type OAuthSecret struct {
	Name        string
	Provider    string
	Tokens      map[string]string
	Meta        map[string]string
	ExpiresAt   time.Time
	RefreshedAt time.Time
	Failure     string
	CreatedAt   time.Time
}

type OAuthSecretStore interface {
	GetOAuth(name string) (OAuthSecret, error)
}

type ResolvedOAuthSecret struct {
	Name        string            `json:"name"`
	Provider    string            `json:"provider"`
	AccessToken string            `json:"access_token,omitempty"`
	ExpiresAt   string            `json:"expires_at"`
	RefreshedAt string            `json:"refreshed_at"`
	Failure     string            `json:"failure,omitempty"`
	Markers     map[string]string `json:"markers"`
	Presented   map[string]string `json:"presented"`
	Meta        map[string]string `json:"meta,omitempty"`
	Hosts       []string          `json:"hosts"`
}

type OAuthSecretRef struct {
	ConfigName string
	StoreName  string
	Hosts      []string
	Seed       []string
}

func OAuthSecretRefs(cfg *Config, store SecretStore) []OAuthSecretRef {
	oauthStore, ok := store.(OAuthSecretStore)
	if !ok {
		return nil
	}
	names := make([]string, 0, len(cfg.Secrets))
	for name := range cfg.Secrets {
		names = append(names, name)
	}
	sort.Strings(names)
	var out []OAuthSecretRef
	for _, name := range names {
		s := cfg.Secrets[name]
		scheme, ref, err := ParseSecretRef(s.From)
		if err != nil || scheme != "hsh" {
			continue
		}
		if _, err := oauthStore.GetOAuth(ref); err != nil {
			continue
		}
		out = append(out, OAuthSecretRef{ConfigName: name, StoreName: ref, Hosts: s.Hosts, Seed: s.Seed})
	}
	return out
}

func ResolveOAuth(cfg *Config, dir string) ([]oauthprov.Resolved, []string, error) {
	var out []oauthprov.Resolved
	var warnings []string
	for _, name := range cfg.OAuth.Providers {
		l, err := oauthprov.Load(dir, name)
		if err != nil {
			return nil, nil, err
		}
		warnings = append(warnings, l.Warnings...)
		var narrow []string
		if hosts, ok := cfg.OAuth.APIHosts[name]; ok {
			narrow = append([]string{}, hosts...)
		}
		r, err := l.Descriptor.Resolve(name, narrow)
		if err != nil {
			return nil, nil, err
		}
		out = append(out, r)
	}
	return out, warnings, nil
}

var ErrSecretNotFound = errors.New("secret not found in the store")

type SecretStore interface {
	Get(name string) (string, error)
}

type SecretHostStore interface {
	SecretHosts(name string) ([]string, error)
}

type UnapprovedRoute struct {
	Secret string
	Store  string
	Hosts  []string
}

type SecretResolution struct {
	Secrets    ResolvedSecrets
	Missing    []string
	Unapproved []UnapprovedRoute
}

func approvedHosts(store SecretStore, name string) ([]string, error) {
	hs, ok := store.(SecretHostStore)
	if !ok {
		return nil, nil
	}
	return hs.SecretHosts(name)
}

func splitApproved(hosts, approved []string) (kept, dropped []string) {
	for _, h := range hosts {
		if HostCovered(approved, h) {
			kept = append(kept, h)
		} else {
			dropped = append(dropped, h)
		}
	}
	return kept, dropped
}

func sortedSecretNames(cfg *Config) []string {
	names := make([]string, 0, len(cfg.Secrets))
	for name := range cfg.Secrets {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func ResolveSecrets(cfg *Config, store SecretStore) (SecretResolution, error) {
	var out SecretResolution
	for _, name := range sortedSecretNames(cfg) {
		s := cfg.Secrets[name]
		scheme, ref, err := ParseSecretRef(s.From)
		if err != nil {
			return out, err
		}
		if scheme != "hsh" {
			return out, fmt.Errorf("secret %s: only hsh:// references are supported, got %q", name, s.From)
		}
		if oauthStore, ok := store.(OAuthSecretStore); ok {
			if _, err := oauthStore.GetOAuth(ref); err == nil {
				continue
			}
		}
		var v string
		if store != nil {
			got, err := store.Get(ref)
			if err != nil && !errors.Is(err, ErrSecretNotFound) {
				return out, fmt.Errorf("secret store: %w", err)
			}
			v = got
		}
		if v == "" {
			if !s.Optional {
				out.Missing = append(out.Missing, s.From)
			}
			continue
		}
		approved, err := approvedHosts(store, ref)
		if err != nil {
			return out, fmt.Errorf("secret store: %w", err)
		}
		kept, dropped := splitApproved(s.Hosts, approved)
		if len(dropped) > 0 {
			out.Unapproved = append(out.Unapproved, UnapprovedRoute{Secret: name, Store: ref, Hosts: dropped})
		}
		if len(kept) == 0 {
			continue
		}
		out.Secrets.Secrets = append(out.Secrets.Secrets, ResolvedSecret{
			Name:   name,
			Value:  v,
			Header: s.HeaderName(name),
			Prefix: s.Prefix,
			Hosts:  kept,
		})
	}
	return out, nil
}

func UnapprovedRoutes(cfg *Config, store SecretStore) ([]UnapprovedRoute, error) {
	var out []UnapprovedRoute
	if store == nil {
		return nil, nil
	}
	for _, name := range sortedSecretNames(cfg) {
		s := cfg.Secrets[name]
		scheme, ref, err := ParseSecretRef(s.From)
		if err != nil || scheme != "hsh" {
			continue
		}
		if oauthStore, ok := store.(OAuthSecretStore); ok {
			if _, err := oauthStore.GetOAuth(ref); err == nil {
				continue
			}
		}
		if _, err := store.Get(ref); err != nil {
			continue
		}
		approved, err := approvedHosts(store, ref)
		if err != nil {
			return nil, fmt.Errorf("secret store: %w", err)
		}
		if _, dropped := splitApproved(s.Hosts, approved); len(dropped) > 0 {
			out = append(out, UnapprovedRoute{Secret: name, Store: ref, Hosts: dropped})
		}
	}
	return out, nil
}

func WriteSecretsFile(path string, rs ResolvedSecrets) error {
	data, err := json.Marshal(rs)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".secrets-*.json")
	if err != nil {
		return err
	}
	tmp := f.Name()
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

func setNetworkString(path, key, value string, parse func(string) (time.Duration, error)) error {
	if value != "" {
		if _, err := parse(value); err != nil {
			return err
		}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := peekVersion(data, path); err != nil {
		return err
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(data, &doc); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	netDoc := map[string]json.RawMessage{}
	if raw, ok := doc["network"]; ok {
		if err := json.Unmarshal(raw, &netDoc); err != nil {
			return fmt.Errorf("%s: network: %w", path, err)
		}
	}
	if value == "" {
		delete(netDoc, key)
	} else {
		v, err := json.Marshal(value)
		if err != nil {
			return err
		}
		netDoc[key] = v
	}
	netJSON, err := json.Marshal(netDoc)
	if err != nil {
		return err
	}
	doc["network"] = netJSON
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	out = append(out, '\n')
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, out, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func SetApprovalWait(path, value string) error {
	return setNetworkString(path, "approval_wait", value, ParseApprovalWait)
}

func SetSuspendDelay(path, value string) error {
	return setNetworkString(path, "suspend_delay", value, ParseSuspendDelay)
}

func WatchFile(path string, interval time.Duration, onChange func(data []byte)) {
	var last []byte
	if data, err := os.ReadFile(path); err == nil {
		last = data
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for range t.C {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		if last != nil && bytes.Equal(last, data) {
			continue
		}
		last = data
		onChange(data)
	}
}
