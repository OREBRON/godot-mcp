//go:build !windows

package proc

import (
	"errors"
	"os/exec"
	"syscall"
)

func prepare(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

func killTree(cmd *exec.Cmd) error {
	// A negative pid signals the whole process group created by Setpgid.
	err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}
