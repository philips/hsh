package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"golang.org/x/term"
)

const detachKey = 0x1d

func dial(sock string, h header) (net.Conn, error) {
	c, err := net.Dial("unix", sock)
	if err != nil {
		return nil, err
	}
	b, _ := json.Marshal(h)
	_, err = c.Write(append(b, '\n'))
	return c, err
}

func cmdAttach(args []string) error {
	fs := flag.NewFlagSet("attach", flag.ExitOnError)
	sock := fs.String("sock", "", "unix socket path")
	fs.Parse(args)
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return fmt.Errorf("attach needs a terminal on stdin")
	}
	cols, rows, _ := term.GetSize(fd)
	c, err := dial(*sock, header{Op: "attach", Cols: uint16(cols), Rows: uint16(rows)})
	if err != nil {
		return err
	}
	defer c.Close()
	old, err := term.MakeRaw(fd)
	if err != nil {
		return err
	}
	defer term.Restore(fd, old)

	winch := make(chan os.Signal, 1)
	signal.Notify(winch, syscall.SIGWINCH)
	go func() {
		for range winch {
			cols, rows, err := term.GetSize(fd)
			if err == nil {
				writeFrame(c, frameResize, []byte{byte(cols >> 8), byte(cols), byte(rows >> 8), byte(rows)})
			}
		}
	}()

	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			kind, payload, err := readFrame(c)
			if err != nil || kind == frameEnd {
				return
			}
			if kind == frameOutput {
				os.Stdout.Write(payload)
			}
		}
	}()
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := os.Stdin.Read(buf)
			if n > 0 {
				for i := 0; i < n; i++ {
					if buf[i] == detachKey {
						if i > 0 {
							writeFrame(c, frameInput, buf[:i])
						}
						c.Close()
						return
					}
				}
				writeFrame(c, frameInput, buf[:n])
			}
			if err != nil {
				return
			}
		}
	}()
	<-done
	fmt.Fprint(os.Stdout, "\x1b[0m\r\n[detached]\r\n")
	return nil
}

func cmdDump(args []string) error {
	fs := flag.NewFlagSet("dump", flag.ExitOnError)
	sock := fs.String("sock", "", "unix socket path")
	fs.Parse(args)
	c, err := dial(*sock, header{Op: "dump"})
	if err != nil {
		return err
	}
	defer c.Close()
	_, err = io.Copy(os.Stdout, c)
	return err
}

func cmdSend(args []string) error {
	fs := flag.NewFlagSet("send", flag.ExitOnError)
	sock := fs.String("sock", "", "unix socket path")
	fs.Parse(args)
	if fs.NArg() != 1 {
		return fmt.Errorf("send TEXT")
	}
	c, err := dial(*sock, header{Op: "send", Text: fs.Arg(0)})
	if err != nil {
		return err
	}
	c.Close()
	return nil
}

func cmdNotify(args []string) error {
	fs := flag.NewFlagSet("notify", flag.ExitOnError)
	sock := fs.String("sock", "", "unix socket path")
	timeout := fs.Duration("timeout", 0, "give up after this long (0: wait forever)")
	fs.Parse(args)
	if fs.NArg() != 1 {
		return fmt.Errorf("notify PROMPT")
	}
	c, err := dial(*sock, header{Op: "notify", Prompt: fs.Arg(0), Timeout: float64(*timeout) / float64(time.Second)})
	if err != nil {
		return err
	}
	defer c.Close()
	_, err = io.Copy(os.Stdout, c)
	return err
}
