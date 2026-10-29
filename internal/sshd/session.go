package sshd

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"syscall"

	"golang.org/x/crypto/ssh"
	"golang.org/x/sys/unix"
)

type SpawnRequest struct {
	Command string
	Pty     bool
	Term    string
	Stdin   *os.File
	Stdout  *os.File
	Stderr  *os.File
	Slave   *os.File
}

type SpawnResult struct {
	Signal func(syscall.Signal) error
	Wait   func() int
}

func ExitCode(err error) int {
	if err == nil {
		return 0
	}
	if exitErr, ok := err.(*exec.ExitError); ok {
		if ws, ok := exitErr.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			return 128 + int(ws.Signal())
		}
		return exitErr.ExitCode()
	}
	return 1
}

var signalsByName = map[string]syscall.Signal{
	"ABRT": syscall.SIGABRT,
	"ALRM": syscall.SIGALRM,
	"HUP":  syscall.SIGHUP,
	"INT":  syscall.SIGINT,
	"KILL": syscall.SIGKILL,
	"QUIT": syscall.SIGQUIT,
	"TERM": syscall.SIGTERM,
	"USR1": syscall.SIGUSR1,
	"USR2": syscall.SIGUSR2,
}

const stderrExtendedCode = 1

type sessionServer struct {
	cfg    Config
	ch     ssh.Channel
	master *os.File
	req    SpawnRequest
	pipes  [3]*os.File
}

func handleSession(newCh ssh.NewChannel, cfg Config) {
	if cfg.Spawn == nil {
		_ = newCh.Reject(ssh.Prohibited, "no shells are configured for this session")
		return
	}
	ch, reqs, err := newCh.Accept()
	if err != nil {
		return
	}
	s := &sessionServer{cfg: cfg, ch: ch}
	started, res, err := s.handshake(reqs)
	if err != nil || !started {
		s.closeLocal()
		ch.Close()
		return
	}
	s.serve(reqs, res)
}

func (s *sessionServer) handshake(reqs <-chan *ssh.Request) (bool, SpawnResult, error) {
	for r := range reqs {
		switch r.Type {
		case "pty-req":
			var p struct {
				Term   string
				Cols   uint32
				Rows   uint32
				Width  uint32
				Height uint32
				Modes  []byte
			}
			if err := ssh.Unmarshal(r.Payload, &p); err != nil {
				_ = r.Reply(false, nil)
				continue
			}
			master, slave, err := openPty()
			if err != nil {
				_ = r.Reply(false, []byte(err.Error()))
				continue
			}
			setWinsize(master, int(p.Rows), int(p.Cols))
			s.master = master
			s.req.Pty = true
			s.req.Term = p.Term
			s.req.Slave = slave
			_ = r.Reply(true, nil)
		case "env":
			_ = r.Reply(false, []byte("the session's environment is not configurable"))
		case "shell":
			res, err := s.spawn("")
			_ = r.Reply(err == nil, errorBytes(err))
			return err == nil, res, err
		case "exec":
			var payload struct{ Command string }
			if err := ssh.Unmarshal(r.Payload, &payload); err != nil {
				_ = r.Reply(false, []byte("malformed exec request"))
				continue
			}
			res, err := s.spawn(payload.Command)
			_ = r.Reply(err == nil, errorBytes(err))
			return err == nil, res, err
		case "subsystem":
			_ = r.Reply(false, []byte("subsystems are not supported; use shell or exec"))
		default:
			_ = r.Reply(false, nil)
		}
	}
	return false, SpawnResult{}, nil
}

func errorBytes(err error) []byte {
	if err == nil {
		return nil
	}
	return []byte(err.Error())
}

func (s *sessionServer) spawn(command string) (SpawnResult, error) {
	if !s.req.Pty {
		inR, inW, err := os.Pipe()
		if err != nil {
			return SpawnResult{}, err
		}
		outR, outW, err := os.Pipe()
		if err != nil {
			inR.Close()
			inW.Close()
			return SpawnResult{}, err
		}
		errR, errW, err := os.Pipe()
		if err != nil {
			inR.Close()
			inW.Close()
			outR.Close()
			outW.Close()
			return SpawnResult{}, err
		}
		s.req.Stdin = inR
		s.req.Stdout = outW
		s.req.Stderr = errW
		s.pipes = [3]*os.File{inW, outR, errR}
	}
	s.req.Command = command
	res, err := s.cfg.Spawn(s.req)
	if s.req.Stdin != nil {
		s.req.Stdin.Close()
	}
	if s.req.Stdout != nil {
		s.req.Stdout.Close()
	}
	if s.req.Stderr != nil {
		s.req.Stderr.Close()
	}
	if s.req.Slave != nil {
		s.req.Slave.Close()
	}
	if err != nil {
		s.closeLocal()
		return SpawnResult{}, err
	}
	return res, nil
}

func (s *sessionServer) closeLocal() {
	if s.master != nil {
		s.master.Close()
		s.master = nil
	}
	for i, f := range s.pipes {
		if f != nil {
			f.Close()
		}
		s.pipes[i] = nil
	}
}

func (s *sessionServer) serve(reqs <-chan *ssh.Request, res SpawnResult) {
	go func() {
		for r := range reqs {
			switch r.Type {
			case "window-change":
				var w struct {
					Cols   uint32
					Rows   uint32
					Width  uint32
					Height uint32
				}
				if s.master != nil && ssh.Unmarshal(r.Payload, &w) == nil {
					setWinsize(s.master, int(w.Rows), int(w.Cols))
				}
			case "signal":
				var payload struct{ Name string }
				if ssh.Unmarshal(r.Payload, &payload) == nil {
					if sig, ok := signalsByName[payload.Name]; ok && res.Signal != nil {
						_ = res.Signal(sig)
					}
				}
			}
		}
	}()

	var out sync.WaitGroup
	in := make(chan struct{})
	if s.master != nil {
		out.Add(1)
		go func() {
			defer out.Done()
			_, _ = io.Copy(s.ch, s.master)
		}()
		go func() {
			_, _ = io.Copy(s.master, s.ch)
			close(in)
		}()
	} else {
		out.Add(2)
		go func() {
			defer out.Done()
			_, _ = io.Copy(s.ch, s.pipes[1])
		}()
		go func() {
			defer out.Done()
			buf := make([]byte, 32*1024)
			for {
				n, err := s.pipes[2].Read(buf)
				if n > 0 {
					if _, werr := s.ch.Stderr().Write(buf[:n]); werr != nil {
						return
					}
				}
				if err != nil {
					return
				}
			}
		}()
		go func() {
			_, _ = io.Copy(s.pipes[0], s.ch)
			s.pipes[0].Close()
			close(in)
		}()
	}

	exit := res.Wait()
	out.Wait()
	_, _ = s.ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{uint32(exit)}))
	s.ch.Close()
	<-in
	s.closeLocal()
}

func openPty() (master, slave *os.File, err error) {
	master, err = os.OpenFile("/dev/ptmx", syscall.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		return nil, nil, fmt.Errorf("open /dev/ptmx: %w", err)
	}
	n, err := unix.IoctlGetInt(int(master.Fd()), unix.TIOCGPTN)
	if err != nil {
		master.Close()
		return nil, nil, fmt.Errorf("query pty number: %w", err)
	}
	if err := unix.IoctlSetPointerInt(int(master.Fd()), unix.TIOCSPTLCK, 0); err != nil {
		master.Close()
		return nil, nil, fmt.Errorf("unlock pty: %w", err)
	}
	slave, err = os.OpenFile(fmt.Sprintf("/dev/pts/%d", n), syscall.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		master.Close()
		return nil, nil, fmt.Errorf("open pty slave: %w", err)
	}
	return master, slave, nil
}

func setWinsize(master *os.File, rows, cols int) {
	_ = unix.IoctlSetWinsize(int(master.Fd()), unix.TIOCSWINSZ, &unix.Winsize{
		Row: uint16(rows),
		Col: uint16(cols),
	})
}
