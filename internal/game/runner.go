// Package game runs the project as a separate Godot process and keeps its
// output. The server owns the process, so it reads stdout and stderr
// directly instead of scraping the editor's Output panel.
package game

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
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
	// Bridge is "connected" once the game's bridge has dialled back,
	// "waiting" before that, and empty when no game runs.
	Bridge string `json:"bridge,omitempty"`
}

// Runner holds at most one running game.
type Runner struct {
	mu       sync.Mutex
	cmd      *exec.Cmd
	link     *Link
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

// StartOptions describe one run.
type StartOptions struct {
	Scene    string   // scene to launch; empty launches the project's main scene
	Headless bool     // no window, no rendering
	Args     []string // arguments for the game, after "--"
	Env      []string // added to the server's environment
	Link     *Link    // the run's bridge connection; closed when the run ends
}

// Start launches `godot --path dir [--headless] [scene] [-- args]`.
func (r *Runner) Start(e godot.Exec, dir string, opts StartOptions) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cmd != nil && r.exitCode == nil {
		return ErrRunning
	}
	args := godotArgs(dir, opts)
	cmd := e.Command(context.Background(), args...)
	cmd.Dir = dir
	if len(opts.Env) > 0 {
		cmd.Env = append(os.Environ(), opts.Env...)
	}
	// os/exec copies the output into these writers and, through WaitDelay,
	// stops waiting for it once the game has exited, even when a process the
	// game started still holds the pipes open.
	stdout := &lineWriter{r: r, cmd: cmd, stream: "stdout"}
	stderr := &lineWriter{r: r, cmd: cmd, stream: "stderr"}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting %s: %w", e, err)
	}
	r.cmd, r.link, r.args, r.started = cmd, opts.Link, args, time.Now()
	r.exitCode, r.lines, r.total = nil, nil, 0
	r.done = make(chan struct{})

	go r.wait(cmd, r.done, opts.Link, stdout, stderr)
	return nil
}

// godotArgs is `--path dir [--headless] [scene] [-- args]`.
func godotArgs(dir string, opts StartOptions) []string {
	args := []string{"--path", dir}
	if opts.Headless {
		args = append(args, "--headless")
	}
	if opts.Scene != "" {
		args = append(args, opts.Scene)
	}
	if len(opts.Args) > 0 {
		args = append(append(args, "--"), opts.Args...)
	}
	return args
}

// wait records how cmd's run ended, then closes done.
func (r *Runner) wait(cmd *exec.Cmd, done chan struct{}, link *Link, stdout, stderr *lineWriter) {
	cmd.Wait()
	if link != nil {
		link.Close()
	}
	stdout.flush()
	stderr.flush()
	code := -1 // ended by a signal
	if cmd.ProcessState != nil {
		code = cmd.ProcessState.ExitCode()
	}
	r.mu.Lock()
	if r.cmd == cmd {
		r.exitCode = &code
	}
	r.mu.Unlock()
	close(done)
}

// add stores one line of output for cmd's run.
func (r *Runner) add(cmd *exec.Cmd, stream, text string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cmd != cmd {
		return // output of an earlier run arriving late
	}
	r.lines = append(r.lines, Line{N: r.total, Stream: stream, Text: text})
	r.total++
	if len(r.lines) > maxLines {
		r.lines = r.lines[len(r.lines)-maxLines:]
	}
}

// maxLineBytes caps one stored line. Longer lines are cut, but the rest is
// still read, so the game never blocks on a full pipe.
const maxLineBytes = 64 * 1024

// lineWriter splits a stream into lines for the runner. os/exec calls Write
// from one goroutine per stream.
type lineWriter struct {
	r      *Runner
	cmd    *exec.Cmd
	stream string
	buf    []byte
	cut    int // bytes dropped from the current line
}

func (w *lineWriter) Write(p []byte) (int, error) {
	n := len(p)
	for len(p) > 0 {
		i := bytes.IndexByte(p, '\n')
		if i < 0 {
			w.take(p)
			break
		}
		w.take(p[:i])
		w.emit()
		p = p[i+1:]
	}
	return n, nil
}

func (w *lineWriter) take(b []byte) {
	room := maxLineBytes - len(w.buf)
	if len(b) > room {
		w.cut += len(b) - room
		b = b[:room]
	}
	w.buf = append(w.buf, b...)
}

func (w *lineWriter) emit() {
	text := strings.ToValidUTF8(godot.CleanLine(string(w.buf)), "\uFFFD")
	if w.cut > 0 {
		text += fmt.Sprintf(" [line cut: %d more bytes]", w.cut)
	}
	w.r.add(w.cmd, w.stream, text)
	w.buf, w.cut = w.buf[:0], 0
}

// flush emits a last line that did not end with a newline.
func (w *lineWriter) flush() {
	if len(w.buf) > 0 || w.cut > 0 {
		w.emit()
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

// Link returns the running game's bridge connection, or nil when no game
// runs.
func (r *Runner) Link() *Link {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cmd == nil || r.exitCode != nil {
		return nil
	}
	return r.link
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
		if r.link != nil {
			s.Bridge = "waiting"
			if r.link.Connected() {
				s.Bridge = "connected"
			}
		}
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

// Issues parses error blocks in lines one stream at a time. stdout and
// stderr are read concurrently, so in the merged order a line from one
// stream can land between an error and its "at:" line from the other.
// stderr comes first: Godot writes errors and warnings there.
func Issues(lines []Line) []godot.Issue {
	var out []godot.Issue
	for _, stream := range []string{"stderr", "stdout"} {
		var texts []string
		for _, l := range lines {
			if l.Stream == stream {
				texts = append(texts, l.Text)
			}
		}
		out = append(out, godot.ParseIssues(texts)...)
	}
	return out
}
