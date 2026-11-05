package godot

import (
	"context"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
	"time"

	"github.com/orebron/godot-mcp/internal/project"
)

// ExportRun is what an export printed and how it exited. Whether it wrote
// anything is for the caller to check: Godot can exit with 0 without
// writing the file.
type ExportRun struct {
	ExitCode int
	Output   string
	Tail     string  // the last 30 lines of Output
	Errors   []Issue // the first 20
}

// Export runs `godot --headless --export-release|--export-debug <preset>
// <out>`. A run that outlives timeout is stopped with its whole process tree.
func (e Exec) Export(ctx context.Context, p *project.Project, preset, out string, debug bool, timeout time.Duration) (ExportRun, error) {
	mode := "--export-release"
	if debug {
		mode = "--export-debug"
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := e.Command(ctx, "--headless", "--path", p.Dir, mode, preset, out)
	b, _ := cmd.CombinedOutput()
	if ctx.Err() != nil {
		return ExportRun{}, fmt.Errorf("the export did not finish within %s; pass a longer timeout_seconds for a large project", timeout)
	}
	text := strings.ReplaceAll(string(b), "\r\n", "\n")
	return ExportRun{
		ExitCode: cmd.ProcessState.ExitCode(),
		Output:   text,
		Tail:     tail(text, 30),
		Errors:   first(errorsOnly(ParseIssues(strings.Split(text, "\n"))), 20),
	}, nil
}

// MissingTemplates returns the export template files Godot could not find,
// from lines such as
//
//	No export template found at the expected path:
//	/home/me/.local/share/godot/export_templates/4.7.2.stable/linux_release.x86_64
func MissingTemplates(out string) []string {
	var paths []string
	lines := strings.Split(out, "\n")
	for i := 0; i+1 < len(lines); i++ {
		if strings.Contains(lines[i], "No export template found") {
			paths = append(paths, strings.TrimSpace(CleanLine(lines[i+1])))
		}
	}
	return paths
}

// FileState is an export's output on disk: a file, or a folder such as a
// macOS .app, with its total size and newest modification time.
type FileState struct {
	Exists  bool
	Size    int64
	ModTime time.Time
}

// OutputStat reads the state of the file or folder at path.
func OutputStat(path string) FileState {
	var st FileState
	filepath.WalkDir(path, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		fi, err := d.Info()
		if err != nil {
			return nil
		}
		st.Exists = true
		if !d.IsDir() {
			st.Size += fi.Size()
		}
		if fi.ModTime().After(st.ModTime) {
			st.ModTime = fi.ModTime()
		}
		return nil
	})
	return st
}

// WrittenSince reports whether the output exists and differs from before,
// its state before the export ran. The exit code alone proves nothing.
func (st FileState) WrittenSince(before FileState) bool {
	return st.Exists && (!before.Exists || !st.ModTime.Equal(before.ModTime) || st.Size != before.Size)
}
