package project

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// findProject writes files (project-relative path -> content) into a new
// project.
func findProject(t *testing.T, files map[string]string) *Project {
	t.Helper()
	p := newProject(t, files["project.godot"])
	for name, body := range files {
		osPath := filepath.Join(p.Dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(osPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(osPath, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return p
}

var findFiles = map[string]string{
	"project.godot": "[application]\n\nrun/main_scene=\"res://scenes/main.tscn\"\n\n[autoload]\n\nGame=\"*res://scripts/game.gd\"\n",
	"scenes/player.tscn": `[gd_scene load_steps=3 format=3 uid="uid://pl"]

[ext_resource type="Script" uid="uid://hero" path="res://scripts/hero.gd" id="1_a"]

[node name="Player" type="CharacterBody2D"]
script = ExtResource("1_a")

[node name="Shape" type="CollisionShape2D" parent="."]

[node name="Hitbox" type="Area2D" parent="."]

[node name="Shape" type="CollisionShape2D" parent="Hitbox"]
`,
	"scenes/main.tscn": `[gd_scene load_steps=2 format=3 uid="uid://main"]

[ext_resource type="PackedScene" uid="uid://pl" path="res://scenes/player.tscn" id="2_b"]

[node name="Main" type="Node2D"]

[node name="World" type="Node2D" parent="."]

[node name="Hero" parent="World" instance=ExtResource("2_b")]

[node name="Zone" type="Area2D" parent="World/Hero"]
`,
	"scripts/hero.gd":     "class_name Hero\nextends CharacterBody2D\n",
	"scripts/hero.gd.uid": "uid://hero\n",
	"scripts/game.gd": `extends Node
# Hero is spawned below.
const PLAYER = preload("res://scenes/player.tscn")
var by_uid = load("uid://pl")
var hero: Hero
var heroes: HeroList
var other = "res://scenes/player.tscn.bak"
`,
	"addons/tool/use.gd":    "var s = load('res://scenes/player.tscn')\n",
	"scenes/spawner.gd":     "const P = preload(\"player.tscn\")\nvar q = load(\"./player.tscn\")\nvar label = \"player.tscn\"\n",
	"scripts/Spawn.cs":      "var s = GD.Load<PackedScene>(\"res://scenes/player.tscn\");\nvar r = \"../scenes/player.tscn\";\n",
	"scripts/up.gd":         "var u = load(\"../scenes/player.tscn\")\n",
	"ignored/.gdignore":     "",
	"ignored/use.gd":        "var s = load(\"res://scenes/player.tscn\")\n",
	".godot/cache.tres":     "path = \"res://scenes/player.tscn\"\n",
	"art/level.scn":         "RSRC binary",
	"art/icon.png.import":   "[remap]\n\nimporter=\"texture\"\nuid=\"uid://icon\"\n",
	"art/icon.png":          "png",
	"materials/glow.tres":   "[gd_resource type=\"Material\" format=3]\n\n[resource]\nnext = \"res://scenes/player.tscn\"\n",
	"scenes/inherited.tscn": "[gd_scene format=3]\n\n[ext_resource type=\"PackedScene\" path=\"res://scenes/main.tscn\" id=\"1\"]\n\n[node name=\"Inherited\" instance=ExtResource(\"1\")]\n",
}

func refLines(refs []Ref) []string {
	var out []string
	for _, r := range refs {
		out = append(out, fmt.Sprintf("%s:%d %s", strings.TrimPrefix(r.File, "res://"), r.Line, r.Kind))
	}
	return out
}

func TestFindRefs(t *testing.T) {
	p := findProject(t, findFiles)
	got, err := p.FindRefs("scenes/player.tscn")
	if err != nil {
		t.Fatal(err)
	}
	// Scripts may name it relative to their folder; C# files count too.
	want := []string{"addons/tool/use.gd:1 load", "materials/glow.tres:4 string", "scenes/main.tscn:3 ext_resource",
		"scenes/spawner.gd:1 preload", "scenes/spawner.gd:2 load", "scripts/Spawn.cs:1 string", "scripts/game.gd:3 preload", "scripts/game.gd:4 load", "scripts/up.gd:1 load"}
	if got.Path != "res://scenes/player.tscn" || got.UID != "uid://pl" || !slices.Equal(refLines(got.References), want) || got.Truncated {
		t.Errorf("FindRefs(player.tscn) = %+v, refs %v, want %v", got, refLines(got.References), want)
	}
	if len(got.Notes) != 1 || !strings.Contains(got.Notes[0], "1 binary") {
		t.Errorf("notes = %v, want one binary file skipped", got.Notes)
	}

	// A uid from a .uid file; a class_name script is also used by its name.
	got, err = p.FindRefs("uid://hero")
	if err != nil {
		t.Fatal(err)
	}
	want = []string{"scenes/player.tscn:3 ext_resource", "scripts/game.gd:5 class"}
	if got.Path != "res://scripts/hero.gd" || got.ClassName != "Hero" || !slices.Equal(refLines(got.References), want) {
		t.Errorf("FindRefs(uid://hero) = %+v, refs %v, want %v", got, refLines(got.References), want)
	}

	got, err = p.FindRefs("res://scripts/game.gd")
	if err != nil || !slices.Equal(refLines(got.References), []string{"project.godot:7 autoload"}) {
		t.Errorf("FindRefs(game.gd) = %v, %v", refLines(got.References), err)
	}
	// An empty answer says what it cannot see.
	if got, err := p.FindRefs("res://scripts/up.gd"); err != nil || len(got.References) != 0 || !strings.Contains(strings.Join(got.Notes, " "), "runtime") {
		t.Errorf("FindRefs(up.gd) = %+v, %v; want no references and a note", got, err)
	}
	got, err = p.FindRefs("res://scenes/main.tscn")
	if err != nil || !slices.Equal(refLines(got.References), []string{"project.godot:3 setting", "scenes/inherited.tscn:3 ext_resource"}) {
		t.Errorf("FindRefs(main.tscn) = %v, %v", refLines(got.References), err)
	}
	if got, err := p.FindRefs("art/icon.png"); err != nil || got.UID != "uid://icon" {
		t.Errorf("FindRefs(icon.png) = %+v, %v; want the uid from its .import file", got, err)
	}
}

func TestFindRefsMissing(t *testing.T) {
	p := findProject(t, findFiles)
	var m *MissingError
	if _, err := p.FindRefs("res://scenes/plyer.tscn"); !errors.As(err, &m) || !slices.Equal(m.Similar, []string{"res://scenes/player.tscn"}) {
		t.Errorf("FindRefs(plyer.tscn) = %v, similar %v", err, m)
	}
	if _, err := p.FindRefs("uid://heroo"); !errors.As(err, &m) || !slices.Contains(m.Similar, "uid://hero") {
		t.Errorf("FindRefs(uid://heroo) = %v", err)
	}
	if _, err := p.FindRefs("scenes"); err == nil || !strings.Contains(err.Error(), "folder") {
		t.Errorf("FindRefs(folder) = %v", err)
	}
	if _, err := p.FindRefs("../outside.gd"); err == nil || errors.As(err, &m) {
		t.Errorf("FindRefs(outside) = %v, want a path error", err)
	}
}

func TestFindRefsCap(t *testing.T) {
	files := map[string]string{"project.godot": "", "a.gd": "", "b.gd": strings.Repeat("var x = load(\"res://a.gd\")\n", FindLimit+50)}
	got, err := findProject(t, files).FindRefs("a.gd")
	if err != nil || len(got.References) != FindLimit || !got.Truncated {
		t.Errorf("FindRefs over the cap: %d refs, truncated %v, %v", len(got.References), got.Truncated, err)
	}
}

func nodeLines(hits []NodeHit) []string {
	var out []string
	for _, h := range hits {
		out = append(out, strings.TrimPrefix(h.Scene, "res://")+" "+h.Node)
	}
	return out
}

func TestFindNodes(t *testing.T) {
	p := findProject(t, findFiles)
	for class, want := range map[string][]string{
		// The instance counts by its root, Zone under it by its own type.
		"Area2D":           {"scenes/main.tscn World/Hero/Zone", "scenes/player.tscn Hitbox"},
		"CollisionShape2D": {"scenes/player.tscn Shape", "scenes/player.tscn Hitbox/Shape"},
		"Hero":             {"scenes/main.tscn World/Hero", "scenes/player.tscn ."},
		"CharacterBody2D":  {"scenes/main.tscn World/Hero", "scenes/player.tscn ."},
		// An inherited scene's root takes its type from the scene it instances.
		"Node2D":   {"scenes/inherited.tscn .", "scenes/main.tscn .", "scenes/main.tscn World"},
		"Sprite2D": nil,
	} {
		got, err := p.FindNodes(class)
		if err != nil || !slices.Equal(nodeLines(got.Nodes), want) {
			t.Errorf("FindNodes(%s) = %v, %v; want %v", class, nodeLines(got.Nodes), err, want)
		}
	}
	got, _ := p.FindNodes("Hero")
	if !slices.Equal(got.Scripts, []string{"res://scripts/hero.gd"}) || got.Nodes[0].Instance != "res://scenes/player.tscn" || got.Nodes[0].Line != 9 {
		t.Errorf("FindNodes(Hero) = %+v", got)
	}
}

// TestFindStaysInProject checks that links and scene paths cannot make
// project_find read files outside the project.
func TestFindStaysInProject(t *testing.T) {
	outside := t.TempDir()
	os.WriteFile(filepath.Join(outside, "secret.gd"), []byte("var key = \"res://scenes/player.tscn\"\n"), 0o644)
	p := findProject(t, map[string]string{
		"project.godot":      "",
		"scenes/player.tscn": "[gd_scene format=3]\n\n[node name=\"Player\" type=\"Node\"]\n",
		"scenes/main.tscn":   "[gd_scene format=3]\n\n[ext_resource type=\"PackedScene\" path=\"res://../out.tscn\" id=\"1\"]\n\n[node name=\"Main\" type=\"Node\"]\n\n[node name=\"O\" parent=\".\" instance=ExtResource(\"1\")]\n",
	})
	// res://../out.tscn names the folder above the project.
	os.WriteFile(filepath.Join(filepath.Dir(p.Dir), "out.tscn"), []byte("[gd_scene format=3]\n\n[node name=\"Out\" type=\"Timer\"]\n"), 0o644)
	if err := os.Symlink(filepath.Join(outside, "secret.gd"), filepath.Join(p.Dir, "leak.gd")); err != nil {
		t.Skip("cannot make links here:", err)
	}
	os.Symlink(outside, filepath.Join(p.Dir, "linked"))
	refs, err := p.FindRefs("res://scenes/player.tscn")
	if err != nil || len(refs.References) != 0 {
		t.Errorf("FindRefs read through a link: %v, %v", refLines(refs.References), err)
	}
	if nodes, err := p.FindNodes("Timer"); err != nil || len(nodes.Nodes) != 0 {
		t.Errorf("FindNodes followed a path out of the project: %+v, %v", nodes, err)
	}
}

// TestFindNodesAfterMove checks that a scene whose ext_resource paths are
// stale after a move still counts, through the uids Godot loads it by.
func TestFindNodesAfterMove(t *testing.T) {
	p := findProject(t, map[string]string{
		"project.godot":     "",
		"chars/player.tscn": "[gd_scene format=3 uid=\"uid://pl\"]\n\n[ext_resource type=\"Script\" uid=\"uid://hero\" path=\"res://old/hero.gd\" id=\"1\"]\n\n[node name=\"Player\" type=\"Node2D\"]\nscript = ExtResource(\"1\")\n",
		"chars/hero.gd":     "class_name Hero\nextends Node2D\n",
		"chars/hero.gd.uid": "uid://hero\n",
		"levels/level.tscn": "[gd_scene format=3]\n\n[ext_resource type=\"PackedScene\" uid=\"uid://pl\" path=\"res://old/player.tscn\" id=\"1\"]\n\n[node name=\"Level\" type=\"Node\"]\n\n[node name=\"P\" parent=\".\" instance=ExtResource(\"1\")]\n",
	})
	got, err := p.FindNodes("Hero")
	if want := []string{"chars/player.tscn .", "levels/level.tscn P"}; err != nil || !slices.Equal(nodeLines(got.Nodes), want) {
		t.Errorf("FindNodes(Hero) = %v, %v; want %v", nodeLines(got.Nodes), err, want)
	}
}

func TestFindNodesCap(t *testing.T) {
	var b strings.Builder
	b.WriteString("[gd_scene format=3]\n\n[node name=\"Root\" type=\"Node\"]\n")
	for i := range FindLimit + 5 {
		fmt.Fprintf(&b, "\n[node name=\"T%d\" type=\"Timer\" parent=\".\"]\n", i)
	}
	got, err := findProject(t, map[string]string{"project.godot": "", "many.tscn": b.String()}).FindNodes("Timer")
	if err != nil || len(got.Nodes) != FindLimit || !got.Truncated {
		t.Errorf("FindNodes over the cap: %d nodes, truncated %v, %v", len(got.Nodes), got.Truncated, err)
	}
}
