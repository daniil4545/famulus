package main

import (
	"fmt"
	"syscall"
)

// hideProc makes /proc/<pid>/environ and ptrace of the runner root-only, so an agent running
// as the same user cannot read the App key from the runner's environment (R5).
func hideProc() error {
	_, _, errno := syscall.RawSyscall(syscall.SYS_PRCTL, syscall.PR_SET_DUMPABLE, 0, 0)
	if errno != 0 {
		return fmt.Errorf("prctl PR_SET_DUMPABLE: %w", errno)
	}
	return nil
}
