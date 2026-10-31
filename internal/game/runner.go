// Package game runs the project as a separate Godot process and keeps its
// output. The server owns the process, so it reads stdout and stderr
// directly instead of scraping the editor's Output panel.
package game

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/orebron/godot-mcp/internal/godot"
	"github.com/orebron/godot-mcp/internal/proc"
)

const maxLines = 10000

// Line is one line of game output. N counts from 0 for the whole run, so it
// works as a cursor even after old lines were dropped.
type Line struct {
	N      int    `json:"n"`
	Stream string `json:"stream"` // "stdout" or "stderr"
	Text   string `json:"text"`
}

// Status describes the current or last run.
type Status struct {
	Running  bool     `json:"running"`
	PID      int      `json:"pid,omitempty"`
	Args     []string `json:"args,omitempty"`
	Uptime   string   `json:"uptime,omitempty"`
	ExitCode *int     `json:"exit_code,omitempty"`
	Lines    int      `json:"total_lines"`
}

// Runner holds at most one running game.
type Runner struct {
	mu       sync.Mutex
	cmd      *exec.Cmd
	args     []string
	started  time.Time
	done     chan struct{}
	exitCode *int
	lines    []Line
	total    int
}

func New() *Runner { return &Runner{} }

// ErrRunning is returned by Start when a game is already running.
var ErrRunning = errors.New("a game is already running")

// Start launches `godot --path dir [--headless] [scene] [-- userArgs]`.
func (r *Runner) Start(e godot.Exec, dir, scene string, headless bool, userArgs []string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cmd != nil && r.exitCode == nil {
		return ErrRunning
	}
	args := []string{"--path", dir}
	if headless {
		args = append(args, "--headless")
	}
	if scene != "" {
		args = append(args, scene)
	}
	if len(userArgs) > 0 {
		args = append(append(args, "--"), userArgs...)
	}
	cmd := e.Command(context.Background(), args...)
	cmd.Dir = dir
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting %s: %w", e, err)
	}
	r.cmd, r.args, r.started = cmd, args, time.Now()
	r.exitCode, r.lines, r.total = nil, nil, 0
	r.done = make(chan struct{})

	var wg sync.WaitGroup
	wg.Add(2)
	go r.read(stdout, "stdout", &wg)
	go r.read(stderr, "stderr", &wg)
	done := r.done
	go func() {
		wg.Wait()
		err := cmd.Wait()
		code := 0
		if err != nil {
			code = -1
			var ee *exec.ExitError
			if errors.As(err, &ee) {
				code = ee.ExitCode()
			}
		}
		r.mu.Lock()
		if r.cmd == cmd {
			r.exitCode = &code
		}
		r.mu.Unlock()
		close(done)
	}()
	return nil
}

func (r *Runner) read(rd io.Reader, stream string, wg *sync.WaitGroup) {
	defer wg.Done()
	sc := bufio.NewScanner(rd)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		text := strings.ToValidUTF8(godot.CleanLine(sc.Text()), "�")
		r.mu.Lock()
		r.lines = append(r.lines, Line{N: r.total, Stream: stream, Text: text})
		r.total++
		if len(r.lines) > maxLines {
			r.lines = r.lines[len(r.lines)-maxLines:]
		}
		r.mu.Unlock()
	}
}

// Stop ends the game and everything it started.
func (r *Runner) Stop() (bool, error) {
	r.mu.Lock()
	cmd, done := r.cmd, r.done
	running := cmd != nil && r.exitCode == nil
	r.mu.Unlock()
	if !running {
		return false, nil
	}
	if err := proc.KillTree(cmd); err != nil {
		return true, err
	}
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		return true, errors.New("the game did not exit within 10 seconds after being killed")
	}
	return true, nil
}

// Wait blocks until the game exits or d passes, whichever comes first.
func (r *Runner) Wait(ctx context.Context, d time.Duration) {
	r.mu.Lock()
	done := r.done
	r.mu.Unlock()
	if done == nil {
		return
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-done:
	case <-t.C:
	case <-ctx.Done():
	}
}

// Status reports the current or last run.
func (r *Runner) Status() Status {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := Status{Lines: r.total, ExitCode: r.exitCode, Args: r.args}
	if r.cmd != nil && r.exitCode == nil {
		s.Running = true
		s.PID = r.cmd.Process.Pid
		s.Uptime = time.Since(r.started).Round(time.Millisecond).String()
	}
	return s
}

// Output returns up to max lines with N >= since, and the cursor for the next
// call. dropped is how many requested lines were already discarded.
func (r *Runner) Output(since, max int) (lines []Line, next, dropped int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if since < 0 {
		since = 0
	}
	first := r.total - len(r.lines)
	if since < first {
		dropped = first - since
		since = first
	}
	start := since - first
	if start > len(r.lines) {
		start = len(r.lines)
	}
	end := len(r.lines)
	if max > 0 && end-start > max {
		end = start + max
	}
	lines = append(lines, r.lines[start:end]...)
	return lines, first + end, dropped
}
