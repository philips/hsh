package cli

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"hsh/internal/certs"
	"hsh/internal/config"
	"hsh/internal/denieddb"
	"hsh/internal/netsetup"
)

var verbose = os.Getenv("HSH_VERBOSE") != ""

func infof(format string, args ...any) {
	if verbose {
		fmt.Fprintf(os.Stderr, format, args...)
	}
}

func Run(args []string) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	configPath := fs.String("config", "config.hsh", "path to the config file")
	imageFlag := fs.String("image", "", "workspace image; without a config file the image's org.ifup.hsh label provides the default config")
	workspaceDir := fs.String("workspace", "", "workspace directory inside the container (default: first mount target)")
	deniedDB := fs.String("denied-db", denieddb.DefaultPath(), "path to the proxy denial database")
	verboseFlag := fs.Bool("verbose", false, "print progress information to stderr")
	resumeFlag := fs.Bool("resume", false, "reuse the sandbox for this workspace directory instead of creating a new one")
	freshFlag := fs.Bool("fresh", false, "with --resume: discard the existing sandbox and start a new one")
	tasksPath := fs.String("tasks", "", "newline-delimited task file (or - for stdin); runs the agent headless with the tasks as its prompt")
	networkMode := fs.String("network", "", "network policy: https, allowlist, or none (default: the config's network.mode, else https)")
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		*imageFlag = args[0]
		args = args[1:]
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	switch *networkMode {
	case "", "https", "allowlist", "none":
	default:
		return fmt.Errorf("invalid --network value %q (want https, allowlist, or none)", *networkMode)
	}
	if *verboseFlag {
		verbose = true
	}
	positional := fs.Args()
	if len(positional) > 0 {
		if len(positional) > 1 {
			return fmt.Errorf("unexpected arguments: %v", positional)
		}
		if *imageFlag != "" && positional[0] != *imageFlag {
			return fmt.Errorf("both --image %s and a positional image %s given", *imageFlag, positional[0])
		}
		*imageFlag = positional[0]
	}
	var tasksPrompt string
	if *tasksPath != "" {
		var data []byte
		var err error
		if *tasksPath == "-" {
			data, err = io.ReadAll(os.Stdin)
		} else {
			data, err = os.ReadFile(*tasksPath)
		}
		if err != nil {
			return fmt.Errorf("read tasks: %w", err)
		}
		tasks := config.ParseTasks(data)
		if len(tasks) == 0 {
			return fmt.Errorf("no tasks found in %s", *tasksPath)
		}
		tasksPrompt = config.ComposePrompt(tasks)
		infof("hsh: %d task(s) from %s\n", len(tasks), *tasksPath)
	}
	absConfig, err := filepath.Abs(*configPath)
	if err != nil {
		return err
	}
	cfg, err := config.Load(absConfig)
	cfgIsFile := true
	if err != nil {
		if !os.IsNotExist(err) {
			return err
		}
		if *imageFlag == "" {
			return fmt.Errorf("no config file at %s and no --image given; pass --image (a harness image supplies its default config via the org.ifup.hsh label) or --config", absConfig)
		}
		label, err := imageLabel(*imageFlag)
		if err != nil {
			return err
		}
		cfg, err = config.FromImageLabel(label)
		if err != nil {
			return err
		}
		cfgIsFile = false
		infof("hsh: using config from the org.ifup.hsh label of %s\n", *imageFlag)
	}
	if *imageFlag != "" {
		cfg.Image = *imageFlag
	}
	var agentExec []string
	if tasksPrompt != "" {
		if len(cfg.Agent.Command) == 0 {
			return fmt.Errorf("--tasks needs an agent command; the image must declare one (\"agent\":{\"command\":[...]}) in its org.ifup.hsh label or config")
		}
		agentExec = make([]string, len(cfg.Agent.Command))
		substituted := false
		for i, el := range cfg.Agent.Command {
			if strings.Contains(el, "{prompt}") {
				agentExec[i] = strings.ReplaceAll(el, "{prompt}", tasksPrompt)
				substituted = true
			} else {
				agentExec[i] = el
			}
		}
		if !substituted {
			agentExec = append(agentExec, tasksPrompt)
		}
		infof("hsh: running agent headless: %s\n", strings.Join(agentExec[:min(len(agentExec), 3)], " "))
	}
	configDir := filepath.Dir(absConfig)
	mountBase := configDir
	if !cfgIsFile {
		if wd, err := os.Getwd(); err == nil {
			mountBase = wd
		}
	}
	mounts, err := cfg.ParsedMounts(mountBase)
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
	resume := *resumeFlag
	workspaceSource := mounts[0].Source
	var podName, runDir string
	var cleanupPod bool
	if resume {
		hash := hashPath(workspaceSource)
		podName = "hsh-" + hash
		runDir = filepath.Join(denieddb.StateDir(), "sessions", hash)
	} else {
		podName = "hsh-" + randHex(6)
		tmpDir, err := os.MkdirTemp("", "hsh-")
		if err != nil {
			return err
		}
		defer os.RemoveAll(tmpDir)
		runDir = filepath.Join(tmpDir, "run")
		cleanupPod = true
	}
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		return err
	}
	if !cfgIsFile {
		absConfig = filepath.Join(runDir, "config.json")
		data, err := json.MarshalIndent(cfg, "", "  ")
		if err != nil {
			return err
		}
		if err := os.WriteFile(absConfig, append(data, '\n'), 0o644); err != nil {
			return err
		}
		configDir = runDir
	}
	caCertPath := filepath.Join(runDir, "ca.crt")
	caKeyPath := filepath.Join(runDir, "ca.key")
	secretsPath := filepath.Join(runDir, "secrets.json")
	if _, err := loadOrCreateCA(resume, caCertPath, caKeyPath); err != nil {
		return err
	}
	rs, missing := config.ResolveSecrets(cfg)
	for _, m := range missing {
		fmt.Fprintf(os.Stderr, "hsh: warning: environment variable %s is not set; its secret will not be injected\n", m)
	}
	if err := config.WriteSecretsFile(secretsPath, rs); err != nil {
		return err
	}
	go config.WatchFile(absConfig, time.Second, func([]byte) {
		cfg2, err := config.Load(absConfig)
		if err != nil {
			fmt.Fprintf(os.Stderr, "hsh: config reload: %v\n", err)
			return
		}
		rs2, missing2 := config.ResolveSecrets(cfg2)
		if err := config.WriteSecretsFile(secretsPath, rs2); err != nil {
			fmt.Fprintf(os.Stderr, "hsh: secrets reload: %v\n", err)
			return
		}
		infof("hsh: configuration reloaded (%d secret(s) resolved, %d missing)\n", len(rs2.Secrets), len(missing2))
	})
	proxyName := podName + "-proxy"
	wsName := podName + "-ws"
	cleanup := func() {
		if cleanupPod {
			_, _ = podman("pod", "rm", "-f", "--time", "0", podName)
		}
	}
	if cleanupPod {
		defer cleanup()
	}
	sigCh := make(chan os.Signal, 2)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		cleanup()
		os.Exit(130)
	}()
	roots := findRootBundle()
	stateDir := filepath.Dir(*deniedDB)
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		return err
	}
	proxyArgs := []string{
		"create", "--pod", podName, "--name", proxyName,
		"--security-opt", "label=disable",
		"--entrypoint", "/hsh",
		"-v", exe + ":/hsh:ro",
		"-v", configDir + ":/hsh-config:ro",
		"-v", runDir + ":/hsh-run:ro",
		"-v", stateDir + ":/hsh-state",
	}
	if roots != "" {
		proxyArgs = append(proxyArgs, "-v", roots+":/hsh-run/roots.pem:ro")
	}
	proxyArgs = append(proxyArgs,
		cfg.Image, "proxy",
		"--listen", netsetup.HostIP+":"+netsetup.ProxyPort,
		"--config", "/hsh-config/"+filepath.Base(absConfig),
		"--secrets", "/hsh-run/secrets.json",
		"--ca-cert", "/hsh-run/ca.crt",
		"--ca-key", "/hsh-run/ca.key",
		"--denied-db", "/hsh-state/"+filepath.Base(*deniedDB),
	)
	if roots != "" {
		proxyArgs = append(proxyArgs, "--roots", "/hsh-run/roots.pem")
	}
	if *networkMode != "" {
		proxyArgs = append(proxyArgs, "--network-mode", *networkMode)
	}
	reattach := false
	if resume {
		if podExists(podName) {
			if *freshFlag {
				infof("hsh: discarding existing sandbox %s (--fresh)\n", podName)
				_, _ = podman("pod", "rm", "-f", "--time", "0", podName)
			} else {
				reattach = true
			}
		}
	}
	if reattach {
		infof("hsh: resuming sandbox %s for workspace %s\n", podName, workspaceSource)
		if !containerExists(proxyName) {
			if out, err := podman(proxyArgs...); err != nil {
				return fmt.Errorf("create proxy container: %w\n%s", err, out)
			}
		} else if !containerRunning(proxyName) {
			if out, err := podman("start", proxyName); err != nil {
				return fmt.Errorf("start proxy container: %w\n%s", err, out)
			}
		}
	} else {
		infof("hsh: creating pod %s (image %s)\n", podName, cfg.Image)
		podCreateArgs := []string{"pod", "create", "--name", podName,
			"--label", "hsh.workspace=" + workspaceSource}
		if out, err := podman(podCreateArgs...); err != nil {
			return fmt.Errorf("create pod: %w\n%s", err, out)
		}
		if out, err := podman(proxyArgs...); err != nil {
			return fmt.Errorf("create proxy container: %w\n%s", err, out)
		}
		if out, err := podman("start", proxyName); err != nil {
			return fmt.Errorf("start proxy container: %w\n%s", err, out)
		}
	}
	wsArgs := []string{
		"create", "--pod", podName, "--name", wsName,
		"--security-opt", "label=disable",
		"--cap-add", "NET_ADMIN", "--cap-add", "SYS_ADMIN",
		"--entrypoint", "/hsh",
		"-v", exe + ":/hsh:ro",
		"-v", caCertPath + ":/hsh-run/ca.crt:ro",
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
		"--ca-cert", "/hsh-run/ca.crt",
		"--proxy-addr", netsetup.ProxyAddr(),
		"--workspace", wsDir,
		"--home", "/home/hsh",
	)
	if len(agentExec) > 0 {
		execJSON, err := json.Marshal(agentExec)
		if err != nil {
			return err
		}
		wsArgs = append(wsArgs, "--exec-json", string(execJSON))
	}
	infof("hsh: starting workspace (proxy logs: podman logs %s)\n", proxyName)
	if reattach || resume {
		if reattach && containerRunning(wsName) {
			return fmt.Errorf("sandbox for workspace %s is already running; exit it first or launch without --resume", workspaceSource)
		}
		if !reattach || !containerExists(wsName) {
			if out, err := podman(wsArgs...); err != nil {
				return fmt.Errorf("create workspace container: %w\n%s", err, out)
			}
		}
		code, err := attachContainer(wsName)
		if err != nil {
			return err
		}
		if code != 0 {
			os.Exit(code)
		}
		return nil
	}
	wsArgs[0] = "run"
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

func loadOrCreateCA(reuse bool, certPath, keyPath string) (*certs.CA, error) {
	if reuse {
		certPEM, err1 := os.ReadFile(certPath)
		keyPEM, err2 := os.ReadFile(keyPath)
		if err1 == nil && err2 == nil {
			if ca, err := certs.LoadCA(certPEM, keyPEM); err == nil {
				return ca, nil
			}
		}
	}
	ca, err := certs.NewCA()
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(certPath, ca.CertPEM(), 0o644); err != nil {
		return nil, err
	}
	if err := os.WriteFile(keyPath, ca.KeyPEM(), 0o600); err != nil {
		return nil, err
	}
	return ca, nil
}

func attachContainer(name string) (int, error) {
	cmd := exec.Command("podman", "start", "-ai", name)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return exitErr.ExitCode(), nil
		}
		return 0, err
	}
	out, err := podman("inspect", "--format", "{{.State.ExitCode}}", name)
	if err != nil {
		return 0, nil
	}
	code := 0
	_, _ = fmt.Sscanf(strings.TrimSpace(out), "%d", &code)
	return code, nil
}

func podExists(name string) bool {
	_, err := podman("pod", "exists", name)
	return err == nil
}

func containerExists(name string) bool {
	_, err := podman("container", "exists", name)
	return err == nil
}

func containerRunning(name string) bool {
	out, err := podman("inspect", "--format", "{{.State.Running}}", name)
	if err != nil {
		return false
	}
	return strings.TrimSpace(out) == "true"
}

func hashPath(p string) string {
	sum := sha256.Sum256([]byte(p))
	return hex.EncodeToString(sum[:])[:12]
}

func podman(args ...string) (string, error) {
	cmd := exec.Command("podman", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("podman %s: %w", strings.Join(args, " "), err)
	}
	return string(out), nil
}

func imageLabel(image string) (string, error) {
	out, err := podman("image", "inspect", "--format", `{{index .Labels "org.ifup.hsh"}}`, image)
	if err != nil {
		return "", fmt.Errorf("inspect image %s: %w\n%s", image, err, out)
	}
	return strings.TrimSpace(out), nil
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
