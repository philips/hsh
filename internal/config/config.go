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
)

type Secret struct {
	From   string   `json:"from"`
	Hosts  []string `json:"hosts"`
	Header string   `json:"header"`
	Prefix string   `json:"prefix"`
}

type Network struct {
	Mode  string   `json:"mode"`
	Allow []string `json:"allow"`
}

func (n Network) ModeOrDefault() string {
	if n.Mode == "" {
		return "https"
	}
	return n.Mode
}

func (n Network) ValidateMode() error {
	switch n.Mode {
	case "", "https", "allowlist", "none":
		return nil
	default:
		return fmt.Errorf("invalid network mode %q (want https, allowlist, or none)", n.Mode)
	}
}

type OAuthTokenEndpoint struct {
	Host string `json:"host"`
	Path string `json:"path"`
}

type OAuthProvider struct {
	TokenEndpoint      OAuthTokenEndpoint `json:"token_endpoint"`
	ResponseFields     []string           `json:"response_fields"`
	RequestNonceFields []string           `json:"request_nonce_fields"`
	APIHosts           []string           `json:"api_hosts"`
}

type Config struct {
	Image   string                   `json:"image"`
	Shell   string                   `json:"shell"`
	Mounts  []string                 `json:"mounts"`
	Secrets map[string]Secret        `json:"secrets"`
	Network Network                  `json:"network"`
	OAuth   map[string]OAuthProvider `json:"oauth"`
	Agent   Agent                    `json:"agent"`
}

type Agent struct {
	Command []string `json:"command"`
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
		Image:  "docker.io/library/alpine:latest",
		Shell:  "/bin/sh",
		Mounts: []string{".:/workspace"},
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
		if _, _, err := ParseSecretRef(c.Secrets[name].From); err != nil {
			return fmt.Errorf("secret %s: %w", name, err)
		}
	}
	return nil
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	cfg := Default()
	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if err := cfg.Network.ValidateMode(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if err := cfg.validateSecretRefs(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return cfg, nil
}

func FromImageLabel(label string) (*Config, error) {
	cfg := Default()
	if label == "" {
		return cfg, nil
	}
	if err := json.Unmarshal([]byte(label), cfg); err != nil {
		return nil, fmt.Errorf("org.ifup.hsh image label: %w", err)
	}
	if err := cfg.Network.ValidateMode(); err != nil {
		return nil, fmt.Errorf("org.ifup.hsh image label: %w", err)
	}
	if err := cfg.validateSecretRefs(); err != nil {
		return nil, fmt.Errorf("org.ifup.hsh image label: %w", err)
	}
	return cfg, nil
}

type ResolvedSecret struct {
	Name   string   `json:"name"`
	Value  string   `json:"value"`
	Header string   `json:"header"`
	Prefix string   `json:"prefix"`
	Hosts  []string `json:"hosts"`
}

type ResolvedSecrets struct {
	Secrets []ResolvedSecret `json:"secrets"`
}

var ErrSecretNotFound = errors.New("secret not found in the store")

type SecretStore interface {
	Get(name string) (string, error)
}

func ResolveSecrets(cfg *Config, store SecretStore) (ResolvedSecrets, []string, error) {
	var out ResolvedSecrets
	var missing []string
	for name, s := range cfg.Secrets {
		scheme, ref, err := ParseSecretRef(s.From)
		if err != nil {
			return out, nil, err
		}
		var v string
		switch scheme {
		case "env":
			v = os.Getenv(ref)
		case "hsh":
			if store != nil {
				got, err := store.Get(ref)
				if err != nil {
					if errors.Is(err, ErrSecretNotFound) {
						v = ""
						break
					}
					return out, nil, fmt.Errorf("secret store: %w", err)
				}
				v = got
			}
		}
		if v == "" {
			missing = append(missing, s.From)
			continue
		}
		out.Secrets = append(out.Secrets, ResolvedSecret{
			Name:   name,
			Value:  v,
			Header: s.HeaderName(name),
			Prefix: s.Prefix,
			Hosts:  s.Hosts,
		})
	}
	return out, missing, nil
}

func WriteSecretsFile(path string, rs ResolvedSecrets) error {
	data, err := json.Marshal(rs)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func AddAllowedHosts(path string, hosts []string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	netDoc := map[string]json.RawMessage{}
	if raw, ok := doc["network"]; ok {
		if err := json.Unmarshal(raw, &netDoc); err != nil {
			return nil, fmt.Errorf("%s: network: %w", path, err)
		}
	}
	var allow []string
	if raw, ok := netDoc["allow"]; ok {
		if err := json.Unmarshal(raw, &allow); err != nil {
			return nil, fmt.Errorf("%s: network.allow: %w", path, err)
		}
	}
	existing := map[string]bool{}
	for _, h := range allow {
		existing[h] = true
	}
	var added []string
	for _, h := range hosts {
		if !existing[h] {
			allow = append(allow, h)
			existing[h] = true
			added = append(added, h)
		}
	}
	if len(added) == 0 {
		return nil, nil
	}
	allowJSON, err := json.Marshal(allow)
	if err != nil {
		return nil, err
	}
	netDoc["allow"] = allowJSON
	netJSON, err := json.Marshal(netDoc)
	if err != nil {
		return nil, err
	}
	doc["network"] = netJSON
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, err
	}
	out = append(out, '\n')
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, out, 0o644); err != nil {
		return nil, err
	}
	return added, os.Rename(tmp, path)
}

func AddSecret(path, name string, secret Secret) error {
	entry := map[string]any{"from": secret.From}
	if len(secret.Hosts) > 0 {
		entry["hosts"] = secret.Hosts
	}
	if secret.Header != "" {
		entry["header"] = secret.Header
	}
	if secret.Prefix != "" {
		entry["prefix"] = secret.Prefix
	}
	entryJSON, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(data, &doc); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	secDoc := map[string]json.RawMessage{}
	if raw, ok := doc["secrets"]; ok {
		if err := json.Unmarshal(raw, &secDoc); err != nil {
			return fmt.Errorf("%s: secrets: %w", path, err)
		}
	}
	secDoc[name] = entryJSON
	secJSON, err := json.Marshal(secDoc)
	if err != nil {
		return err
	}
	doc["secrets"] = secJSON
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

func FindSecretRefs(path, ref string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var doc struct {
		Secrets map[string]Secret `json:"secrets"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	var out []string
	for name, s := range doc.Secrets {
		if s.From == ref {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out, nil
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
