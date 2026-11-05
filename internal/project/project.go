// Package project finds a Godot project on disk, reads project.godot and
// converts between res:// paths and OS paths. All path handling goes through
// here so that separators, case and confinement rules live in one place.
package project

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Project is a Godot project rooted at Dir (the folder holding project.godot).
type Project struct {
	Dir        string
	Name       string
	MainScene  string
	Autoloads  map[string]string // name -> res:// path (leading "*" removed)
	hiddenData bool
}

// ErrNotFound is returned when no project.godot exists at or above a directory.
var ErrNotFound = errors.New("no project.godot found")

// Find walks up from start until it finds a folder holding project.godot.
func Find(start string) (string, error) {
	dir, err := filepath.Abs(start)
	if err != nil {
		return "", err
	}
	for {
		if fi, err := os.Stat(filepath.Join(dir, "project.godot")); err == nil && !fi.IsDir() {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("%w at or above %s", ErrNotFound, start)
		}
		dir = parent
	}
}

// Load reads project.godot in dir.
func Load(dir string) (*Project, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	p := &Project{Dir: abs, Autoloads: map[string]string{}, hiddenData: true}
	if err := scanCfg(filepath.Join(abs, "project.godot"), p.apply); err != nil {
		return nil, err
	}
	return p, nil
}

// scanCfg calls fn for every key in a Godot .cfg-style file, with its
// section and unquoted value.
// shortcut: a multi-line string value is read line by line; fine while only
// single-line keys are wanted.
func scanCfg(path string, fn func(section, key, val string)) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	section := ""
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = line[1 : len(line)-1]
		} else if key, raw, ok := strings.Cut(line, "="); ok && !strings.HasPrefix(line, ";") {
			fn(section, strings.TrimSpace(key), unquote(strings.TrimSpace(raw)))
		}
	}
	return sc.Err()
}

// ExportPreset is one [preset.N] section of export_presets.cfg.
type ExportPreset struct {
	Name       string `json:"name"`
	Platform   string `json:"platform"`
	ExportPath string `json:"export_path,omitempty"`
}

// ExportPresets reads export_presets.cfg, in file order. A missing file
// gives an error that wraps os.ErrNotExist.
func (p *Project) ExportPresets() ([]ExportPreset, error) {
	var out []ExportPreset
	last := ""
	err := scanCfg(filepath.Join(p.Dir, "export_presets.cfg"), func(section, key, val string) {
		// [preset.N] holds the preset itself; [preset.N.options] its options.
		if n, ok := strings.CutPrefix(section, "preset."); !ok || strings.Contains(n, ".") {
			return
		}
		if section != last {
			out, last = append(out, ExportPreset{}), section
		}
		switch pr := &out[len(out)-1]; key {
		case "name":
			pr.Name = val
		case "platform":
			pr.Platform = val
		case "export_path":
			pr.ExportPath = val
		}
	})
	return out, err
}

// apply records the project.godot settings the server uses.
func (p *Project) apply(section, key, val string) {
	if section == "autoload" {
		p.Autoloads[key] = strings.TrimPrefix(val, "*")
		return
	}
	if section != "application" {
		return
	}
	switch key {
	case "config/name":
		p.Name = val
	case "run/main_scene":
		p.MainScene = val
	case "config/use_hidden_project_data_directory":
		p.hiddenData = val != "false"
	}
}

func unquote(v string) string {
	if len(v) >= 2 && v[0] == '"' && v[len(v)-1] == '"' {
		if s, err := strconv.Unquote(v); err == nil {
			return s
		}
		return v[1 : len(v)-1]
	}
	return v
}

// DataDir is the project's editor data folder (.godot by default).
func (p *Project) DataDir() string {
	if p.hiddenData {
		return filepath.Join(p.Dir, ".godot")
	}
	return filepath.Join(p.Dir, "godot")
}

// MCPDir is where the plugin and the server exchange files (bridge.json,
// helper scripts). It sits inside the data folder, which Godot never imports
// and which the default .gitignore excludes.
func (p *Project) MCPDir() string {
	return filepath.Join(p.DataDir(), "godot_mcp")
}

// ToRes converts a res:// path, a project-relative path or an absolute OS
// path inside the project into a clean res:// path. Paths that leave the
// project, directly or through a symbolic link, are rejected.
func (p *Project) ToRes(path string) (string, error) {
	if path == "" {
		return "", errors.New("empty path")
	}
	if strings.HasPrefix(path, "uid://") {
		return path, nil
	}
	var rel string
	if strings.HasPrefix(path, "res://") {
		rel = strings.TrimPrefix(path, "res://")
	} else if filepath.IsAbs(path) {
		r, err := filepath.Rel(p.Dir, filepath.Clean(path))
		if err != nil {
			return "", fmt.Errorf("%s is not inside the project %s", path, p.Dir)
		}
		rel = r
	} else {
		rel = path
	}
	// Godot treats "\" as a separator on every OS, so "..\" must count as a
	// parent step on Linux and macOS too.
	rel = strings.ReplaceAll(rel, `\`, "/")
	rel = filepath.ToSlash(filepath.Clean(filepath.FromSlash(rel)))
	if rel == "." {
		rel = ""
	}
	if rel == ".." || strings.HasPrefix(rel, "../") || strings.HasPrefix(rel, "/") {
		return "", fmt.Errorf("%s is outside the project folder %s", path, p.Dir)
	}
	if err := p.checkLinks(rel); err != nil {
		return "", fmt.Errorf("%s: %w", path, err)
	}
	return "res://" + rel, nil
}

// checkLinks rejects a project-relative path whose existing part passes
// through a symbolic link (or Windows junction) that points outside the
// project.
func (p *Project) checkLinks(rel string) error {
	if rel == "" {
		return nil
	}
	root, err := filepath.EvalSymlinks(p.Dir)
	if err != nil {
		return err
	}
	cur := p.Dir
	for _, part := range strings.Split(rel, "/") {
		cur = filepath.Join(cur, part)
		fi, err := os.Lstat(cur)
		if err != nil {
			return nil // the rest does not exist yet, so it holds no links
		}
		if fi.Mode()&(os.ModeSymlink|os.ModeIrregular) == 0 {
			continue
		}
		target, err := filepath.EvalSymlinks(cur)
		if err != nil {
			return fmt.Errorf("the link %s cannot be resolved: %v", cur, err)
		}
		if r, err := filepath.Rel(root, target); err != nil || r == ".." || strings.HasPrefix(r, ".."+string(filepath.Separator)) {
			return fmt.Errorf("the link %s points outside the project, to %s", cur, target)
		}
	}
	return nil
}

// ToOS converts a res:// path to an absolute OS path.
func (p *Project) ToOS(res string) (string, error) {
	r, err := p.ToRes(res)
	if err != nil {
		return "", err
	}
	if strings.HasPrefix(r, "uid://") {
		return "", fmt.Errorf("%s is a UID, not a file path", r)
	}
	return filepath.Join(p.Dir, filepath.FromSlash(strings.TrimPrefix(r, "res://"))), nil
}

// CheckCase reports an error when the on-disk spelling of res differs from
// the requested one. macOS and Windows accept the wrong case, but Linux and
// exported .pck files do not, so a wrong-cased path is a bug that only shows
// up later on another machine.
func (p *Project) CheckCase(res string) error {
	return p.checkCase(res, true)
}

// CheckCaseExisting is CheckCase for a path that may not exist yet, such as
// a scene about to be created: it checks the folders and file that do exist.
func (p *Project) CheckCaseExisting(res string) error {
	return p.checkCase(res, false)
}

func (p *Project) checkCase(res string, mustExist bool) error {
	r, err := p.ToRes(res)
	if err != nil {
		return err
	}
	cur := p.Dir
	for _, part := range strings.Split(strings.TrimPrefix(r, "res://"), "/") {
		if part == "" {
			continue
		}
		found, err := spelling(cur, part)
		if err != nil && (mustExist || !errors.Is(err, os.ErrNotExist)) {
			return err
		}
		if found == "" && !mustExist {
			return nil // the rest does not exist yet
		}
		if found == "" {
			return fmt.Errorf("%s does not exist", r)
		}
		if found != part {
			return fmt.Errorf("%s has the wrong letter case: the file on disk is spelled %q; Linux and exported games are case-sensitive", r, found)
		}
		cur = filepath.Join(cur, found)
	}
	return nil
}

// spelling returns how name is spelled in dir: name itself, an entry that
// differs only in letter case, or "" when there is none.
func spelling(dir, name string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	found := ""
	for _, e := range entries {
		if e.Name() == name {
			return name, nil
		}
		if found == "" && strings.EqualFold(e.Name(), name) {
			found = e.Name()
		}
	}
	return found, nil
}
