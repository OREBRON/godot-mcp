package game

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/orebron/godot-mcp/internal/godot"
)

// TestMain doubles as a fake Godot: with FAKE_GODOT set, the test binary
// prints like a game, optionally starts a child of its own, and waits.
func TestMain(m *testing.M) {
	switch os.Getenv("FAKE_GODOT") {
	case "":
		os.Exit(m.Run())
	case "child":
		time.Sleep(time.Minute)
		os.Exit(0)
	}
	fmt.Println("Godot Engine v4.7.2.stable.official - https://godotengine.org")
	fmt.Println("args:", strings.Join(os.Args[1:], " "))
	fmt.Fprintln(os.Stderr, "SCRIPT ERROR: Invalid call. Nonexistent function 'jump'.")
	fmt.Fprintln(os.Stderr, "          at: _ready (res://player.gd:12)")
	if os.Getenv("FAKE_GODOT") == "exit" {
		os.Exit(3)
	}
	child := exec.Command(os.Args[0])
	child.Env = append(os.Environ(), "FAKE_GODOT=child")
	child.Start()
	fmt.Println("CHILD_PID", child.Process.Pid)
	time.Sleep(time.Minute)
}

func fake(t *testing.T, mode string) godot.Exec {
	t.Setenv("FAKE_GODOT", mode)
	return godot.Exec{Path: os.Args[0]}
}

func TestRunAndStop(t *testing.T) {
	r := New()
	if err := r.Start(fake(t, "run"), t.TempDir(), "res://main.tscn", true, []string{"--level=2"}); err != nil {
		t.Fatal(err)
	}
	if err := r.Start(fake(t, "run"), t.TempDir(), "", false, nil); err != ErrRunning {
		t.Errorf("second Start = %v, want ErrRunning", err)
	}

	var lines []Line
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		lines, _, _ = r.Output(0, 100)
		if len(lines) >= 5 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	var all []string
	childPID := 0
	for _, l := range lines {
		all = append(all, l.Text)
		fmt.Sscanf(l.Text, "CHILD_PID %d", &childPID)
	}
	text := strings.Join(all, "\n")
	if !strings.Contains(text, "args: --path") || !strings.Contains(text, "--headless res://main.tscn -- --level=2") {
		t.Errorf("unexpected arguments in output:\n%s", text)
	}
	if is := godot.ParseIssues(all); len(is) != 1 || is[0].File != "res://player.gd" || is[0].Line != 12 {
		t.Errorf("issues = %+v", is)
	}
	if st := r.Status(); !st.Running || st.PID == 0 {
		t.Errorf("status while running = %+v", st)
	}

	stopped, err := r.Stop()
	if !stopped || err != nil {
		t.Fatalf("Stop = %v, %v", stopped, err)
	}
	if st := r.Status(); st.Running || st.ExitCode == nil {
		t.Errorf("status after stop = %+v", st)
	}
	if childPID == 0 {
		t.Fatal("the fake game did not report its child")
	}
	// The child must be gone too: Stop ends the whole process tree.
	deadline = time.Now().Add(5 * time.Second)
	for alive(childPID) {
		if time.Now().After(deadline) {
			t.Fatalf("child process %d survived Stop", childPID)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if stopped, _ := r.Stop(); stopped {
		t.Error("second Stop reported a running game")
	}
}

func TestExitCodeAndCursor(t *testing.T) {
	r := New()
	if err := r.Start(fake(t, "exit"), t.TempDir(), "", false, nil); err != nil {
		t.Fatal(err)
	}
	r.Wait(context.Background(), 10*time.Second)
	st := r.Status()
	if st.Running || st.ExitCode == nil || *st.ExitCode != 3 {
		t.Fatalf("status = %+v, want exit code 3", st)
	}
	first, next, _ := r.Output(0, 2)
	rest, end, _ := r.Output(next, 100)
	if len(first) != 2 || next != 2 || len(rest) != st.Lines-2 || end != st.Lines {
		t.Errorf("cursor: first %d next %d rest %d end %d total %d", len(first), next, len(rest), end, st.Lines)
	}
	// A new run is allowed once the old one exited.
	if err := r.Start(fake(t, "exit"), t.TempDir(), "", false, nil); err != nil {
		t.Errorf("restart after exit: %v", err)
	}
	r.Wait(context.Background(), 10*time.Second)
}
