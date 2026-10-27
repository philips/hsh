package imageRef

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const DefaultPrefix = "docker://ghcr.io/philips/hsh-"

func IsTransport(ref string) bool {
	return strings.Contains(ref, "://")
}

func IsQualified(ref string) bool {
	name := stripTag(ref)
	first := name
	if i := strings.Index(first, "/"); i >= 0 {
		first = first[:i]
	}
	return strings.Contains(first, ".") || strings.Contains(first, ":") || first == "localhost"
}

func stripTag(ref string) string {
	i := strings.LastIndex(ref, ":")
	if i < 0 {
		return ref
	}
	if strings.Contains(ref[i:], "/") {
		return ref
	}
	return ref[:i]
}

type Shortnames struct {
	Prefix  string
	Aliases map[string]string
}

func DefaultShortnames() *Shortnames {
	return &Shortnames{Prefix: DefaultPrefix, Aliases: map[string]string{}}
}

func ShortnamesPath() string {
	if v := os.Getenv("XDG_CONFIG_HOME"); v != "" {
		return filepath.Join(v, "hsh", "shortnames.conf")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "shortnames.conf"
	}
	return filepath.Join(home, ".config", "hsh", "shortnames.conf")
}

func LoadShortnames(path string) (*Shortnames, error) {
	sn := DefaultShortnames()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return sn, nil
		}
		return nil, err
	}
	for _, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return nil, fmt.Errorf("%s: %q: want key=value", path, line)
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if key == "shortname-prefix" {
			sn.Prefix = value
			continue
		}
		sn.Aliases[key] = value
	}
	return sn, nil
}

func (sn *Shortnames) Resolve(name string) string {
	if v, ok := sn.Aliases[name]; ok {
		return v
	}
	return sn.Prefix + name
}
