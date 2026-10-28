package main

import (
	"fmt"
	"os"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	args := os.Args[2:]
	var err error
	switch os.Args[1] {
	case "serve":
		err = cmdServe(args)
	case "attach":
		err = cmdAttach(args)
	case "dump":
		err = cmdDump(args)
	case "send":
		err = cmdSend(args)
	case "deny":
		err = cmdDeny(args)
	case "status":
		err = cmdStatus(args)
	case "demo":
		err = cmdDemo(args)
	case "notify":
		err = cmdNotify(args)
	default:
		usage()
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "ptymux: %v\n", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `usage: ptymux COMMAND

  demo [-- CMD [ARG...]]                        try it: your shell with the hsh status line and fake proxy denials
  serve --sock S [--log FILE] [--fake] -- CMD   run CMD in a pty; track its screen with libghostty-vt
  attach --sock S                               attach (Ctrl-] detaches); the screen is restored from emulator state
  dump --sock S                                 print the current screen as plain text
  send --sock S TEXT                            type TEXT into the session without a client
  deny --sock S HOST                            report a proxy denial (adds a review item)
  status --sock S                               print the mode, status line color, and pending count
  notify --sock S [--timeout D] PROMPT          pop an approval prompt over the attached client; prints approved or denied`)
	os.Exit(2)
}
