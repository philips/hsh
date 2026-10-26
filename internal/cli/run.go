package cli

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/peterbourgon/ff/v4"

	"hsh/internal/certs"
	"hsh/internal/config"
	"hsh/internal/denieddb"
	"hsh/internal/netsetup"
	"hsh/internal/secretstore"
)

type runOptions struct {
	configPath   string
	workspaceDir string
	deniedDB     string
	secretDB     string
	tasksPath    string
	networkMode  string
	verbose      bool
	resume       bool
	fresh        bool
	rm           bool
}

func runCommand() *ff.Command {
	ro := runOptions{
		configPath: "config.hsh",
		deniedDB:   denieddb.DefaultPath(),
		secretDB:   secretstore.DefaultPath(),
	}
	fs := ff.NewFlagSet("run")
	fs.StringVar(&ro.configPath, 0, "config", "config.hsh", "config file that seeds the session's config copy")
	fs.StringVar(&ro.workspaceDir, 0, "workspace", "", "workspace directory inside the container (default: first mount target)")
	fs.StringVar(&ro.deniedDB, 0, "denied-db", denieddb.DefaultPath(), "proxy denial database")
	fs.StringVar(&ro.secretDB, 0, "secret-db", secretstore.DefaultPath(), "secret store for hsh:// secret references")
	fs.StringVar(&ro.tasksPath, 0, "tasks", "", "newline-delimited task file (or - for stdin); runs the agent headless")
	fs.StringVar(&ro.networkMode, 0, "network", "", "network policy: https (default), allowlist, or none")
	fs.BoolVar(&ro.verbose, 0, "verbose", "print progress information to stderr (or set HSH_VERBOSE=1)")
	fs.BoolVar(&ro.resume, 0, "resume", "re-enter a session for this workspace (positional: session id)")
	fs.BoolVar(&ro.fresh, 0, "fresh", "with --resume: discard the session and start a new one")
	fs.BoolVar(&ro.rm, 0, "rm", "temporary session: remove pod and state at exit")
	return &ff.Command{
		Name:      "run",
		Usage:     "hsh run [FLAGS] [IMAGE]",
		ShortHelp: "start a sandboxed shell (persistent session; re-enter with --resume)",
		Flags:     fs,
		Exec: func(ctx context.Context, args []string) error {
			if len(args) > 1 {
				return fmt.Errorf("unexpected arguments %v: at most one positional (IMAGE, or a session id with --resume) is accepted, and flags must come before it", args)
			}
			positional := ""
			if len(args) == 1 {
				positional = args[0]
			}
			return run(ctx, ro, positional)
		},
	}
}

func run(ctx context.Context, ro runOptions, positional string) error {
	if ro.verbose {
		verbose = true
	}
	switch ro.networkMode {
	case "", "https", "allowlist", "none":
	default:
		return fmt.Errorf("invalid --network value %q (want https, allowlist, or none)", ro.networkMode)
	}
	if ro.rm && ro.resume {
		return fmt.Errorf("--rm and --resume are mutually exclusive: --rm sessions are removed at exit, --resume re-enters a persistent one")
	}
	if ro.fresh && !ro.resume {
		return fmt.Errorf("--fresh requires --resume")
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	var tasksPrompt string
	if ro.tasksPath != "" {
		var data []byte
		if ro.tasksPath == "-" {
			data, err = io.ReadAll(os.Stdin)
		} else {
			data, err = os.ReadFile(ro.tasksPath)
		}
		if err != nil {
			return fmt.Errorf("read tasks: %w", err)
		}
		tasks := config.ParseTasks(data)
		if len(tasks) == 0 {
			return fmt.Errorf("no tasks found in %s", ro.tasksPath)
		}
		tasksPrompt = config.ComposePrompt(tasks)
		infof("hsh: %d task(s) from %s\n", len(tasks), ro.tasksPath)
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

	var sessionID string
	resumeSession := false
	freshImage := ""
	if ro.resume {
		resumeSession = true
		if positional != "" {
			sessionID = strings.TrimPrefix(positional, "hsh-")
		} else {
			pods, err := listSandboxes("label=hsh.workspace=" + cwd)
			if err != nil {
				return err
			}
			switch len(pods) {
			case 0:
				return fmt.Errorf("no session to resume, try: hsh run IMAGE")
			case 1:
				sessionID = strings.TrimPrefix(pods[0].Name, "hsh-")
			default:
				var ids []string
				for _, p := range pods {
					ids = append(ids, strings.TrimPrefix(p.Name, "hsh-"))
				}
				return fmt.Errorf("multiple sessions for workspace %s:\n  %s\nre-enter with: hsh run --resume <session id>", cwd, strings.Join(ids, "\n  "))
			}
		}
	} else {
		sessionID = randHex(6)
	}
	podName := "hsh-" + sessionID
	sessionDir := denieddb.SessionDir(sessionID)
	sessionConfig := filepath.Join(sessionDir, "config.json")

	var cfg *config.Config
	var mounts []config.Mount
	var workspaceSource string

	if resumeSession {
		cfg, err = config.Load(sessionConfig)
		if err != nil {
			return fmt.Errorf("session %s: %w (remove orphaned session state with: hsh gc)", sessionID, err)
		}
		if !podExists(podName) {
			return fmt.Errorf("session %s has no pod (remove orphaned session state with: hsh gc)", sessionID)
		}
		mounts, err = cfg.ParsedMounts(sessionDir)
		if err != nil {
			return err
		}
		workspaceSource = readWorkspaceFile(sessionDir)
		if ro.fresh {
			infof("hsh: discarding session %s (--fresh)\n", sessionID)
			_, _ = podman("pod", "rm", "-f", "--time", "0", podName)
			_ = os.RemoveAll(sessionDir)
			freshImage = cfg.Image
			resumeSession = false
			ro.resume = false
			ro.fresh = false
			sessionID = randHex(6)
			podName = "hsh-" + sessionID
			sessionDir = denieddb.SessionDir(sessionID)
			sessionConfig = filepath.Join(sessionDir, "config.json")
		}
	}

	if !resumeSession {
		image := positional
		if image == "" && freshImage != "" {
			image = freshImage
		}
		absSeed, err := filepath.Abs(ro.configPath)
		if err != nil {
			return err
		}
		var mountBase string
		if fileExists(absSeed) {
			cfg, err = config.Load(absSeed)
			if err != nil {
				return err
			}
			mountBase = filepath.Dir(absSeed)
		} else {
			if image == "" {
				return fmt.Errorf("no config file at %s and no image given; try: hsh run IMAGE (a harness image supplies its default config via the org.ifup.hsh label)", absSeed)
			}
			label, err := imageLabel(image)
			if err != nil {
				return err
			}
			cfg, err = config.FromImageLabel(label)
			if err != nil {
				return err
			}
			mountBase = cwd
			infof("hsh: using config from the org.ifup.hsh label of %s\n", image)
		}
		if image != "" {
			cfg.Image = image
		}
		mounts, err = cfg.ParsedMounts(mountBase)
		if err != nil {
			return err
		}
		absMounts := make([]string, 0, len(mounts))
		for _, m := range mounts {
			am := m.Source + ":" + m.Target
			if m.Options != "" {
				am += ":" + m.Options
			}
			absMounts = append(absMounts, am)
		}
		cfg.Mounts = absMounts
		workspaceSource = mounts[0].Source
		if err := os.MkdirAll(sessionDir, 0o755); err != nil {
			return err
		}
		data, err := json.MarshalIndent(cfg, "", "  ")
		if err != nil {
			return err
		}
		if err := os.WriteFile(sessionConfig, append(data, '\n'), 0o644); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(sessionDir, "workspace"), []byte(workspaceSource+"\n"), 0o644); err != nil {
			return err
		}
	}
	failureCleanup := true
	defer func() {
		if failureCleanup && !resumeSession && !ro.resume {
			removeSessionState(podName, sessionDir)
		}
	}()

	wsDir := ro.workspaceDir
	if wsDir == "" {
		wsDir = mounts[0].Target
	}
	var agentExec []string
	if tasksPrompt != "" {
		if len(cfg.Agent.Command) == 0 {
			return fmt.Errorf("--tasks needs an agent command; the session config must declare one (\"agent\":{\"command\":[...]})")
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
	caCertPath := filepath.Join(sessionDir, "ca.crt")
	secretsPath := filepath.Join(sessionDir, "secrets.json")
	if _, err := ensureCA(sessionDir, resumeSession); err != nil {
		return err
	}

	var secretStore config.SecretStore
	storeOpened := false
	openStore := func() (config.SecretStore, error) {
		if !storeOpened {
			db, _, err := secretstore.Open(ro.secretDB)
			if err != nil {
				return nil, err
			}
			secretStore = db
			storeOpened = true
		}
		return secretStore, nil
	}
	storeFor := func(c *config.Config) config.SecretStore {
		for _, s := range c.Secrets {
			if strings.HasPrefix(s.From, "hsh://") {
				store, err := openStore()
				if err != nil {
					fmt.Fprintf(os.Stderr, "hsh: warning: opening the secret store: %v\n", err)
					return nil
				}
				return store
			}
		}
		return nil
	}
	resolve := func(c *config.Config) error {
		rs, missing, err := config.ResolveSecrets(c, storeFor(c))
		if err != nil {
			return err
		}
		for _, m := range missing {
			fmt.Fprintf(os.Stderr, "hsh: warning: secret %s is not set (env variable or hsh secret); it will not be injected\n", m)
		}
		if err := config.WriteSecretsFile(secretsPath, rs); err != nil {
			return err
		}
		infof("hsh: %d secret(s) resolved, %d missing\n", len(rs.Secrets), len(missing))
		return nil
	}
	if err := resolve(cfg); err != nil {
		return err
	}
	go config.WatchFile(sessionConfig, time.Second, func([]byte) {
		cfg2, err := config.Load(sessionConfig)
		if err != nil {
			fmt.Fprintf(os.Stderr, "hsh: config reload: %v\n", err)
			return
		}
		if err := resolve(cfg2); err != nil {
			fmt.Fprintf(os.Stderr, "hsh: secrets reload: %v\n", err)
			return
		}
		infof("hsh: configuration reloaded\n")
	})

	proxyName := podName + "-proxy"
	wsName := podName + "-ws"
	stateDir := filepath.Dir(ro.deniedDB)
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		return err
	}
	proxyArgs := []string{
		"create", "--pod", podName, "--name", proxyName,
		"--security-opt", "label=disable",
		"--entrypoint", "/hsh-proxy",
		"-v", exe + ":/hsh-proxy:ro",
		"-v", sessionDir + ":/hsh-session:ro",
		"-v", stateDir + ":/hsh-state",
	}
	roots := findRootBundle()
	if roots != "" {
		proxyArgs = append(proxyArgs, "-v", roots+":/hsh-roots.pem:ro")
	}
	proxyArgs = append(proxyArgs,
		cfg.Image,
		"--listen", netsetup.HostIP+":"+netsetup.ProxyPort,
		"--config", "/hsh-session/config.json",
		"--secrets", "/hsh-session/secrets.json",
		"--ca-cert", "/hsh-session/ca.crt",
		"--ca-key", "/hsh-session/ca.key",
		"--denied-db", "/hsh-state/"+filepath.Base(ro.deniedDB),
		"--session", sessionID,
		"--workspace", workspaceSource,
	)
	if roots != "" {
		proxyArgs = append(proxyArgs, "--roots", "/hsh-roots.pem")
	}
	if ro.networkMode != "" {
		proxyArgs = append(proxyArgs, "--network-mode", ro.networkMode)
	}

	reattach := false
	if resumeSession {
		if !containerExists(proxyName) {
			if out, err := podman(proxyArgs...); err != nil {
				return fmt.Errorf("create proxy container: %w\n%s", err, out)
			}
		} else if !containerRunning(proxyName) {
			if out, err := podman("start", proxyName); err != nil {
				return fmt.Errorf("start proxy container: %w\n%s", err, out)
			}
		}
		reattach = true
		infof("hsh: resuming session %s for workspace %s\n", sessionID, workspaceSource)
	} else {
		infof("hsh: creating session %s (image %s)\n", sessionID, cfg.Image)
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
	failureCleanup = false

	stopSession := func() {
		_ = os.Remove(secretsPath)
		if out, err := podman("pod", "stop", "--time", "2", podName); err != nil {
			fmt.Fprintf(os.Stderr, "hsh: stopping session pod: %v\n%s", err, out)
		}
	}
	cleanup := stopSession
	if ro.rm {
		cleanup = func() { removeSessionState(podName, sessionDir) }
	}
	sigCh := make(chan os.Signal, 2)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		cleanup()
		os.Exit(130)
	}()

	wsArgs := []string{
		"create", "--pod", podName, "--name", wsName,
		"--security-opt", "label=disable",
		"--cap-add", "NET_ADMIN", "--cap-add", "SYS_ADMIN",
		"--entrypoint", "/hsh-workspace",
		"-v", exe + ":/hsh-workspace:ro",
		"-v", exe + ":/hsh-workspace-inner:ro",
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
		cfg.Image,
		"--shell", cfg.Shell,
		"--inner-exe", "/hsh-workspace-inner",
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
	if reattach {
		if containerRunning(wsName) {
			return fmt.Errorf("session %s is already running; exit it first or pick another session", sessionID)
		}
		if !containerExists(wsName) {
			if out, err := podman(wsArgs...); err != nil {
				return fmt.Errorf("create workspace container: %w\n%s", err, out)
			}
		}
		code, err := attachContainer(wsName)
		cleanup()
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
	cleanup()
	code := 0
	if runErr != nil {
		if exitErr, ok := runErr.(*exec.ExitError); ok {
			code = exitErr.ExitCode()
		} else {
			return runErr
		}
	}
	if code != 0 {
		os.Exit(code)
	}
	return nil
}

func removeSessionState(podName, sessionDir string) {
	_, _ = podman("pod", "rm", "-f", "--time", "0", podName)
	_ = os.RemoveAll(sessionDir)
}

func ensureCA(sessionDir string, existing bool) (*certs.CA, error) {
	certPath := filepath.Join(sessionDir, "ca.crt")
	keyPath := filepath.Join(sessionDir, "ca.key")
	if existing {
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

func readWorkspaceFile(sessionDir string) string {
	data, err := os.ReadFile(filepath.Join(sessionDir, "workspace"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
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
