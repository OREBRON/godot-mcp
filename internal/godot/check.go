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
	dir, runnerRes, list, err := writeCheckInputs(p, scripts)
	if err != nil {
		return CheckResult{}, err
	}
	defer os.RemoveAll(dir)

	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	out, runErr := e.Command(ctx, "--headless", "--path", p.Dir, "--script", runnerRes, "--", list).CombinedOutput()
	if ctx.Err() != nil {
		return CheckResult{}, errors.New("the script check did not finish within 2 minutes; a script may run code at load time that never returns (static initialisers, @tool scripts)")
	}
	lines := strings.Split(strings.ReplaceAll(string(out), "\r\n", "\n"), "\n")
	loaded, err := readLoaded(lines, scripts)
	if err != nil && runErr != nil {
		err = fmt.Errorf("%w (godot: %v)", err, runErr)
	}
	if err != nil {
		return CheckResult{}, fmt.Errorf("%w\nLast output:\n%s", err, tail(string(out), 20))
	}
	byFile, other := attributeIssues(ParseIssues(lines), scripts, strings.TrimSuffix(runnerRes, "check_scripts.gd"))
	return buildResult(loaded, byFile, other), nil
}

// writeCheckInputs writes the checker and the list of scripts into a new
// folder of their own, since several agent sessions may check scripts in the
// same project at once. The caller removes dir.
func writeCheckInputs(p *project.Project, scripts []string) (dir, runnerRes, list string, err error) {
	// Private like the plugin keeps it: bridge.json, with the token, lives here.
	if err = os.MkdirAll(p.MCPDir(), 0o700); err != nil {
		return
	}
	if dir, err = os.MkdirTemp(p.MCPDir(), "check-"); err != nil {
		return
	}
	runner := filepath.Join(dir, "check_scripts.gd")
	// The list goes in a file: Windows caps a command line at 32k characters.
	list = filepath.Join(dir, "check_list.txt")
	if err = os.WriteFile(runner, checkScript, 0o644); err == nil {
		err = os.WriteFile(list, []byte(strings.Join(scripts, "\n")+"\n"), 0o644)
	}
	if err == nil {
		runnerRes, err = p.ToRes(runner)
	}
	if err != nil {
		os.RemoveAll(dir)
	}
	return
}

type loadedScript struct {
	Path   string `json:"path"`
	Loaded bool   `json:"loaded"`
}

// readLoaded reads the checker's result line and makes sure it covers
// exactly the requested scripts.
func readLoaded(lines, scripts []string) ([]loadedScript, error) {
	var loaded []loadedScript
	found := false
	for _, l := range lines {
		if rest, ok := strings.CutPrefix(CleanLine(l), resultMarker); ok {
			found = json.Unmarshal([]byte(rest), &loaded) == nil
		}
	}
	if !found {
		return nil, errors.New("the script checker did not report a result")
	}
	reported := map[string]bool{}
	for _, l := range loaded {
		reported[l.Path] = true
	}
	if len(reported) != len(scripts) || len(loaded) != len(scripts) {
		return nil, fmt.Errorf("the checker reported %d files but %d were requested; run script_check again", len(loaded), len(scripts))
	}
	for _, s := range scripts {
		if !reported[s] {
			return nil, fmt.Errorf("the checker did not report %s; run script_check again", s)
		}
	}
	return loaded, nil
}

// attributeIssues ties each error to a checked script. Godot returns a
// script object even when parsing failed, so "loaded" alone proves nothing:
// an error that cannot be tied to a checked file goes to other, which fails
// the check, instead of vanishing.
func attributeIssues(issues []Issue, scripts []string, runnerDir string) (byFile map[string][]Issue, other []Issue) {
	listed := map[string]bool{}
	for _, s := range scripts {
		listed[s] = true
	}
	byFile = map[string][]Issue{}
	failedLoad := map[string]Issue{}
	for _, is := range issues {
		switch issueKind(is, listed, runnerDir) {
		case kindFailedLoad:
			failedLoad[is.File] = is
		case kindFile:
			byFile[is.File] = append(byFile[is.File], is)
		case kindOther:
			other = append(other, is)
		}
	}
	keepLoneFailures(byFile, failedLoad)
	return byFile, other
}

// keepLoneFailures adds a file's "Failed to load script" line only when the
// file got no other error; usually it repeats the parse errors above it.
func keepLoneFailures(byFile map[string][]Issue, failedLoad map[string]Issue) {
	for file, is := range failedLoad {
		if len(byFile[file]) == 0 {
			byFile[file] = []Issue{is}
		}
	}
}

const (
	kindSkip       = iota // warnings, and the checker's own errors
	kindFailedLoad        // Godot's "Failed to load script" summary
	kindFile              // an error in a checked script
	kindOther             // an error that names no checked script
)

func issueKind(is Issue, listed map[string]bool, runnerDir string) int {
	switch {
	case is.Severity != "error" || strings.HasPrefix(is.File, runnerDir):
		return kindSkip
	case !listed[is.File]:
		return kindOther
	case strings.HasPrefix(is.Message, "Failed to load script"):
		return kindFailedLoad
	}
	return kindFile
}

func buildResult(loaded []loadedScript, byFile map[string][]Issue, other []Issue) CheckResult {
	res := CheckResult{OK: len(other) == 0, Checked: len(loaded), Other: other}
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
	return res
}

func tail(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
