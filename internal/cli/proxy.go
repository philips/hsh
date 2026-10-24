package cli

import (
	"flag"

	"harnessh/internal/netsetup"
	"harnessh/internal/proxy"
)

func Proxy(args []string) error {
	fs := flag.NewFlagSet("proxy", flag.ContinueOnError)
	listen := fs.String("listen", netsetup.HostIP+":"+netsetup.ProxyPort, "listen address")
	configPath := fs.String("config", "/harnessh-run/config.harnessh", "config file path")
	secretsPath := fs.String("secrets", "/harnessh-run/secrets.json", "resolved secrets file path")
	caCert := fs.String("ca-cert", "/harnessh-run/ca.crt", "CA certificate path")
	caKey := fs.String("ca-key", "/harnessh-run/ca.key", "CA private key path")
	roots := fs.String("roots", "", "upstream root CA bundle path")
	if err := fs.Parse(args); err != nil {
		return err
	}
	p, err := proxy.New(proxy.Options{
		ListenAddr:  *listen,
		ConfigPath:  *configPath,
		SecretsPath: *secretsPath,
		CACertPath:  *caCert,
		CAKeyPath:   *caKey,
		RootsPath:   *roots,
	})
	if err != nil {
		return err
	}
	return p.Run()
}
