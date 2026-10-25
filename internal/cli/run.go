package cli

import (
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"harnessh/internal/certs"
	"harnessh/internal/config"
	"harnessh/internal/denieddb"
	"harnessh/internal/netsetup"
)

var verbose = os.Getenv("HARNESSH_VERBOSE") != ""

func infof(format string, args ...any) {
	if verbose {
		fmt.Fprintf(os.Stderr, format, args...)
	}
}

func Run(args []string) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	configPath := fs.String("config", "config.harnessh", "path to the config file")
	workspaceDir := fs.String("workspace", "", "workspace directory inside the container (default: first mount target)")
	deniedDB := fs.String("denied-db", denieddb.DefaultPath(), "path to the proxy denial database")
	verboseFlag := fs.Bool("verbose", false, "print progress information to stderr")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *verboseFlag {
		verbose = true
	}
	absConfig, err := filepath.Abs(*configPath)
	if err != nil {
		return err
	}
	cfg, err := config.Load(absConfig)
	if err != nil {
		return err
	}
	configDir := filepath.Dir(absConfig)
	mounts, err := cfg.ParsedMounts(configDir)
	if err != nil {
		return err
	}
	if len(mounts) == 0 {
		mounts = []config.Mount{{Source: configDir, Target: "/workspace"}}
	}
	wsDir := *workspaceDir
	if wsDir == "" {
		wsDir = mounts[0].Target
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	exe, err = filepath.EvalSymlinks(exe)
	if err != nil {
		return err
	}
	if _, err := exec.LookPath("podman"); err != nil {
		return fmt.Errorf("podman is required but was not found in PATH")
	}
	ca, err := certs.NewCA()
	if err != nil {
		return err
	}
	tmpDir, err := os.MkdirTemp("", "harnessh-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmpDir)
	runDir := filepath.Join(tmpDir, "run")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		return err
	}
	caCertPath := filepath.Join(runDir, "ca.crt")
	caKeyPath := filepath.Join(runDir, "ca.key")
	secretsPath := filepath.Join(runDir, "secrets.json")
	if err := os.WriteFile(caCertPath, ca.CertPEM(), 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(caKeyPath, ca.KeyPEM(), 0o600); err != nil {
		return err
	}
	rs, missing := config.ResolveSecrets(cfg)
	for _, m := range missing {
		fmt.Fprintf(os.Stderr, "harnessh: warning: environment variable %s is not set; its secret will not be injected\n", m)
	}
	if err := config.WriteSecretsFile(secretsPath, rs); err != nil {
		return err
	}
	go config.WatchFile(absConfig, time.Second, func([]byte) {
		cfg2, err := config.Load(absConfig)
		if err != nil {
			fmt.Fprintf(os.Stderr, "harnessh: config reload: %v\n", err)
			return
		}
		rs2, missing2 := config.ResolveSecrets(cfg2)
		if err := config.WriteSecretsFile(secretsPath, rs2); err != nil {
			fmt.Fprintf(os.Stderr, "harnessh: secrets reload: %v\n", err)
			return
		}
		infof("harnessh: configuration reloaded (%d secret(s) resolved, %d missing)\n", len(rs2.Secrets), len(missing2))
	})
	podName := "harnessh-" + randHex(6)
	infof("harnessh: creating pod %s (image %s)\n", podName, cfg.Image)
	if out, err := podman("pod", "create", "--name", podName); err != nil {
		return fmt.Errorf("create pod: %w\n%s", err, out)
	}
	cleanup := func() {
		_, _ = podman("pod", "rm", "-f", "--time", "0", podName)
	}
	defer cleanup()
	sigCh := make(chan os.Signal, 2)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		cleanup()
		os.Exit(130)
	}()
	proxyName := podName + "-proxy"
	wsName := podName + "-ws"
	roots := findRootBundle()
	stateDir := filepath.Dir(*deniedDB)
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		return err
	}
	proxyArgs := []string{
		"create", "--pod", podName, "--name", proxyName,
		"--security-opt", "label=disable",
		"--entrypoint", "/harnessh",
		"-v", exe + ":/harnessh:ro",
		"-v", configDir + ":/harnessh-config:ro",
		"-v", runDir + ":/harnessh-run:ro",
		"-v", stateDir + ":/harnessh-state",
	}
	if roots != "" {
		proxyArgs = append(proxyArgs, "-v", roots+":/harnessh-run/roots.pem:ro")
	}
	proxyArgs = append(proxyArgs,
		cfg.Image, "proxy",
		"--listen", netsetup.HostIP+":"+netsetup.ProxyPort,
		"--config", "/harnessh-config/"+filepath.Base(absConfig),
		"--secrets", "/harnessh-run/secrets.json",
		"--ca-cert", "/harnessh-run/ca.crt",
		"--ca-key", "/harnessh-run/ca.key",
		"--denied-db", "/harnessh-state/"+filepath.Base(*deniedDB),
	)
	if roots != "" {
		proxyArgs = append(proxyArgs, "--roots", "/harnessh-run/roots.pem")
	}
	if out, err := podman(proxyArgs...); err != nil {
		return fmt.Errorf("create proxy container: %w\n%s", err, out)
	}
	if out, err := podman("start", proxyName); err != nil {
		return fmt.Errorf("start proxy container: %w\n%s", err, out)
	}
	wsArgs := []string{
		"run", "--pod", podName, "--name", wsName,
		"--security-opt", "label=disable",
		"--cap-add", "NET_ADMIN", "--cap-add", "SYS_ADMIN",
		"--entrypoint", "/harnessh",
		"-v", exe + ":/harnessh:ro",
		"-v", caCertPath + ":/harnessh-run/ca.crt:ro",
	}
	if isTerminal(os.Stdin) {
		wsArgs = append(wsArgs, "-i", "-t")
	} else {
		wsArgs = append(wsArgs, "-i")
	}
	for _, m := range mounts {
		v := m.Source + ":" + m.Target
		if m.Options != "" {
			v += ":" + m.Options
		}
		wsArgs = append(wsArgs, "-v", v)
	}
	wsArgs = append(wsArgs,
		cfg.Image, "workspace",
		"--shell", cfg.Shell,
		"--ca-cert", "/harnessh-run/ca.crt",
		"--proxy-addr", netsetup.ProxyAddr(),
		"--workspace", wsDir,
		"--home", "/home/harnessh",
	)
	infof("harnessh: starting workspace (proxy logs: podman logs %s)\n", proxyName)
	cmd := exec.Command("podman", wsArgs...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	runErr := cmd.Run()
	code := 0
	if runErr != nil {
		if exitErr, ok := runErr.(*exec.ExitError); ok {
			code = exitErr.ExitCode()
		} else {
			cleanup()
			return runErr
		}
	}
	cleanup()
	if code != 0 {
		os.Exit(code)
	}
	return nil
}

func podman(args ...string) (string, error) {
	cmd := exec.Command("podman", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("podman %s: %w", strings.Join(args, " "), err)
	}
	return string(out), nil
}

func isTerminal(f *os.File) bool {
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

func findRootBundle() string {
	if v := os.Getenv("SSL_CERT_FILE"); v != "" {
		if fileExists(v) {
			return v
		}
	}
	for _, p := range []string{
		"/etc/ssl/certs/ca-certificates.crt",
		"/etc/pki/tls/certs/ca-bundle.crt",
		"/etc/ssl/ca-bundle.pem",
		"/etc/pki/tls/cert.pem",
		"/etc/ssl/cert.pem",
	} {
		if fileExists(p) {
			return p
		}
	}
	return ""
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func randHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}
