package main

import (
	"fmt"
	"os"
	"path/filepath"
)

func cmdDemo(args []string) error {
	o, err := parseServeFlags("demo", args)
	if err != nil {
		return err
	}
	if len(o.args) == 0 {
		shell := os.Getenv("SHELL")
		if shell == "" {
			shell = "/bin/sh"
		}
		o.args = []string{shell}
	}
	o.fake = true
	if o.name == "session" {
		o.name = "demo"
	}
	dir, err := os.MkdirTemp("", "ptymux-demo-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	o.sock = filepath.Join(dir, "s.sock")
	if o.logPath == "" {
		o.logPath = filepath.Join(dir, "decisions.log")
	}
	s, err := newSession(o)
	if err != nil {
		return err
	}
	ready := make(chan struct{})
	go s.serve(o, func() { close(ready) })
	<-ready
	err = cmdAttach([]string{"--sock", o.sock})
	s.shutdown()
	if data, rerr := os.ReadFile(o.logPath); rerr == nil && len(data) > 0 {
		fmt.Printf("decisions made in the review dialog:\n%s", data)
	}
	return err
}
