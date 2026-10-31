package project

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newProject(t *testing.T, godotFile string) *Project {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "project.godot"), []byte(godotFile), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoad(t *testing.T) {
	p := newProject(t, `; comment
config_version=5

[application]

config/name="My \"Game\""
run/main_scene="uid://b2x3"
config/use_hidden_project_data_directory=false

[autoload]

Game="*res://autoload/game.gd"
Audio="res://autoload/audio.gd"
`)
	if p.Name != `My "Game"` || p.MainScene != "uid://b2x3" {
		t.Errorf("name %q, main scene %q", p.Name, p.MainScene)
	}
	if p.Autoloads["Game"] != "res://autoload/game.gd" || p.Autoloads["Audio"] != "res://autoload/audio.gd" {
		t.Errorf("autoloads %v", p.Autoloads)
	}
	if p.DataDir() != filepath.Join(p.Dir, "godot") {
		t.Errorf("data dir %s, want the non-hidden one", p.DataDir())
	}
}

func TestFind(t *testing.T) {
	p := newProject(t, "config_version=5\n")
	deep := filepath.Join(p.Dir, "a", "b")
	os.MkdirAll(deep, 0o755)
	got, err := Find(deep)
	if err != nil || got != p.Dir {
		t.Errorf("Find = %q, %v; want %q", got, err, p.Dir)
	}
}

func TestToRes(t *testing.T) {
	p := newProject(t, "config_version=5\n")
	ok := map[string]string{
		"res://scenes/main.tscn":                      "res://scenes/main.tscn",
		"res://scenes/../main.tscn":                   "res://main.tscn",
		"scenes/main.tscn":                            "res://scenes/main.tscn",
		filepath.Join(p.Dir, "scenes", "player.tscn"): "res://scenes/player.tscn",
		"uid://abc123":                                "uid://abc123",
		`scenes\win.tscn`:                             "res://scenes/win.tscn",
		`res://a\..\b.gd`:                             "res://b.gd",
	}
	for in, want := range ok {
		got, err := p.ToRes(in)
		if err != nil || got != want {
			t.Errorf("ToRes(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	// Godot reads "\" as a separator on every OS, so these leave the project.
	for _, bad := range []string{"res://../secret.txt", "../outside.gd", `res://..\outside.tscn`, `..\outside.gd`, "res:///etc/passwd", filepath.Join(filepath.Dir(p.Dir), "x.gd"), ""} {
		if got, err := p.ToRes(bad); err == nil {
			t.Errorf("ToRes(%q) = %q, want an error", bad, got)
		}
	}
	osPath, err := p.ToOS("res://a/b.gd")
	if err != nil || osPath != filepath.Join(p.Dir, "a", "b.gd") {
		t.Errorf("ToOS = %q, %v", osPath, err)
	}
}

func TestToResRejectsLinksOutside(t *testing.T) {
	p := newProject(t, "config_version=5\n")
	outside := t.TempDir()
	os.WriteFile(filepath.Join(outside, "secret.gd"), nil, 0o644)
	if err := os.Symlink(outside, filepath.Join(p.Dir, "escape")); err != nil {
		t.Skip("cannot create symbolic links here:", err)
	}
	os.MkdirAll(filepath.Join(p.Dir, "real"), 0o755)
	os.Symlink(filepath.Join(p.Dir, "real"), filepath.Join(p.Dir, "inside"))

	if got, err := p.ToRes("res://escape/secret.gd"); err == nil || !strings.Contains(err.Error(), "outside the project") {
		t.Errorf("link to outside: ToRes = %q, %v; want an error", got, err)
	}
	if got, err := p.ToRes("res://inside/new.gd"); err != nil || got != "res://inside/new.gd" {
		t.Errorf("link inside the project: ToRes = %q, %v", got, err)
	}
}

func TestCheckCase(t *testing.T) {
	p := newProject(t, "config_version=5\n")
	os.MkdirAll(filepath.Join(p.Dir, "Scenes"), 0o755)
	os.WriteFile(filepath.Join(p.Dir, "Scenes", "Main.tscn"), nil, 0o644)

	if err := p.CheckCase("res://Scenes/Main.tscn"); err != nil {
		t.Errorf("exact case: %v", err)
	}
	err := p.CheckCase("res://scenes/main.tscn")
	if err == nil || !strings.Contains(err.Error(), "letter case") {
		t.Errorf("wrong case: got %v, want a letter case error", err)
	}
	if err := p.CheckCase("res://Scenes/Nope.tscn"); err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Errorf("missing file: %v", err)
	}
}
