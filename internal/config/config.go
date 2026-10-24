package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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
	Allow []string `json:"allow"`
}

type Config struct {
	Image   string            `json:"image"`
	Shell   string            `json:"shell"`
	Mounts  []string          `json:"mounts"`
	Secrets map[string]Secret `json:"secrets"`
	Network Network           `json:"network"`
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

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	cfg := Default()
	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return cfg, nil
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

func ResolveSecrets(cfg *Config) (ResolvedSecrets, []string) {
	var out ResolvedSecrets
	var missing []string
	for name, s := range cfg.Secrets {
		v := os.Getenv(s.From)
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
	return out, missing
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

func WatchFile(path string, interval time.Duration, onChange func(data []byte)) {
	var last []byte
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
