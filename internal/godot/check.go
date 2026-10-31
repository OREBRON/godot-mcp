package godot

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/orebron/godot-mcp/internal/project"
)

//go:embed check_scripts.gd
var checkScript []byte

const resultMarker = "GODOT_MCP_RESULT "

// ScriptResult is the outcome for one script.
type ScriptResult struct {
	Path   string  `json:"path"`
	OK     bool    `json:"ok"`
	Errors []Issue `json:"errors,omitempty"`
}

// CheckResult is the outcome of a script check run.
type CheckResult struct {
	OK      bool           `json:"ok"`
	Checked int            `json:"checked"`
	Failed  int            `json:"failed"`
	Scripts []ScriptResult `json:"scripts"`
	// Other holds errors in files that were not on the list, such as a
	// broken script that a checked script preloads.
	Other []Issue  `json:"other_errors,omitempty"`
	Notes []string `json:"notes,omitempty"`
}

// ListScripts returns every .gd file in the project as res:// paths. Hidden
// folders and folders marked with .gdignore are skipped, and so is addons/
// unless includeAddons is set.
func ListScripts(p *project.Project, includeAddons bool) ([]string, error) {
	var out []string
	err := filepath.WalkDir(p.Dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // unreadable folders are skipped, not fatal
		}
		if d.IsDir() {
			name := d.Name()
			if path != p.Dir && (strings.HasPrefix(name, ".") || isFile(filepath.Join(path, ".gdignore"))) {
				return filepath.SkipDir
			}
			if !includeAddons && path == filepath.Join(p.Dir, "addons") {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.EqualFold(filepath.Ext(path), ".gd") {
			res, err := p.ToRes(path)
			if err == nil {
				out = append(out, res)
			}
		}
		return nil
	})
	sort.Strings(out)
	return out, err
}

// ClassCacheStale reports whether a script with class_name changed after
// Godot last wrote its global class cache. Without a fresh cache, scripts
// that use those class names fail to parse with "Identifier not declared".
func ClassCacheStale(p *project.Project) bool {
	cache, err := os.Stat(filepath.Join(p.DataDir(), "global_script_class_cache.cfg"))
	if err != nil {
		return true
	}
	stale := false
	scripts, _ := ListScripts(p, true)
	for _, res := range scripts {
		osPath, err := p.ToOS(res)
		if err != nil {
			continue
		}
		fi, err := os.Stat(osPath)
		if err != nil || !fi.ModTime().After(cache.ModTime()) {
			continue
		}
		if b, err := os.ReadFile(osPath); err == nil && bytes.Contains(b, []byte("class_name")) {
			stale = true
			break
		}
	}
	return stale
}

// Import runs `godot --headless --import`, which refreshes the class cache
// and imports new assets. Do not call it while an editor has the project open;
// ask that editor to rescan instead.
func (e Exec) Import(ctx context.Context, p *project.Project) error {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	out, err := e.Command(ctx, "--headless", "--path", p.Dir, "--import").CombinedOutput()
	if ctx.Err() != nil {
		return errors.New("godot --import did not finish within 3 minutes")
	}
	if err != nil {
		return fmt.Errorf("godot --import failed: %v\n%s", err, tail(string(out), 20))
	}
	return nil
}

// CheckScripts loads each script in a headless Godot and collects the parse
// errors. Godot exits with 0 even when a script fails to parse, so the result
// comes from the printed errors, never from the exit code.
func (e Exec) CheckScripts(ctx context.Context, p *project.Project, scripts []string) (CheckResult, error) {
	if len(scripts) == 0 {
		return CheckResult{OK: true, Notes: []string{"no .gd files to check"}}, nil
	}
	if err := os.MkdirAll(p.MCPDir(), 0o755); err != nil {
		return CheckResult{}, err
	}
	// Each run gets its own folder: several agent sessions may check
	// scripts in the same project at once.
	dir, err := os.MkdirTemp(p.MCPDir(), "check-")
	if err != nil {
		return CheckResult{}, err
	}
	defer os.RemoveAll(dir)
	runner := filepath.Join(dir, "check_scripts.gd")
	if err := os.WriteFile(runner, checkScript, 0o644); err != nil {
		return CheckResult{}, err
	}
	// The list goes in a file: Windows caps a command line at 32k characters.
	list := filepath.Join(dir, "check_list.txt")
	if err := os.WriteFile(list, []byte(strings.Join(scripts, "\n")+"\n"), 0o644); err != nil {
		return CheckResult{}, err
	}
	runnerRes, err := p.ToRes(runner)
	if err != nil {
		return CheckResult{}, err
	}

	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	out, runErr := e.Command(ctx, "--headless", "--path", p.Dir, "--script", runnerRes, "--", list).CombinedOutput()
	if ctx.Err() != nil {
		return CheckResult{}, errors.New("the script check did not finish within 2 minutes; a script may run code at load time that never returns (static initialisers, @tool scripts)")
	}

	lines := strings.Split(strings.ReplaceAll(string(out), "\r\n", "\n"), "\n")
	var loaded []struct {
		Path   string `json:"path"`
		Loaded bool   `json:"loaded"`
	}
	found := false
	for _, l := range lines {
		l = CleanLine(l)
		if strings.HasPrefix(l, resultMarker) {
			found = json.Unmarshal([]byte(strings.TrimPrefix(l, resultMarker)), &loaded) == nil
		}
	}
	if !found {
		msg := "the script checker did not report a result"
		if runErr != nil {
			msg += ": " + runErr.Error()
		}
		return CheckResult{}, fmt.Errorf("%s\nLast output:\n%s", msg, tail(string(out), 20))
	}

	listed := map[string]bool{}
	for _, s := range scripts {
		listed[s] = true
	}
	reported := map[string]bool{}
	for _, l := range loaded {
		reported[l.Path] = true
	}
	if len(reported) != len(listed) || len(loaded) != len(listed) {
		return CheckResult{}, fmt.Errorf("the checker reported %d files but %d were requested; run script_check again", len(loaded), len(listed))
	}
	for s := range listed {
		if !reported[s] {
			return CheckResult{}, fmt.Errorf("the checker did not report %s; run script_check again", s)
		}
	}

	byFile := map[string][]Issue{}
	var other []Issue
	for _, is := range ParseIssues(lines) {
		if is.Severity != "error" {
			continue
		}
		// "Failed to load script" repeats what the parse errors already said.
		if strings.HasPrefix(is.Message, "Failed to load script") {
			continue
		}
		if listed[is.File] {
			byFile[is.File] = append(byFile[is.File], is)
		} else if is.File != "" && !strings.HasPrefix(is.File, runnerRes) {
			other = append(other, is)
		}
	}

	res := CheckResult{OK: true, Checked: len(loaded), Other: other}
	for _, l := range loaded {
		sr := ScriptResult{Path: l.Path, OK: l.Loaded && len(byFile[l.Path]) == 0, Errors: byFile[l.Path]}
		if !l.Loaded && len(sr.Errors) == 0 {
			sr.Errors = []Issue{{Severity: "error", Kind: "ERROR", Message: "the script could not be loaded (missing file or unsupported type)", File: l.Path}}
		}
		if !sr.OK {
			res.OK = false
			res.Failed++
		}
		res.Scripts = append(res.Scripts, sr)
	}
	if len(other) > 0 {
		res.OK = false
	}
	return res, nil
}

func tail(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
