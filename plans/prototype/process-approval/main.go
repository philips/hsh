package main

import (
	"fmt"
	"os"
	"runtime"
)

func init() {
	runtime.LockOSThread()
}

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	args := os.Args[2:]
	var err error
	switch os.Args[1] {
	case "broker":
		err = cmdBroker(args)
	case "proxy-deny":
		err = cmdProxyDeny(args)
	case "pending":
		err = cmdPending(args)
	case "approve":
		err = cmdApprove(args)
	case "guard":
		err = cmdGuard(args)
	case "agent":
		err = cmdAgent(args)
	default:
		usage()
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "pa: %v\n", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `usage: pa COMMAND

  broker --proxy-sock P --admin-sock A --shell-pid PID   stand-in for the workspace supervisor
  proxy-deny --proxy-sock P HOST                         stand-in for the proxy reporting a denial
  pending --admin-sock A                                 list hosts awaiting approval
  approve --admin-sock A (HOST | --all)                  approve from the admin shell
  guard -- CMD [ARG...]                                  run CMD in a Landlock domain with no socket access
  agent --out FILE --ticks N                             demo workload`)
	os.Exit(2)
}
