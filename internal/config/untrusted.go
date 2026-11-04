package config

import (
	"fmt"
	"strings"
)

func ListOf(items []string) string {
	const shown = 4
	if len(items) <= shown {
		return strings.Join(items, ", ")
	}
	return strings.Join(items[:shown], ", ") + fmt.Sprintf(", and %d more", len(items)-shown)
}

func (c *Config) RestrictUntrustedImage() []string {
	var dropped []string
	if len(c.Network.Allow) > 0 {
		dropped = append(dropped, "network.allow ("+ListOf(c.Network.Allow)+")")
		c.Network.Allow = nil
	}
	if mode := c.Network.ModeOrDefault(); mode != "allowlist" {
		dropped = append(dropped, "network.mode "+mode+" (using allowlist)")
		c.Network.Mode = "allowlist"
	}
	if len(c.OAuth.Providers) > 0 {
		dropped = append(dropped, "oauth.providers ("+ListOf(c.OAuth.Providers)+")")
		c.OAuth = OAuth{}
	}
	def := Default().Mounts
	if strings.Join(c.Mounts, "\x00") != strings.Join(def, "\x00") {
		dropped = append(dropped, "mounts ("+ListOf(c.Mounts)+"; using "+strings.Join(def, ", ")+")")
		c.Mounts = def
	}
	return dropped
}
