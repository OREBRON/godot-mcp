//go:build !windows

package e2e

import (
	"syscall"
	"testing"
	"time"
)

// The game runs in its own process group, so it only stops with the server
// if the server shuts down properly on SIGTERM.
func TestSIGTERMStopsGame(t *testing.T) {
	s := start(t, true)
	run := s.ok("game_run", map[string]any{"scene": "res://demo/main.tscn", "headless": true, "wait_seconds": 1})
	game := int(run["status"].(map[string]any)["pid"].(float64))
	if !alive(game) {
		t.Fatalf("the game (pid %d) is not running", game)
	}
	if err := s.server.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(15 * time.Second)
	for alive(game) || alive(s.server.Process.Pid) {
		if time.Now().After(deadline) {
			syscall.Kill(-game, syscall.SIGKILL)
			t.Fatalf("after SIGTERM: server alive %v, game alive %v", alive(s.server.Process.Pid), alive(game))
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// alive reports whether pid runs and is not a zombie waiting to be reaped.
func alive(pid int) bool {
	var ws syscall.WaitStatus
	if p, _ := syscall.Wait4(pid, &ws, syscall.WNOHANG, nil); p == pid {
		return false // our own child, reaped just now
	}
	return syscall.Kill(pid, 0) == nil
}
