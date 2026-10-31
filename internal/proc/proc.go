// Package proc starts child processes so that they can be stopped together
// with everything they spawn, on every OS. On Unix the child gets its own
// process group; on Windows it gets a new process group and no console
// window, and the whole tree is ended with taskkill /T.
package proc

import (
	"os/exec"
	"time"
)

// Prepare sets the platform attributes Godot children need. Call it before
// cmd.Start.
func Prepare(cmd *exec.Cmd) {
	prepare(cmd)
	// When the context is cancelled, end the whole tree, not just the parent.
	cmd.Cancel = func() error { return KillTree(cmd) }
	cmd.WaitDelay = 5 * time.Second
}

// KillTree ends cmd's process and all of its descendants.
func KillTree(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	return killTree(cmd)
}
