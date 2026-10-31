//go:build !windows

package game

import "syscall"

func alive(pid int) bool {
	// Signal 0 checks for existence. A zombie still exists until reaped, but
	// its parent (the fake game) is dead, so init reaps it quickly.
	return syscall.Kill(pid, 0) == nil
}
