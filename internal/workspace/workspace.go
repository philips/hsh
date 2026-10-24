package workspace

import (
	"bytes"
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

	"harnessh/internal/netsetup"

	"golang.org/x/sys/unix"
)

type ParentOptions struct {
	Shell     string
	Workspace string
	Home      string
	CACert    string
	ProxyAddr string
}

type InnerOptions struct {
	Shell     string
	Workspace string
	Home      string
	CACert    string
	ProxyAddr string
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
	self, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := exec.Command(self, "workspace-inner",
		"--shell", opts.Shell,
		"--ca-cert", opts.CACert,
		"--proxy-addr", opts.ProxyAddr,
		"--workspace", opts.Workspace,
		"--home", opts.Home,
	)
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
	if err := installCA(opts.CACert); err != nil {
		return err
	}
	if err := os.MkdirAll(opts.Home, 0o755); err != nil {
		return err
	}
	stripSetuid(opts.Workspace)
	if err := dropCaps(); err != nil {
		return err
	}
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return err
	}
	if err := os.Chdir(opts.Workspace); err != nil {
		return err
	}
	return syscall.Exec(opts.Shell, []string{opts.Shell}, buildEnv(opts))
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
	for _, kv := range [][2]string{
		{"http_proxy", opts.ProxyAddr},
		{"https_proxy", opts.ProxyAddr},
		{"all_proxy", opts.ProxyAddr},
		{"HTTP_PROXY", opts.ProxyAddr},
		{"HTTPS_PROXY", opts.ProxyAddr},
		{"ALL_PROXY", opts.ProxyAddr},
		{"no_proxy", noProxy},
		{"NO_PROXY", noProxy},
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
	const content = "nameserver 127.0.0.1\noptions timeout:1 attempts:1\n"
	tmp := "/tmp/harnessh-resolv.conf"
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
	hdr := &unix.CapUserHeader{Version: unix.LINUX_CAPABILITY_VERSION_3}
	data := make([]unix.CapUserData, 2)
	if err := unix.Capget(hdr, &data[0]); err != nil {
		return err
	}
	for i := range data {
		data[i].Effective = 0
		data[i].Permitted = 0
		data[i].Inheritable = 0
	}
	return unix.Capset(hdr, &data[0])
}
