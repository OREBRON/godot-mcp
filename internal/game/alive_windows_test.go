//go:build windows

package game

import (
	"os/exec"
	"strconv"
	"strings"
)

func alive(pid int) bool {
	out, _ := exec.Command("tasklist", "/FI", "PID eq "+strconv.Itoa(pid), "/NH").Output()
	return strings.Contains(string(out), strconv.Itoa(pid))
}
