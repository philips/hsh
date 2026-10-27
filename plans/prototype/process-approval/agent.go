package main

import (
	"flag"
	"fmt"
	"os"
	"time"
)

func cmdAgent(args []string) error {
	fs := flag.NewFlagSet("agent", flag.ExitOnError)
	out := fs.String("out", "", "file that receives one line per tick")
	ticks := fs.Int("ticks", 40, "")
	fs.Parse(args)
	f, err := os.OpenFile(*out, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	fmt.Println("agent: started")
	for i := 1; i <= *ticks; i++ {
		fmt.Fprintf(f, "tick %d\n", i)
		time.Sleep(100 * time.Millisecond)
	}
	fmt.Println("agent: done")
	return nil
}
