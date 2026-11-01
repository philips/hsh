package cli

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"hsh/internal/netsetup"
	"hsh/internal/sshd"
	"hsh/internal/workspace"
)

const sshdPath = "/hsh-sshd"

const pinWait = 10 * time.Second

func SSHD(args []string) error {
	fs := flag.NewFlagSet("hsh-sshd", flag.ContinueOnError)
	serve := fs.Int("serve", -1, "internal: serve with the session shell snapshot on this fd (set by /hsh-sshd itself after it restricts)")
	deliver := fs.String("deliver", "", "deliver the raw HTTP request from --request to 127.0.0.1:PORT in the sandbox and print the response")
	request := fs.String("request", "", "raw HTTP request for --deliver")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected arguments %v", fs.Args())
	}
	if *serve < 0 {
		return enterAndRestrict(args)
	}
	if err := workspace.CheckRestricted(); err != nil {
		return fmt.Errorf("refusing to serve: %w", err)
	}
	if err := unix.Prctl(unix.PR_SET_DUMPABLE, 0, 0, 0, 0); err != nil {
		return err
	}
	snap, err := workspace.ReadSnapshot(*serve)
	if err != nil {
		return err
	}
	if *deliver != "" {
		return workspace.DeliverLocal(*deliver, *request)
	}
	return sshd.Serve(sshd.Stdio(), sshd.Config{
		Dial: workspace.DialLocalPort,
		List: func() ([]sshd.Listener, error) {
			ls, err := workspace.InnerListeners()
			if err != nil {
				return nil, err
			}
			out := make([]sshd.Listener, len(ls))
			for i, l := range ls {
				out[i] = sshd.Listener{Port: l.Port, Addr: l.Addr}
			}
			return out, nil
		},
		Spawn: func(r sshd.SpawnRequest) (sshd.SpawnResult, error) {
			return spawnShell(snap, r)
		},
	})
}

func enterAndRestrict(args []string) error {
	runtime.LockOSThread()
	pid := 1
	if netsetup.OnPodSide() {
		pin, pinned, err := netsetup.OpenPin(netsetup.PinDir, pinWait)
		if err != nil {
			return err
		}
		if err := unix.Setns(int(pin.Fd()), unix.CLONE_NEWNET); err != nil {
			return fmt.Errorf("enter the session network namespace: %w", err)
		}
		pin.Close()
		pid = pinned
	}
	snap, err := workspace.SnapshotShell(pid).WriteMemfd()
	if err != nil {
		return err
	}
	if err := workspace.RestrictThread(); err != nil {
		return err
	}
	argv := append([]string{sshdPath, "--serve", strconv.Itoa(int(snap.Fd()))}, args...)
	err = syscall.Exec("/proc/self/exe", argv, os.Environ())
	runtime.KeepAlive(snap)
	return err
}

func spawnShell(snap workspace.ShellSnapshot, r sshd.SpawnRequest) (sshd.SpawnResult, error) {
	argv := []string{snap.Shell}
	if r.Command != "" {
		argv = append(argv, "-c", r.Command)
	}
	cmd := &exec.Cmd{
		Path: snap.Shell,
		Args: argv,
		Env:  snap.EnvWithTerm(r.Term),
		Dir:  snap.Dir,
		SysProcAttr: &syscall.SysProcAttr{
			Setsid:  true,
			Setctty: r.Pty,
			Ctty:    0,
		},
	}
	if r.Pty {
		cmd.Stdin = r.Slave
		cmd.Stdout = r.Slave
		cmd.Stderr = r.Slave
	} else {
		cmd.Stdin = r.Stdin
		cmd.Stdout = r.Stdout
		cmd.Stderr = r.Stderr
	}
	if err := cmd.Start(); err != nil {
		return sshd.SpawnResult{}, err
	}
	pgid := cmd.Process.Pid
	return sshd.SpawnResult{
		Signal: func(sig syscall.Signal) error { return syscall.Kill(-pgid, sig) },
		Wait: func() int {
			return sshd.ExitCode(cmd.Wait())
		},
	}, nil
}
