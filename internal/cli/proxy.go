package cli

import (
	"flag"
	"path/filepath"

	"hsh/internal/netsetup"
	"hsh/internal/proxy"
)

func Proxy(args []string) error {
	fs := flag.NewFlagSet("proxy", flag.ContinueOnError)
	listen := fs.String("listen", netsetup.HostIP+":"+netsetup.ProxyPort, "listen address")
	configPath := fs.String("config", "/hsh-run/config.hsh", "config file path")
	secretsPath := fs.String("secrets", "/hsh-run/secrets.json", "resolved secrets file path")
	caCert := fs.String("ca-cert", "/hsh-run/ca.crt", "CA certificate path")
	caKey := fs.String("ca-key", "/hsh-run/ca.key", "CA private key path")
	roots := fs.String("roots", "", "upstream root CA bundle path")
	deniedDB := fs.String("denied-db", "", "denial database path (empty disables recording)")
	oauthStore := fs.String("oauth-store", "", "oauth token capture store path (default: next to the denial database)")
	networkMode := fs.String("network-mode", "", "network policy override: https, allowlist, or none (default: the config's network.mode)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	storePath := *oauthStore
	if storePath == "" && *deniedDB != "" {
		storePath = filepath.Join(filepath.Dir(*deniedDB), "oauth-tokens.json")
	}
	p, err := proxy.New(proxy.Options{
		ListenAddr:  *listen,
		ConfigPath:  *configPath,
		SecretsPath: *secretsPath,
		CACertPath:  *caCert,
		CAKeyPath:   *caKey,
		RootsPath:   *roots,
		DeniedDB:    *deniedDB,
		OAuthStore:  storePath,
		NetworkMode: *networkMode,
	})
	if err != nil {
		return err
	}
	return p.Run()
}
