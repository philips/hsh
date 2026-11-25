package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"z.gfire.dev/gvx/pkg/gvx"
	"z.gfire.dev/gvx/pkg/gvx/control"
)

func dialControl(ctx context.Context, path string) *control.Client {
	c, err := control.Dial(ctx, control.DialOptions{Endpoint: path, ClientName: "gvx-spike"})
	if err != nil {
		fail("control dial", err)
	}
	return c
}

func clientExec(args []string) int {
	fs := flag.NewFlagSet("exec", flag.ExitOnError)
	path := fs.String("control", "", "control socket path")
	tty := fs.Bool("tty", false, "allocate a guest pty")
	interactive := fs.Bool("i", false, "forward stdin")
	user := fs.String("user", "", "guest user")
	workdir := fs.String("workdir", "", "guest working directory")
	var env listFlag
	fs.Var(&env, "env", "KEY=VALUE (repeatable)")
	_ = fs.Parse(args)

	ctx := context.Background()
	client := dialControl(ctx, *path)
	defer client.Close()
	report("capabilities: %s", strings.Join(client.Capabilities(), ","))

	var rows, cols uint16 = 24, 80
	if *tty {
		if r, c, ok := gvx.HostTerminalSize(os.Stdin, os.Stdout); ok {
			rows, cols = r, c
		}
	}
	stream, err := client.OpenStream(ctx, control.MethodExecStart, control.ExecStartRequest{
		Argv: fs.Args(), Env: env, User: *user, WorkDir: *workdir,
		Stdin: *interactive, TTY: *tty, Rows: rows, Cols: cols,
	})
	if err != nil {
		fail("exec.start", err)
	}
	defer stream.Close()
	var resp control.ExecStartResponse
	if err := stream.WaitResponse(ctx, &resp); err != nil {
		fail("exec.start", err)
	}
	report("guest pid %d", resp.PID)

	if *tty && gvx.IsHostTerminal(os.Stdin) {
		if restore, ok := gvx.EnterHostRawMode(os.Stdin, os.Stdout); ok {
			defer restore()
		}
		stop := gvx.WatchHostWinch(ctx, os.Stdin, os.Stdout, func(r, c uint16) {
			_ = stream.SendEvent(ctx, "exec.resize", control.ExecResizeEvent{Rows: r, Cols: c})
		})
		defer stop()
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, _ = io.Copy(os.Stdout, stream.ChannelReader(ctx, control.ChannelStdout))
	}()
	if !*tty {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = io.Copy(os.Stderr, stream.ChannelReader(ctx, control.ChannelStderr))
		}()
	}
	if *interactive {
		go func() {
			w := stream.ChannelWriter(ctx, control.ChannelStdin, false)
			_, _ = io.Copy(w, os.Stdin)
			_ = w.Close()
		}()
	}
	for {
		ev, err := stream.RecvEvent(ctx)
		if err != nil {
			fail("exec events", err)
		}
		if ev.Type != "exec.exit" {
			continue
		}
		var exit control.ExecExitEvent
		if err := json.Unmarshal(ev.Data, &exit); err != nil {
			fail("exec.exit", err)
		}
		wg.Wait()
		return exit.ExitCode
	}
}

func clientDial(args []string) int {
	fs := flag.NewFlagSet("dial", flag.ExitOnError)
	path := fs.String("control", "", "control socket path")
	port := fs.Uint("port", 0, "guest TCP port")
	_ = fs.Parse(args)

	ctx := context.Background()
	client := dialControl(ctx, *path)
	defer client.Close()
	stream, err := client.OpenStream(ctx, control.MethodNetDial, control.NetDialRequest{
		Proto: "tcp", GuestPort: uint16(*port), LoopbackFallback: true,
	})
	if err != nil {
		fail("net.dial", err)
	}
	defer stream.Close()
	var resp control.NetDialResponse
	if err := stream.WaitResponse(ctx, &resp); err != nil {
		fail("net.dial", err)
	}
	report("connected to %s", resp.RemoteAddr)
	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(os.Stdout, stream.ChannelReader(ctx, control.ChannelGeneric))
		close(done)
	}()
	w := stream.ChannelWriter(ctx, control.ChannelGeneric, false)
	_, _ = io.Copy(w, os.Stdin)
	_ = w.Close()
	<-done
	return 0
}

func clientCall(args []string) int {
	fs := flag.NewFlagSet("call", flag.ExitOnError)
	path := fs.String("control", "", "control socket path")
	_ = fs.Parse(args)
	if fs.NArg() < 1 {
		fail("call", errors.New("usage: call -control PATH METHOD [JSON]"))
	}
	params := json.RawMessage("{}")
	if fs.NArg() > 1 {
		params = json.RawMessage(fs.Arg(1))
	}
	ctx := context.Background()
	client := dialControl(ctx, *path)
	defer client.Close()
	var out json.RawMessage
	start := time.Now()
	if err := client.Call(ctx, fs.Arg(0), params, &out); err != nil {
		fail(fs.Arg(0), err)
	}
	report("%s took %s", fs.Arg(0), time.Since(start).Round(time.Microsecond))
	fmt.Println(string(out))
	return 0
}
