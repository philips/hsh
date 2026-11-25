package main

import (
	"context"
	"flag"
	"fmt"
	"net"
	"os"
	"runtime/pprof"
	"strings"
	"time"

	"z.gfire.dev/gvx/pkg/gvx"
	"z.gfire.dev/gvx/pkg/gvx/control"
)

type listFlag []string

func (l *listFlag) String() string     { return strings.Join(*l, ",") }
func (l *listFlag) Set(v string) error { *l = append(*l, v); return nil }

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "exec":
			os.Exit(clientExec(os.Args[2:]))
		case "dial":
			os.Exit(clientDial(os.Args[2:]))
		case "call":
			os.Exit(clientCall(os.Args[2:]))
		case "peer":
			os.Exit(runPeer(os.Args[2:]))
		}
	}
	image := flag.String("image", "alpine:3.20", "OCI image reference or archive path")
	rootVolume := flag.String("root-volume", "", "ext4lite image path for the persistent root filesystem upper layer")
	workspace := flag.String("workspace", "", "host directory mounted at /workspace")
	network := flag.String("net", "none", "network mode: none, nat, or link")
	linkPath := flag.String("link", "", "Unix stream socket of the packet link peer (implies -net link)")
	linkMTU := flag.Uint("link-mtu", 0, "eth0 MTU in link mode")
	tty := flag.Bool("tty", false, "allocate a guest terminal on this process's stdio")
	workdir := flag.String("workdir", "", "guest working directory")
	user := flag.String("user", "", "guest user")
	hostname := flag.String("hostname", "", "guest hostname")
	pull := flag.String("pull", "", "pull policy: missing, always, or never")
	memory := flag.String("memory", "", "memory limit, e.g. 2g")
	cpuProfile := flag.String("cpuprofile", "", "write a CPU profile of gvx.New to this file")
	controlPath := flag.String("control", "", "serve gvx-ctrl/2 on this Unix socket path")
	checkpointDir := flag.String("checkpoint-dir", "", "pre-opened checkpoint bundle directory (enables lifecycle.checkpoint)")
	restoreDir := flag.String("restore", "", "restore from this checkpoint bundle directory instead of booting")
	noLockdown := flag.Bool("no-lockdown", false, "skip host lockdown")
	var env listFlag
	flag.Var(&env, "env", "KEY=VALUE added to the guest environment (repeatable)")
	flag.Parse()

	cfg := gvx.Config{
		NetworkMode:         gvx.NetworkMode(*network),
		OCIImage:            *image,
		RootVolume:          *rootVolume,
		WorkspacePath:       *workspace,
		Argv:                flag.Args(),
		Envv:                env,
		User:                *user,
		WorkDir:             *workdir,
		Hostname:            *hostname,
		PullPolicy:          gvx.PullPolicy(*pull),
		TTY:                 *tty,
		Stdin:               os.Stdin,
		Stdout:              os.Stdout,
		Stderr:              os.Stderr,
		Warnings:            os.Stderr,
		DisableHostLockdown: *noLockdown,
		CheckpointDir:       *checkpointDir,
	}
	if !*tty {
		cfg.Stdin = nil
	}
	if *memory != "" {
		m, err := gvx.ParseMemory(*memory)
		if err != nil {
			fail("memory", err)
		}
		cfg.Resources.MemoryBytes = m
	}

	if *linkPath != "" {
		conn, err := net.Dial("unix", *linkPath)
		if err != nil {
			fail("link dial", err)
		}
		if uc, ok := conn.(*net.UnixConn); ok {
			_ = uc.SetReadBuffer(4 << 20)
			_ = uc.SetWriteBuffer(4 << 20)
		}
		cfg.NetworkMode = gvx.NetModeLink
		cfg.LinkConn = conn
		cfg.LinkMTU = uint32(*linkMTU)
	}
	ctx := context.Background()
	if *controlPath != "" {
		ep, err := control.Listen(*controlPath, control.ListenOptions{})
		if err != nil {
			fail("control listen", err)
		}
		cfg.ControlListener = ep.Listener
	}
	if *cpuProfile != "" {
		f, err := os.Create(*cpuProfile)
		if err != nil {
			fail("cpuprofile", err)
		}
		if err := pprof.StartCPUProfile(f); err != nil {
			fail("cpuprofile", err)
		}
	}
	start := time.Now()
	var sb *gvx.Sandbox
	var err error
	if *restoreDir != "" {
		sb, err = gvx.Restore(ctx, cfg, gvx.RestoreOptions{Dir: *restoreDir})
	} else {
		sb, err = gvx.New(ctx, cfg)
	}
	if *cpuProfile != "" {
		pprof.StopCPUProfile()
	}
	if err != nil {
		fail("new", err)
	}
	defer sb.Close()
	booted := time.Since(start)
	report("platform=%s lockdown=%+v", sb.PlatformName(), sb.LockdownStatus())
	report("new took %s", booted.Round(time.Millisecond))

	if *controlPath != "" {
		go func() { _ = sb.ServeControl(ctx) }()
	}
	runStart := time.Now()
	code, err := sb.Run(ctx)
	report("run took %s, exit=%d err=%v", time.Since(runStart).Round(time.Millisecond), code, err)
	if err != nil {
		os.Exit(125)
	}
	os.Exit(code)
}

func report(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "spike: "+format+"\n", args...)
}

func fail(what string, err error) {
	report("%s: %v", what, err)
	os.Exit(125)
}
