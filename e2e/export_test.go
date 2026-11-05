package e2e

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// Three presets: Linux with the official templates, Web without an export
// path, and Linux with a stand-in template (%[1]s) so that an export can
// succeed on a machine without templates.
const exportPresets = `[preset.0]

name="Linux"
platform="Linux"
export_filter="all_resources"
include_filter=""
exclude_filter=""
export_path="build/game.x86_64"

[preset.0.options]

binary_format/architecture="x86_64"

[preset.1]

name="Web"
platform="Web"
export_filter="all_resources"
include_filter=""
exclude_filter=""
export_path=""

[preset.1.options]

variant/extensions_support=false

[preset.2]

name="Stand-in"
platform="Linux"
export_filter="all_resources"
include_filter=""
exclude_filter=""
export_path="build/standin.x86_64"

[preset.2.options]

binary_format/architecture="x86_64"
custom_template/debug="%[1]s"
custom_template/release="%[1]s"
`

// standInTemplate writes the smallest file Godot 4.7 takes as a Linux x86_64
// template: an ELF header naming the architecture. The export copies it and
// writes the .pck beside it.
func standInTemplate(t *testing.T) string {
	h := make([]byte, 64)
	copy(h, "\x7fELF\x02\x01\x01")
	h[18] = 0x3e // e_machine: x86_64
	path := filepath.Join(t.TempDir(), "linux_template.x86_64")
	if err := os.WriteFile(path, h, 0o755); err != nil {
		t.Fatal(err)
	}
	return filepath.ToSlash(path)
}

// templatesFolder is where this Godot looks for its export templates, such
// as .../export_templates/4.7.2.stable.
func templatesFolder(t *testing.T, s *session) string {
	t.Helper()
	v, err := findGodot(t).GetVersion(s.ctx)
	if err != nil {
		t.Fatal(err)
	}
	// "4.7.2.stable.official.ed1daf0bf" -> "4.7.2.stable": up to the status.
	parts := strings.Split(v.Raw, ".")
	n := 0
	for n < len(parts)-1 && strings.Trim(parts[n], "0123456789") == "" {
		n++
	}
	base, _ := os.UserConfigDir() // macOS: ~/Library/Application Support, Windows: %APPDATA%
	base = filepath.Join(base, "Godot")
	if runtime.GOOS == "linux" {
		base = filepath.Join(os.Getenv("HOME"), ".local", "share", "godot")
		if xdg := os.Getenv("XDG_DATA_HOME"); xdg != "" {
			base = filepath.Join(xdg, "godot")
		}
	}
	return filepath.Join(base, "export_templates", strings.Join(parts[:n+1], "."))
}

func TestExportBuild(t *testing.T) {
	s := start(t, true)
	s.fails("export_build", nil, "NO_EXPORT_PRESETS")

	write(t, filepath.Join(s.proj, "export_presets.cfg"), fmt.Sprintf(exportPresets, standInTemplate(t)))
	names := `the presets are "Linux", "Web", "Stand-in"`
	s.fails("export_build", nil, names)
	s.fails("export_build", map[string]any{"preset": "Mac"}, names)
	s.fails("export_build", map[string]any{"preset": "Web"}, "has no export path")
	s.fails("export_build", map[string]any{"preset": "Linux", "output": "../game.x86_64"}, "outside the project")
	s.fails("export_build", map[string]any{"preset": "Linux", "output": `build\..\..\game.x86_64`}, "outside the project")
	s.fails("export_build", map[string]any{"preset": "Linux", "output": "demo/main.tscn"}, "must not overwrite")
	write(t, filepath.Join(s.proj, "art", "icon.png"), "png")
	s.fails("export_build", map[string]any{"preset": "Linux", "output": "art/icon.png"}, "must not overwrite")
	os.Remove(filepath.Join(s.proj, "art", "icon.png")) // not a real image; the next export would report it
	// Libraries go beside the game, so a link there must not lead out.
	os.MkdirAll(filepath.Join(s.proj, "linked"), 0o755)
	if os.Symlink(t.TempDir(), filepath.Join(s.proj, "linked", "lib")) == nil {
		s.fails("export_build", map[string]any{"preset": "Linux", "output": "linked/game.x86_64"}, "points outside the project")
	}

	r := s.ok("export_build", map[string]any{"preset": "Stand-in", "output": "res://build/sub/game.x86_64", "debug": true})
	if r["ok"] != true || r["output"] != "res://build/sub/game.x86_64" || r["platform"] != "Linux" || r["size_bytes"] != 64.0 || r["exit_code"] != 0.0 || r["errors"] != nil {
		t.Errorf("stand-in export = %v", r)
	}
	if _, err := os.Stat(filepath.Join(s.proj, "build", "sub", "game.pck")); err != nil {
		t.Errorf("the export wrote no .pck: %v", err)
	}

	folder := templatesFolder(t, s)
	if _, err := os.Stat(filepath.Join(folder, "linux_release.x86_64")); err == nil {
		if r := s.ok("export_build", map[string]any{"preset": "Linux"}); r["ok"] != true || r["output"] != "res://build/game.x86_64" {
			t.Errorf("export with the installed templates = %v", r)
		}
		return
	}
	text, isErr := s.call("export_build", map[string]any{"preset": "Linux"})
	for _, want := range []string{"EXPORT_TEMPLATES_MISSING", "linux_release.x86_64", "Manage Export Templates", folder} {
		if !isErr || !strings.Contains(text, want) {
			t.Errorf("export without templates: want an error containing %q, got isError=%v %s", want, isErr, text)
		}
	}
}
