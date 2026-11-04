package cli

import (
	"bufio"
	"fmt"
	"io"
	"strings"

	"hsh/internal/config"
)

type hostAllower interface {
	SecretHosts(name string) ([]string, error)
	AllowHosts(name string, hosts ...string) ([]string, error)
}

func promptSecretHosts(cfg *config.Config, store config.SecretStore, source string, in io.Reader, out io.Writer) error {
	allower, ok := store.(hostAllower)
	if !ok {
		return nil
	}
	routes, err := config.UnapprovedRoutes(cfg, store)
	if err != nil {
		return err
	}
	if len(routes) == 0 {
		return nil
	}
	r := bufio.NewReader(in)
	explained := map[string]bool{}
	for _, route := range routes {
		if current, err := allower.SecretHosts(route.Store); err == nil && len(current) == 0 && !explained[route.Store] {
			explained[route.Store] = true
			fmt.Fprintf(out, "hsh: hsh://%s has no approved hosts yet. A secret is only sent to hosts you approve, whatever a config or image label asks for; manage them with: hsh secret edit %s\n", route.Store, route.Store)
		}
		for _, host := range route.Hosts {
			fmt.Fprintf(out, "hsh: %s asks to send hsh://%s (as %s) to %s. Allow? [y/N] ", source, route.Store, route.Secret, host)
			line, err := r.ReadString('\n')
			answer := strings.ToLower(strings.TrimSpace(line))
			if answer == "y" || answer == "yes" {
				if _, err := allower.AllowHosts(route.Store, host); err != nil {
					return err
				}
				fmt.Fprintf(out, "hsh: hsh://%s may now be sent to %s\n", route.Store, host)
			}
			if err != nil {
				return nil
			}
		}
	}
	return nil
}

func unapprovedWarning(route config.UnapprovedRoute, source string) string {
	hosts := strings.Join(route.Hosts, " ")
	return fmt.Sprintf("secret %s (hsh://%s) is not sent to %s: %s asks for it, but you have not approved hsh://%s for that host; allow it with: hsh secret allow %s %s", route.Secret, route.Store, strings.Join(route.Hosts, ", "), source, route.Store, route.Store, hosts)
}
