package workspace

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"hsh/internal/netsetup"
	"hsh/internal/notice"

	"golang.org/x/sys/unix"
)

type ParentOptions struct {
	Shell        string
	Exec         []string
	Launch       []string
	ContextFiles []string
	Workspace    string
	Home         string
	CACert       string
	ProxyAddr    string
	InnerExe     string
}

type InnerOptions struct {
	Shell        string
	Exec         []string
	Launch       []string
	ContextFiles []string
	Workspace    string
	Home         string
	CACert       string
	ProxyAddr    string
}

func RunParent(opts ParentOptions) error {
	if err := netsetup.CreateHostVeth(); err != nil {
		return err
	}
	if err := waitForProxy(net.JoinHostPort(netsetup.HostIP, netsetup.ProxyPort), 30*time.Second); err != nil {
		return err
	}
	readyR, readyW, err := os.Pipe()
	if err != nil {
		return err
	}
	defer readyR.Close()
	goR, goW, err := os.Pipe()
	if err != nil {
		return err
	}
	defer goR.Close()
	if opts.InnerExe == "" {
		return fmt.Errorf("the inner executable path is required: mount the hsh binary as /hsh-workspace-inner and pass --inner-exe")
	}
	innerArgs := []string{
		"--shell", opts.Shell,
		"--ca-cert", opts.CACert,
		"--proxy-addr", opts.ProxyAddr,
		"--workspace", opts.Workspace,
		"--home", opts.Home,
	}
	if len(opts.Exec) > 0 {
		execJSON, err := json.Marshal(opts.Exec)
		if err != nil {
			return err
		}
		innerArgs = append(innerArgs, "--exec-json", string(execJSON))
	}
	if len(opts.Launch) > 0 {
		launchJSON, err := json.Marshal(opts.Launch)
		if err != nil {
			return err
		}
		innerArgs = append(innerArgs, "--launch-json", string(launchJSON))
	}
	if len(opts.ContextFiles) > 0 {
		contextJSON, err := json.Marshal(opts.ContextFiles)
		if err != nil {
			return err
		}
		innerArgs = append(innerArgs, "--context-files-json", string(contextJSON))
	}
	cmd := exec.Command(opts.InnerExe, innerArgs...)
	cmd.ExtraFiles = []*os.File{readyW, goR}
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return err
	}
	readyW.Close()
	goR.Close()
	var b [1]byte
	if _, err := readyR.Read(b[:]); err != nil {
		return fmt.Errorf("waiting for child network namespace: %w", err)
	}
	if err := netsetup.MoveInnerVethToPID(cmd.Process.Pid); err != nil {
		return err
	}
	if err := dropCaps(); err != nil {
		return err
	}
	if _, err := goW.Write(b[:]); err != nil {
		return err
	}
	sigCh := make(chan os.Signal, 4)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP, syscall.SIGQUIT)
	go func() {
		for sig := range sigCh {
			_ = syscall.Kill(cmd.Process.Pid, sig.(syscall.Signal))
		}
	}()
	err = cmd.Wait()
	if err == nil {
		return nil
	}
	if exitErr, ok := err.(*exec.ExitError); ok {
		if ws, ok := exitErr.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			os.Exit(128 + int(ws.Signal()))
		}
		os.Exit(exitErr.ExitCode())
	}
	return err
}

func RunInner(opts InnerOptions) error {
	runtime.LockOSThread()
	if err := syscall.Unshare(syscall.CLONE_NEWNET); err != nil {
		return fmt.Errorf("create network namespace: %w", err)
	}
	readyW := os.NewFile(3, "ready")
	if readyW == nil {
		return fmt.Errorf("ready pipe not found on fd 3")
	}
	if _, err := readyW.Write([]byte{0}); err != nil {
		return err
	}
	goR := os.NewFile(4, "go")
	if goR == nil {
		return fmt.Errorf("go pipe not found on fd 4")
	}
	var b [1]byte
	if _, err := goR.Read(b[:]); err != nil {
		return err
	}
	if err := netsetup.ConfigureInner(); err != nil {
		return err
	}
	if err := writeResolvConf(); err != nil {
		return err
	}
	return enterSession(opts)
}

func RunDirect(opts InnerOptions) error {
	opts.ProxyAddr = ""
	if err := writeResolvConf(); err != nil {
		return err
	}
	return enterSession(opts)
}

func enterSession(opts InnerOptions) error {
	if err := installCA(opts.CACert); err != nil {
		return err
	}
	if err := os.MkdirAll(opts.Home, 0o755); err != nil {
		return err
	}
	applyNotice(&opts, notice.Path)
	stripSetuid(opts.Workspace)

	// Its owner is the parent-namespace UID selected by the mapping, which
	// is 1000 in this example. Access still depends on Linux permission
	// checks, and capabilities take part in those. Namespace root holds
	// CAP_DAC_OVERRIDE in its own user namespace, which lets it override
	// the ordinary permission checks on files whose owner and group are
	// both mapped into that namespace. When I made a 1000:1000 directory
	// read-only with mode 0555, my host shell could not create a file in
	// it, but namespace root could, and the new file was still owned by
	// 1000:1000 on disk. That override stops at the mapping: namespace
	// root gains nothing over host files owned by unmapped IDs, such as
	// root’s files.
	//
	// see
	// https://thomasvanlaere.com/posts/2026/10/exploring-containers-part-4/
	keepCaps := []int{unix.CAP_DAC_OVERRIDE}
	if os.Getenv("HSH_NESTING") == "1" {
		keepCaps = append(keepCaps,
			unix.CAP_SETUID, unix.CAP_SETGID, unix.CAP_SETFCAP, unix.CAP_SETPCAP,
			unix.CAP_SYS_ADMIN, unix.CAP_NET_ADMIN,
			unix.CAP_CHOWN, unix.CAP_FOWNER, unix.CAP_FSETID)
		if err := dropBoundingSetKeep(keepCaps...); err != nil {
			return err
		}
		if err := nestingStorageSetup(); err != nil {
			return err
		}
		os.Setenv("HSH_NESTED", "1")
	}
	if err := dropCapsKeep(keepCaps...); err != nil {
		return err
	}
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return err
	}
	if err := os.Chdir(opts.Workspace); err != nil {
		return err
	}
	if opts.ProxyAddr != "" {
		startCallbackRelay()
	}
	if len(opts.Exec) > 0 {
		argv := opts.Exec
		if !strings.Contains(argv[0], "/") {
			resolved, err := exec.LookPath(argv[0])
			if err != nil {
				return fmt.Errorf("resolve %s: %w", argv[0], err)
			}
			argv[0] = resolved
		}
		return syscall.Exec(argv[0], argv, buildEnv(opts))
	}
	shellArgs := []string{opts.Shell}
	if len(opts.Launch) > 0 && filepath.Base(opts.Shell) == "bash" {
		rcPath := filepath.Join(opts.Home, ".hsh-launch.bashrc")
		if err := writeLaunchRC(rcPath, opts.Launch); err != nil {
			return err
		}
		shellArgs = append(shellArgs, "--rcfile", rcPath)
	}
	return syscall.Exec(opts.Shell, shellArgs, buildEnv(opts))
}

func applyNotice(opts *InnerOptions, noticePath string) {
	text := notice.Read(noticePath)
	argPath := ""
	if text != "" {
		argPath = noticePath
	}
	for _, f := range opts.ContextFiles {
		if err := notice.ApplyContextFile(notice.ExpandHome(f, opts.Home), text); err != nil {
			fmt.Fprintf(os.Stderr, "hsh: warning: network notice in %s: %v\n", f, err)
		}
	}
	if len(opts.Exec) > 0 {
		opts.Exec = notice.SubstituteArgv(opts.Exec, argPath)
	}
	if len(opts.Launch) > 0 {
		opts.Launch = notice.SubstituteArgv(opts.Launch, argPath)
	}
}

func writeLaunchRC(path string, argv []string) error {
	quoted := make([]string, len(argv))
	for i, a := range argv {
		quoted[i] = "'" + strings.ReplaceAll(a, "'", "'\\''") + "'"
	}
	rc := "set -m\n"
	rc += "[ -r \"$HOME/.bashrc\" ] && . \"$HOME/.bashrc\"\n"
	rc += strings.Join(quoted, " ") + "\n"
	return os.WriteFile(path, []byte(rc), 0o644)
}

func waitForProxy(addr string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var err error
	for {
		var conn net.Conn
		conn, err = net.DialTimeout("tcp", addr, 2*time.Second)
		if err == nil {
			conn.Close()
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("egress proxy at %s did not become ready: %w", addr, err)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func buildEnv(opts InnerOptions) []string {
	noProxy := "localhost,127.0.0.1,::1," + netsetup.HostIP + "," + netsetup.InnerIP
	bundle := "/etc/ssl/certs/ca-certificates.crt"
	env := os.Environ()
	env = setEnv(env, "HOME", opts.Home)
	env = setEnv(env, "SHELL", opts.Shell)
	env = setEnv(env, "HSH_SHELL", opts.Shell)
	if os.Getenv("USER") == "" {
		env = setEnv(env, "USER", "root")
	}
	if os.Getenv("LOGNAME") == "" {
		env = setEnv(env, "LOGNAME", "root")
	}
	if os.Getenv("TERM") == "" {
		env = setEnv(env, "TERM", "xterm")
	}
	path := os.Getenv("PATH")
	if path == "" {
		path = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
	}
	env = setEnv(env, "PATH", path)
	if opts.ProxyAddr != "" {
		for _, kv := range [][2]string{
			{"http_proxy", opts.ProxyAddr},
			{"https_proxy", opts.ProxyAddr},
			{"all_proxy", opts.ProxyAddr},
			{"HTTP_PROXY", opts.ProxyAddr},
			{"HTTPS_PROXY", opts.ProxyAddr},
			{"ALL_PROXY", opts.ProxyAddr},
			{"no_proxy", noProxy},
			{"NO_PROXY", noProxy},
		} {
			env = setEnv(env, kv[0], kv[1])
		}
	}
	for _, kv := range [][2]string{
		{"SSL_CERT_FILE", bundle},
		{"GIT_SSL_CAINFO", bundle},
		{"NODE_EXTRA_CA_CERTS", bundle},
		{"CURL_CA_BUNDLE", bundle},
		{"REQUESTS_CA_BUNDLE", bundle},
	} {
		env = setEnv(env, kv[0], kv[1])
	}
	return env
}

func setEnv(env []string, key, value string) []string {
	prefix := key + "="
	for i, e := range env {
		if strings.HasPrefix(e, prefix) {
			env[i] = prefix + value
			return env
		}
	}
	return append(env, prefix+value)
}

func writeResolvConf() error {
	content := "nameserver " + netsetup.HostIP + "\noptions timeout:1 attempts:2\n"
	tmp := "/tmp/hsh-resolv.conf"
	if err := os.WriteFile(tmp, []byte(content), 0o644); err != nil {
		return err
	}
	if err := syscall.Mount(tmp, "/etc/resolv.conf", "", syscall.MS_BIND, ""); err != nil {
		return os.WriteFile("/etc/resolv.conf", []byte(content), 0o644)
	}
	return nil
}

func installCA(caPath string) error {
	caPEM, err := os.ReadFile(caPath)
	if err != nil {
		return err
	}
	const bundlePath = "/etc/ssl/certs/ca-certificates.crt"
	existing, err := os.ReadFile(bundlePath)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if bytes.Contains(existing, caPEM) {
		return nil
	}
	out := existing
	if len(out) > 0 && !bytes.HasSuffix(out, []byte("\n")) {
		out = append(out, '\n')
	}
	out = append(out, caPEM...)
	return os.WriteFile(bundlePath, out, 0o644)
}

func stripSetuid(skip string) {
	skipAbs, _ := filepath.Abs(skip)
	_ = filepath.WalkDir("/", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			switch path {
			case "/proc", "/sys", "/dev":
				return fs.SkipDir
			}
			if abs, _ := filepath.Abs(path); abs == skipAbs {
				return fs.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		if info.Mode()&(os.ModeSetuid|os.ModeSetgid) != 0 {
			_ = os.Chmod(path, info.Mode()&^(os.ModeSetuid|os.ModeSetgid))
		}
		return nil
	})
}

func dropCaps() error {
	return dropCapsKeep()
}

func nestingStorageSetup() error {
	const dir = "/tmp/podman-nest"
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	storage := filepath.Join(dir, "storage.conf")
	storageContent := "[storage]\ndriver = \"vfs\"\nrunroot = \"" + filepath.Join(dir, "runroot") + "\"\ngraphroot = \"" + filepath.Join(dir, "store") + "\"\n"
	if err := os.WriteFile(storage, []byte(storageContent), 0o644); err != nil {
		return err
	}
	os.Setenv("CONTAINERS_STORAGE_CONF", storage)
	containers := filepath.Join(dir, "containers.conf")
	containersContent := "[containers]\ncgroups = \"disabled\"\ncgroupns = \"host\"\nnetns = \"host\"\ncgroup_manager = \"cgroupfs\"\n"
	if err := os.WriteFile(containers, []byte(containersContent), 0o644); err != nil {
		return err
	}
	os.Setenv("CONTAINERS_CONF_OVERRIDE", containers)
	return nil
}

func dropBoundingSetKeep(keep ...int) error {
	keepSet := make(map[int]bool, len(keep))
	for _, c := range keep {
		keepSet[c] = true
	}
	for c := 0; ; c++ {
		isSet, err := unix.PrctlRetInt(unix.PR_CAPBSET_READ, uintptr(c), 0, 0, 0)
		if err == unix.EINVAL {
			return nil
		}
		if err != nil {
			return err
		}
		if isSet == 1 && !keepSet[c] {
			if err := unix.Prctl(unix.PR_CAPBSET_DROP, uintptr(c), 0, 0, 0); err != nil {
				return err
			}
		}
	}
}

func dropCapsKeep(keep ...int) error {
	hdr := &unix.CapUserHeader{Version: unix.LINUX_CAPABILITY_VERSION_3}
	data := make([]unix.CapUserData, 2)
	if err := unix.Capget(hdr, &data[0]); err != nil {
		return err
	}
	old := [2]uint32{data[0].Permitted, data[1].Permitted}
	for i := range data {
		data[i].Effective = 0
		data[i].Permitted = 0
		data[i].Inheritable = 0
	}
	for _, c := range keep {
		i, bit := c/32, uint(c%32)
		if old[i]&(1<<bit) != 0 {
			data[i].Effective |= 1 << bit
			data[i].Permitted |= 1 << bit
		}
	}
	return unix.Capset(hdr, &data[0])
}
