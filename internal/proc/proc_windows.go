//go:build windows

package proc

import (
	"os/exec"
	"strconv"
	"syscall"
)

const createNoWindow = 0x08000000

func prepare(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= syscall.CREATE_NEW_PROCESS_GROUP | createNoWindow
}

func killTree(cmd *exec.Cmd) error {
	// Killing only the parent on Windows leaves its children running, so end
	// the tree. taskkill ships with every Windows version since XP.
	kill := exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid))
	kill.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
	if err := kill.Run(); err != nil {
		// The process may have exited already; fall back to the plain kill.
		return cmd.Process.Kill()
	}
	return nil
}
