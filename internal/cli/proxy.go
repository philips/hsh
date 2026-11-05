package cli

import (
	"flag"

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
	proxyDB := fs.String("proxy-db", "", "this session's denial database, in its own writable directory; read by hsh monitor (empty disables recording)")
	monitorDB := fs.String("monitor-db", "", "this session's rules, written by the host and read here read-only (empty: the config's network.allow is the allowlist)")
	oauthStore := fs.String("oauth-store", "", "oauth token capture store path, in the session's own writable directory (empty keeps captures in memory)")
	networkMode := fs.String("network-mode", "", "network policy override: https, allowlist, or none (default: the config's network.mode)")
	attended := fs.String("attended", "", "marker file whose recent modification time means someone can approve denied requests (enables holding them)")
	netstack := fs.String("netstack-socket", "", "serve a gVisor sandbox's network over this SOCK_SEQPACKET socket (runsc --network-proxy-path) instead of listening for proxy clients")
	approvalWaitDefault := fs.Duration("approval-wait-default", 0, "how long to hold denied requests for approval when the session config does not set network.approval_wait (the global default from hsh settings)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	p, err := proxy.New(proxy.Options{
		ListenAddr:   *listen,
		ConfigPath:   *configPath,
		SecretsPath:  *secretsPath,
		CACertPath:   *caCert,
		CAKeyPath:    *caKey,
		RootsPath:    *roots,
		ProxyDB:      *proxyDB,
		MonitorDB:    *monitorDB,
		OAuthStore:   *oauthStore,
		NetworkMode:  *networkMode,
		AttendedPath: *attended,
		NetstackPath: *netstack,

		ApprovalWaitDefault: *approvalWaitDefault,
	})
	if err != nil {
		return err
	}
	return p.Run()
}
