package e2e

import (
	"encoding/json"
	"strings"
	"testing"
)

// project_find reads the project's files itself, so no editor runs here.
func TestProjectFind(t *testing.T) {
	s := newSession(t)
	s.cs, s.server = s.connect()
	check := func(args map[string]any, want ...string) map[string]any {
		t.Helper()
		r := s.ok("project_find", args)
		b, _ := json.Marshal(r)
		for _, w := range want {
			if !strings.Contains(string(b), w) {
				t.Errorf("project_find(%v) lacks %s: %s", args, w, b)
			}
		}
		return r
	}

	check(map[string]any{"path": "res://demo/player.tscn"},
		`"references":[{"file":"res://demo/main.tscn","kind":"ext_resource","line":4,`, `"truncated":false`)
	check(map[string]any{"path": "demo/item_data.gd"},
		`"class_name":"ItemData"`, `{"file":"res://demo/player.gd","kind":"class","line":7,"text":"@export var weapon: ItemData"}`)
	check(map[string]any{"path": "res://demo/main.tscn"}, `{"file":"res://project.godot","kind":"setting",`, `"text":"run/main_scene=\"res://demo/main.tscn\""`)

	// The import gave the script a uid; it leads back to the file.
	uid, _ := check(map[string]any{"path": "res://demo/player.gd"}, `"uid":"uid://`, `"file":"res://demo/player.tscn","kind":"ext_resource"`)["uid"].(string)
	check(map[string]any{"path": uid}, `"path":"res://demo/player.gd"`)

	check(map[string]any{"node_type": "DemoPlayer"}, `"scripts":["res://demo/player.gd"]`,
		`{"instance":"res://demo/player.tscn","line":9,"node":"Player","scene":"res://demo/main.tscn"}`, `{"line":8,"node":".","scene":"res://demo/player.tscn"}`)
	check(map[string]any{"node_type": "Timer"}, `"nodes":[{"line":24,"node":"Tick","scene":"res://demo/main.tscn"}]`)
	check(map[string]any{"node_type": "Sprite2D"}, `"nodes":[]`)

	s.fails("project_find", map[string]any{"path": "res://demo/plyer.tscn"}, "FILE_NOT_FOUND")
	s.fails("project_find", map[string]any{"path": "res://demo/plyer.tscn"}, "did you mean: res://demo/player.tscn")
	s.fails("project_find", map[string]any{"path": "uid://nope"}, "UID_NOT_FOUND")
	s.fails("project_find", map[string]any{"path": "res://demo/main.tscn", "node_type": "Timer"}, "BAD_ARGUMENTS")
	s.fails("project_find", map[string]any{"path": "../outside.gd"}, "outside the project")
}
