package e2e

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

// class_info asks the project's Godot headless, so no editor runs here.
func TestClassInfo(t *testing.T) {
	s := newSession(t)
	s.cs, s.server = s.connect()
	answer := func(args map[string]any) string {
		var b strings.Builder
		enc := json.NewEncoder(&b)
		enc.SetEscapeHTML(false) // keeps "->" readable
		enc.Encode(s.ok("class_info", args))
		return b.String()
	}
	check := func(args map[string]any, want, notWant []string) {
		t.Helper()
		got := answer(args)
		for _, w := range want {
			if !strings.Contains(got, w) {
				t.Errorf("class_info(%v) lacks %s: %s", args, w, got)
			}
		}
		for _, w := range notWant {
			if strings.Contains(got, w) {
				t.Errorf("class_info(%v) has %s: %s", args, w, got)
			}
		}
	}

	// Built-in: its own members only; setters and getters stay out of methods.
	check(map[string]any{"class": "CharacterBody2D"},
		[]string{`"inherits":["PhysicsBody2D",`, `"move_and_slide() -> bool"`, `{"default":"Vector2(0, 0)","name":"velocity","type":"Vector2"}`, `"type":"CharacterBody2D.MotionMode"`},
		[]string{"set_velocity", "get_parent"})
	check(map[string]any{"class": "CharacterBody2D", "filter": "slide"}, []string{"move_and_slide"}, []string{`"velocity"`})
	check(map[string]any{"class": "CharacterBody2D", "include_inherited": true, "filter": "PARENT"}, []string{`"get_parent() -> Node"`}, []string{"move_and_slide"})

	// A class_name script from demo/.
	check(map[string]any{"class": "DemoPlayer"},
		[]string{`"path":"res://demo/player.gd"`, `"inherits":["CharacterBody2D",`, `{"default":200,"name":"speed","type":"float"}`,
			`{"name":"weapon","type":"ItemData"}`, `"signals":["died()"]`, `"take_hit() -> void"`},
		[]string{"move_and_slide"})
	check(map[string]any{"class": "DemoPlayer", "include_inherited": true, "filter": "velocity"}, []string{`"name":"velocity"`}, []string{"speed"})

	// A class_name added after the import, found once the class cache is refreshed.
	write(t, filepath.Join(s.proj, "scripts", "helper.gd"), "class_name Helper\nextends RefCounted\n\nstatic func hi(times := 1) -> int:\n\treturn times\n")
	check(map[string]any{"class": "Helper"}, []string{`"static hi(times: int = 1) -> int"`, "godot --import"}, nil)

	// Constants and enums of a base script come with include_inherited.
	write(t, filepath.Join(s.proj, "scripts", "base_unit.gd"), "class_name BaseUnit\nextends RefCounted\n\nconst SPEED = 10\nenum State { IDLE, RUN }\n")
	write(t, filepath.Join(s.proj, "scripts", "child_unit.gd"), "class_name ChildUnit\nextends BaseUnit\n\nconst OWN = 1\n")
	check(map[string]any{"class": "ChildUnit"}, []string{`"OWN"`}, []string{`"SPEED"`, `"State"`})
	check(map[string]any{"class": "ChildUnit", "include_inherited": true}, []string{`"OWN"`, `"SPEED"`, `"State"`}, nil)

	s.fails("class_info", map[string]any{"class": "CharacterBody2d"}, "CLASS_NOT_FOUND")
	s.fails("class_info", map[string]any{"class": "CharacterBody2d"}, "did you mean: CharacterBody2D")
	write(t, filepath.Join(s.proj, "scripts", "broken.gd"), "class_name Broken\nextends Node\n\nvar x: int = \"text\"\n")
	s.fails("class_info", map[string]any{"class": "Broken"}, "SCRIPT_ERROR")
}
