package main

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"unsafe"
)

const (
	sysLandlockCreateRuleset = 444
	sysLandlockRestrictSelf  = 446

	landlockCreateRulesetVersion = 1 << 0

	accessFSResolveUnix = 1 << 16
	scopeAbstractUnix   = 1 << 0
	scopeSignal         = 1 << 1

	minABI = 9

	prSetNoNewPrivs = 38
)

type rulesetAttr struct {
	handledAccessFS  uint64
	handledAccessNet uint64
	scoped           uint64
}

func landlockABI() (int, error) {
	v, _, errno := syscall.Syscall(sysLandlockCreateRuleset, 0, 0, landlockCreateRulesetVersion)
	if errno != 0 {
		return 0, errno
	}
	return int(v), nil
}

func cmdGuard(args []string) error {
	if len(args) > 0 && args[0] == "--" {
		args = args[1:]
	}
	if len(args) == 0 {
		return fmt.Errorf("guard -- CMD [ARG...]")
	}
	path, err := exec.LookPath(args[0])
	if err != nil {
		return err
	}
	abi, err := landlockABI()
	if err != nil {
		return fmt.Errorf("landlock unavailable: %v", err)
	}
	if abi < minABI {
		return fmt.Errorf("landlock ABI %d < %d: pathname unix socket control (RESOLVE_UNIX) unsupported on this kernel", abi, minABI)
	}
	attr := rulesetAttr{handledAccessFS: accessFSResolveUnix, scoped: scopeAbstractUnix | scopeSignal}
	fd, _, errno := syscall.Syscall(sysLandlockCreateRuleset, uintptr(unsafe.Pointer(&attr)), unsafe.Sizeof(attr), 0)
	if errno != 0 {
		return fmt.Errorf("landlock_create_ruleset: %v", errno)
	}
	if _, _, errno := syscall.RawSyscall6(syscall.SYS_PRCTL, prSetNoNewPrivs, 1, 0, 0, 0, 0); errno != 0 {
		return fmt.Errorf("no_new_privs: %v", errno)
	}
	if _, _, errno := syscall.Syscall(sysLandlockRestrictSelf, fd, 0, 0); errno != 0 {
		return fmt.Errorf("landlock_restrict_self: %v", errno)
	}
	syscall.Close(int(fd))
	return syscall.Exec(path, args, os.Environ())
}
