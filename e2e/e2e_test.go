// Package e2e runs the real godot-mcp binary over stdio against a Godot
// editor with the plugin enabled. It needs Godot 4.4+ (found the same way the
// server finds it) and is skipped when Godot is missing or with -short.
//
// TestEditorScreenshot opens a visible editor window, so it only runs with
// GODOT_MCP_E2E_WINDOWED=1. With GODOT_MCP_E2E_REQUIRED=1 a missing Godot
// fails the tests instead of skipping them.
package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/orebron/godot-mcp/internal/godot"
)

// session is a headless or windowed editor on a copy of the demo project,
// plus the godot-mcp binary connected to it over stdio.
type session struct {
	t         *testing.T
	ctx       context.Context
	cs        *mcp.ClientSession
	server    *exec.Cmd // the godot-mcp process behind cs
	proj      string
	editorLog string
	bin       string
	godot     godot.Exec
	godotPath string
}

// findGodot returns the Godot to test with, or skips the test.
func findGodot(t *testing.T) godot.Exec {
	t.Helper()
	if testing.Short() {
		t.Skip("needs Godot; skipped with -short")
	}
	g, err := godot.Locate("")
	if err != nil {
		// CI sets GODOT_MCP_E2E_REQUIRED so that a missing Godot fails the
		// run instead of passing it with nothing tested.
		if os.Getenv("GODOT_MCP_E2E_REQUIRED") == "1" {
			t.Fatal(err)
		}
		t.Skip(err)
	}
	return g
}

func start(t *testing.T, headless bool) *session {
	t.Helper()
	s := newSession(t)
	args := []string{"-e", "--path", s.proj}
	if headless {
		args = append([]string{"--headless"}, args...)
	}
	editor := s.godot.Command(s.ctx, args...)
	editorLog, _ := os.Create(filepath.Join(t.TempDir(), "editor.log"))
	editor.Stdout, editor.Stderr = editorLog, editorLog
	if err := editor.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		editor.Cancel()
		editor.Wait()
		if t.Failed() {
			b, _ := os.ReadFile(editorLog.Name())
			t.Logf("editor log:\n%s", b)
		}
	})
	waitFor(t, filepath.Join(s.proj, ".godot", "godot_mcp", "bridge.json"), time.Minute)
	s.editorLog = editorLog.Name()
	s.cs, s.server = s.connect()
	return s
}

// newSession copies the demo project, imports it and builds the binary,
// without starting an editor or a server.
func newSession(t *testing.T) *session {
	t.Helper()
	g := findGodot(t)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	t.Cleanup(cancel)

	repo, _ := filepath.Abs("..")
	proj := t.TempDir()
	for _, p := range []string{"project.godot", "addons", "demo"} {
		copyTree(t, filepath.Join(repo, p), filepath.Join(proj, p))
	}
	if out, err := g.Command(ctx, "--headless", "--path", proj, "--import").CombinedOutput(); err != nil {
		t.Fatalf("import: %v\n%s", err, out)
	}
	bin := filepath.Join(t.TempDir(), "godot-mcp")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	if out, err := exec.Command("go", "build", "-o", bin, "../cmd/godot-mcp").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	return &session{t: t, ctx: ctx, proj: proj, bin: bin, godot: g, godotPath: g.Path}
}

// connect starts another godot-mcp server on the same project, as a second
// agent session would.
func (s *session) connect() (*mcp.ClientSession, *exec.Cmd) {
	s.t.Helper()
	cmd := exec.Command(s.bin, "--project", s.proj, "--godot", s.godotPath)
	client := mcp.NewClient(&mcp.Implementation{Name: "e2e", Version: "0"}, nil)
	cs, err := client.Connect(s.ctx, &mcp.CommandTransport{Command: cmd}, nil)
	if err != nil {
		s.t.Fatal(err)
	}
	s.t.Cleanup(func() { cs.Close() })
	return cs, cmd
}

func (s *session) result(name string, args map[string]any) *mcp.CallToolResult {
	s.t.Helper()
	res, err := s.cs.CallTool(s.ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		s.t.Fatalf("%s: protocol error: %v", name, err)
	}
	return res
}

func (s *session) call(name string, args map[string]any) (string, bool) {
	s.t.Helper()
	res := s.result(name, args)
	var text strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			text.WriteString(tc.Text)
		}
	}
	return text.String(), res.IsError
}

func (s *session) ok(name string, args map[string]any) map[string]any {
	s.t.Helper()
	text, isErr := s.call(name, args)
	if isErr {
		s.t.Fatalf("%s(%v) failed: %s", name, args, text)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(text), &m); err != nil {
		s.t.Fatalf("%s: not JSON: %s", name, text)
	}
	return m
}

// checkPaging reads tool's tree one node per page and checks that the pages
// together show the nodes of one full answer, in the same order.
func (s *session) checkPaging(tool string, args map[string]any) {
	s.t.Helper()
	var want, got []string
	shownPaths(s.ok(tool, args)["tree"], &want)
	page := map[string]any{"limit": 1}
	maps.Copy(page, args)
	for offset := 0; ; {
		page["offset"] = offset
		r := s.ok(tool, page)
		shownPaths(r["tree"], &got)
		next, more := r["next_offset"].(float64)
		if !more {
			break
		}
		if int(next) <= offset || len(got) > len(want) {
			s.t.Fatalf("%s pages do not advance: offset %d gave next_offset %v", tool, offset, next)
		}
		offset = int(next)
	}
	if len(want) < 2 || !slices.Equal(got, want) {
		s.t.Errorf("%s one node per page showed %v, one answer %v", tool, got, want)
	}
}

// shownPaths lists the paths of the nodes a tree answer shows in full;
// nodes from earlier pages carry no type.
func shownPaths(tree any, out *[]string) {
	d, _ := tree.(map[string]any)
	if _, shown := d["type"]; shown {
		*out = append(*out, d["path"].(string))
	}
	kids, _ := d["children"].([]any)
	for _, k := range kids {
		shownPaths(k, out)
	}
}

func (s *session) fails(name string, args map[string]any, want string) {
	s.t.Helper()
	text, isErr := s.call(name, args)
	if !isErr || !strings.Contains(text, want) {
		s.t.Errorf("%s(%v): want an error containing %q, got isError=%v %s", name, args, want, isErr, text)
	}
}

func TestEndToEnd(t *testing.T) {
	s := start(t, true)
	ok, fails, proj := s.ok, s.fails, s.proj

	tools, err := s.cs.ListTools(s.ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(tools.Tools) != 33 {
		t.Errorf("got %d tools, want 33", len(tools.Tools))
	}

	info := ok("project_info", nil)
	if ed := info["editor"].(map[string]any); ed["connected"] != true {
		t.Fatalf("editor not connected: %v", ed)
	}

	// Scenes and nodes.
	ok("scene_create", map[string]any{"path": "res://levels/test_level.tscn", "root_type": "Node2D"})
	fails("scene_create", map[string]any{"path": "res://levels/test_level.tscn", "root_type": "Node2D"}, "FILE_EXISTS")
	added := ok("node_add", map[string]any{"type": "Sprite2D", "name": "Hero", "properties": map[string]any{"position": []any{10, 20}, "modulate": "#ff0000"}})
	if added["path"] != "Hero" {
		t.Errorf("node_add path = %v", added["path"])
	}
	ok("node_add", map[string]any{"scene": "res://demo/player.tscn", "properties": map[string]any{"speed": 50}})
	fails("node_add", map[string]any{"type": "Sprit2D"}, "Sprite2D")
	// A class_name class, which a non-tool script cannot instance with new() in the editor.
	if r := ok("node_add", map[string]any{"type": "DemoPlayer", "name": "Rival"}); r["type"] != "CharacterBody2D" {
		t.Errorf("node_add DemoPlayer = %v", r)
	}
	if p := ok("node_get", map[string]any{"node_path": "Rival", "properties": []any{"script"}})["properties"].(map[string]any); p["script"] != "res://demo/player.gd" {
		t.Errorf("the DemoPlayer node has no player script: %v", p)
	}
	ok("node_delete", map[string]any{"node_path": "Rival"})
	fails("node_add", map[string]any{"type": "Resource"}, "INVALID_TYPE")
	fails("node_add", map[string]any{"parent_path": "Nope/Deeper", "type": "Node"}, "NODE_NOT_FOUND")
	// ".." must not reach the editor's own nodes above the scene root.
	fails("node_delete", map[string]any{"node_path": ".."}, "INVALID_PATH")
	fails("node_get", map[string]any{"node_path": "Player/../.."}, "INVALID_PATH")
	fails("node_set_properties", map[string]any{"node_path": "../..", "properties": map[string]any{"name": "x"}}, "INVALID_PATH")
	if text, _ := s.call("node_get", map[string]any{"node_path": "../Nope"}); strings.Contains(text, "@") {
		t.Errorf("error for a path above the root names editor nodes: %s", text)
	}
	if runtime.GOOS != "linux" { // Linux would not find the file at all
		fails("node_add", map[string]any{"scene": "res://Demo/Player.tscn"}, "letter case")
		fails("scene_open", map[string]any{"path": "res://demo/MAIN.tscn"}, "letter case")
	}

	ok("node_set_properties", map[string]any{"node_path": "Hero", "properties": map[string]any{"position": map[string]any{"x": 5, "y": 6}, "visible": false}})
	fails("node_set_properties", map[string]any{"node_path": "Hero", "properties": map[string]any{"positon": []any{1, 2}}}, "position")
	fails("node_set_properties", map[string]any{"node_path": "Hero", "properties": map[string]any{"position": "left"}}, "INVALID_VALUE")
	// Resources must fit the property's class, and scripts the node's class.
	write(t, filepath.Join(proj, "shapes", "box.tres"), "[gd_resource type=\"RectangleShape2D\" format=3]\n\n[resource]\nsize = Vector2(8, 8)\n")
	ok("node_add", map[string]any{"type": "CollisionShape2D", "name": "Hit"})
	fails("node_set_properties", map[string]any{"node_path": "Hit", "properties": map[string]any{"shape": "res://demo/player.tscn"}}, "needs Shape2D")
	ok("node_set_properties", map[string]any{"node_path": "Hit", "properties": map[string]any{"shape": "res://shapes/box.tres"}})
	if p := ok("node_get", map[string]any{"node_path": "Hit", "properties": []any{"shape"}})["properties"].(map[string]any); p["shape"] != "res://shapes/box.tres" {
		t.Errorf("Hit shape = %v", p["shape"])
	}
	fails("node_set_properties", map[string]any{"node_path": "Hero", "properties": map[string]any{"script": "res://demo/player.gd"}}, "extends CharacterBody2D")

	// A new resource inline, read back in the same form.
	ok("node_set_properties", map[string]any{"node_path": "Hit", "properties": map[string]any{"shape": map[string]any{"type": "RectangleShape2D", "size": []any{8, 12}}}})
	shape := ok("node_get", map[string]any{"node_path": "Hit", "properties": []any{"shape", "shape:size"}})["properties"].(map[string]any)
	if inline, _ := shape["shape"].(map[string]any); inline["type"] != "RectangleShape2D" || inline["size"] != "Vector2(8, 12)" || shape["shape:size"] != "Vector2(8, 12)" {
		t.Errorf("inline shape reads back as %v", shape)
	}
	for spec, want := range map[string]string{
		`{"type": "Sprite2D"}`:                       "not a resource class",
		`{"type": "PackedScene"}`:                    "needs Shape2D",
		`{"type": "Shape2D"}`:                        "abstract",
		`{"type": "CircleShape2D", "radius": "big"}`: "CircleShape2D.radius",
		`{"type": "CircleShape2D", "radus": 3}`:      "no such property",
	} {
		var v map[string]any
		json.Unmarshal([]byte(spec), &v)
		fails("node_set_properties", map[string]any{"node_path": "Hit", "properties": map[string]any{"shape": v}}, want)
	}

	// Paths inside property values get the same checks as tool paths.
	fails("node_set_properties", map[string]any{"node_path": "Hero", "properties": map[string]any{"texture": "res://../outside.png"}}, "leaves the project")
	fails("node_set_properties", map[string]any{"node_path": "Hero", "properties": map[string]any{"texture": `res://demo\..\..\outside.png`}}, "leaves the project")
	if runtime.GOOS != "linux" {
		fails("node_set_properties", map[string]any{"node_path": "Hero", "properties": map[string]any{"script": "res://Demo/main.gd"}}, "letter case")
	}

	got := ok("node_get", map[string]any{"node_path": "Hero"})
	props := got["properties"].(map[string]any)
	if props["position"] != "Vector2(5, 6)" || props["visible"] != false {
		t.Errorf("node_get Hero = %v", props)
	}
	player := ok("node_get", map[string]any{"node_path": "Player", "properties": []any{"speed"}})
	if player["properties"].(map[string]any)["speed"] != 50.0 {
		t.Errorf("instanced Player speed = %v", player["properties"])
	}
	// The insides of an instanced scene belong to player.tscn: readable, but
	// not deletable, and not editable without Editable Children.
	ok("node_get", map[string]any{"node_path": "Player/Shape"})
	fails("node_delete", map[string]any{"node_path": "Player/Shape"}, "FOREIGN_NODE")
	fails("node_set_properties", map[string]any{"node_path": "Player/Shape", "properties": map[string]any{"disabled": true}}, "Editable Children")
	fails("node_add", map[string]any{"parent_path": "Player/Shape", "type": "Node"}, "FOREIGN_NODE")

	tree := ok("scene_tree", nil)
	treeJSON, _ := json.Marshal(tree)
	for _, want := range []string{`"Hero"`, `"instance":"res://demo/player.tscn"`} {
		if !strings.Contains(string(treeJSON), want) {
			t.Errorf("scene_tree lacks %s: %s", want, treeJSON)
		}
	}
	if strings.Contains(string(treeJSON), `"Shape"`) {
		t.Errorf("scene_tree shows the inside of an instanced scene: %s", treeJSON)
	}
	s.checkPaging("scene_tree", nil)

	ok("node_add", map[string]any{"type": "Node2D", "name": "Temp", "parent_path": "Hero"})
	ok("node_delete", map[string]any{"node_path": "Hero/Temp"})
	fails("node_get", map[string]any{"node_path": "Hero/Temp"}, "NODE_NOT_FOUND")

	// Resource files, built-in and class_name, and class_name resources inline.
	ok("resource_save", map[string]any{"path": "res://items/box.tres", "type": "RectangleShape2D", "properties": map[string]any{"size": []any{4, 4}}})
	ok("resource_save", map[string]any{"path": "res://items/sword.tres", "type": "ItemData", "properties": map[string]any{"title": "Sword", "damage": 5}})
	fails("resource_save", map[string]any{"path": "res://items/sword.tres", "type": "ItemData"}, "FILE_EXISTS")
	ok("resource_save", map[string]any{"path": "res://items/sword.tres", "type": "ItemData", "properties": map[string]any{"title": "Sword", "damage": 6}, "overwrite": true})
	fails("resource_save", map[string]any{"path": "res://items/x.tres", "type": "Sprite2D"}, "not a resource class")
	fails("resource_save", map[string]any{"path": "res://items/x.tres", "type": "ItemData", "properties": map[string]any{"damage": "lots"}}, "ItemData.damage")
	for file, want := range map[string]string{"box.tres": "size = Vector2(4, 4)", "sword.tres": "damage = 6"} {
		if b, _ := os.ReadFile(filepath.Join(proj, "items", file)); !strings.Contains(string(b), want) {
			t.Errorf("%s lacks %q:\n%s", file, want, b)
		}
	}
	ok("node_set_properties", map[string]any{"node_path": "Player", "properties": map[string]any{"weapon": map[string]any{"type": "ItemData", "damage": 7}}})
	if w, _ := ok("node_get", map[string]any{"node_path": "Player", "properties": []any{"weapon"}})["properties"].(map[string]any)["weapon"].(map[string]any); w["type"] != "ItemData" || w["damage"] != 7.0 {
		t.Errorf("inline ItemData reads back as %v", w)
	}
	ok("node_set_properties", map[string]any{"node_path": "Player", "properties": map[string]any{"weapon": "res://items/sword.tres"}})
	fails("node_set_properties", map[string]any{"node_path": "Player", "properties": map[string]any{"weapon": "res://items/box.tres"}}, "needs ItemData")

	// Copy, rename, reparent and reorder, and Editable Children.
	if r := ok("node_duplicate", map[string]any{"node_path": "Hero"}); r["path"] != "Hero2" {
		t.Errorf("node_duplicate Hero = %v", r)
	}
	if p := ok("node_get", map[string]any{"node_path": "Hero2", "properties": []any{"position"}})["properties"].(map[string]any); p["position"] != "Vector2(5, 6)" {
		t.Errorf("the copy lost its position: %v", p)
	}
	if r := ok("node_move", map[string]any{"node_path": "Hero2", "new_name": "Clone", "new_parent": "Player", "index": 0}); r["path"] != "Player/Clone" {
		t.Errorf("node_move into Player = %v", r)
	}
	if r := ok("node_move", map[string]any{"node_path": "Player/Clone", "new_parent": ".", "index": 0}); r["path"] != "Clone" {
		t.Errorf("node_move back to the root = %v", r)
	}
	if first := ok("scene_tree", map[string]any{"max_depth": 1})["tree"].(map[string]any)["children"].([]any)[0].(map[string]any); first["name"] != "Clone" {
		t.Errorf("index 0 did not put Clone first: %v", first)
	}
	fails("node_move", map[string]any{"node_path": ".", "new_parent": "Hero"}, "root cannot be moved")
	fails("node_move", map[string]any{"node_path": "Hero", "new_parent": "Hero"}, "into itself")
	fails("node_move", map[string]any{"node_path": "Hero"}, "INVALID_PARAMS")
	fails("node_move", map[string]any{"node_path": "Hero", "editable_children": true}, "instanced scene")
	ok("node_move", map[string]any{"node_path": "Player", "editable_children": true})
	ok("node_set_properties", map[string]any{"node_path": "Player/Shape", "properties": map[string]any{"disabled": true}})

	// Project settings and input actions, saved to project.godot.
	if v := ok("project_settings_get", map[string]any{"prefix": "display/window/size/viewport_"})["settings"].(map[string]any); v["display/window/size/viewport_width"] != 1152.0 {
		t.Errorf("project_settings_get viewport = %v", v)
	}
	ok("project_settings_set", map[string]any{"settings": map[string]any{"application/config/description": "MCP test"}})
	fails("project_settings_set", map[string]any{"settings": map[string]any{"display/window/size/viewport_widht": 640}}, "viewport_width")
	fails("project_settings_set", map[string]any{"settings": map[string]any{"display/window/size/viewport_width": "wide"}}, "INVALID_VALUE")
	fails("project_settings_get", nil, "prefix or names")
	jump := ok("input_action_set", map[string]any{"action": "jump", "keys": []any{"Space", "W"}, "joypad_buttons": []any{0}})
	if ev, _ := json.Marshal(jump["events"]); !strings.Contains(string(ev), "Space") || len(jump["events"].([]any)) != 3 {
		t.Errorf("input_action_set jump = %v", jump)
	}
	ok("input_action_set", map[string]any{"action": "dash", "mouse_buttons": []any{"right"}})
	ok("input_action_set", map[string]any{"action": "dash", "remove": true})
	fails("input_action_set", map[string]any{"action": "x", "keys": []any{"Hyperdrive"}}, "unknown key")
	godotFile, _ := os.ReadFile(filepath.Join(proj, "project.godot"))
	for _, want := range []string{`config/description="MCP test"`, "jump={", "physical_keycode"} {
		if !strings.Contains(string(godotFile), want) {
			t.Errorf("project.godot lacks %s:\n%s", want, godotFile)
		}
	}
	if strings.Contains(string(godotFile), "dash=") {
		t.Errorf("the removed action dash is still in project.godot")
	}

	// Signals: connect, saved with the scene.
	con := ok("node_signal_connect", map[string]any{"from": "Hero", "signal": "visibility_changed", "to": "Player", "method": "take_hit"})
	if conJSON, _ := json.Marshal(con["connections"]); string(conJSON) != `[{"method":"take_hit","to":"Player"}]` {
		t.Errorf("connections after connect = %s", conJSON)
	}
	ok("node_signal_connect", map[string]any{"from": "Hero", "signal": "visibility_changed", "to": "Player", "method": "take_hit"}) // again: no-op
	fails("node_signal_connect", map[string]any{"from": "Hero", "signal": "explode", "to": "Player", "method": "take_hit"}, "SIGNAL_NOT_FOUND")
	fails("node_signal_connect", map[string]any{"from": "Hero", "signal": "visibility_changed", "to": "Player", "method": "fly"}, "METHOD_NOT_FOUND")

	ok("scene_save", nil)
	saved, err := os.ReadFile(filepath.Join(proj, "levels", "test_level.tscn"))
	if !strings.Contains(string(saved), `[connection signal="visibility_changed" from="Hero" to="Player" method="take_hit"]`) {
		t.Errorf("the saved scene lacks the connection:\n%s", saved)
	}
	if r := ok("node_signal_connect", map[string]any{"from": "Hero", "signal": "visibility_changed", "to": "Player", "method": "take_hit", "disconnect": true}); r["connected"] != false || len(r["connections"].([]any)) != 0 {
		t.Errorf("disconnect = %v", r)
	}
	if err != nil || !strings.Contains(string(saved), `name="Hero"`) || !strings.Contains(string(saved), "player.tscn") {
		t.Errorf("saved scene: %v\n%s", err, saved)
	}
	fails("editor_screenshot", nil, "NOT_AVAILABLE_HEADLESS")

	// Scripts: a new class_name script and a script that uses it, plus a broken one.
	write(t, filepath.Join(proj, "scripts", "helper.gd"), "class_name Helper\nextends RefCounted\n\nstatic func hi() -> int:\n\treturn 1\n")
	write(t, filepath.Join(proj, "scripts", "user.gd"), "extends Node\n\nfunc _ready() -> void:\n\tprint(Helper.hi())\n")
	write(t, filepath.Join(proj, "scripts", "broken.gd"), "extends Node\n\nfunc _ready() -> void:\n\tvar x: int = \"text\"\n")
	check := ok("script_check", nil)
	if check["ok"] != false || check["failed"] != 1.0 {
		t.Errorf("script_check = %v", check)
	}
	failures, _ := json.Marshal(check["failures"])
	if !strings.Contains(string(failures), `"file":"res://scripts/broken.gd"`) || !strings.Contains(string(failures), `"line":4`) {
		t.Errorf("script_check failures = %s", failures)
	}
	if c := ok("script_check", map[string]any{"paths": []any{"scripts/user.gd", "res://scripts/helper.gd"}}); c["ok"] != true {
		t.Errorf("class_name script not resolved: %v", c)
	}
	fails("script_check", map[string]any{"paths": []any{"../outside.gd"}}, "outside")

	// Two sessions checking different files at the same time must each get
	// their own result.
	cs2, _ := s.connect()
	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			res, err := s.cs.CallTool(s.ctx, &mcp.CallToolParams{Name: "script_check", Arguments: map[string]any{"paths": []any{"scripts/broken.gd"}}})
			if err != nil || res.IsError || !strings.Contains(res.Content[0].(*mcp.TextContent).Text, `"checked":1,"failed":1`) {
				t.Errorf("session 1 check: %v %+v", err, res)
			}
		}()
		go func() {
			defer wg.Done()
			res, err := cs2.CallTool(s.ctx, &mcp.CallToolParams{Name: "script_check", Arguments: map[string]any{"paths": []any{"scripts/user.gd", "scripts/helper.gd"}}})
			if err != nil || res.IsError || !strings.Contains(res.Content[0].(*mcp.TextContent).Text, `"checked":2,"failed":0`) {
				t.Errorf("session 2 check: %v %+v", err, res)
			}
		}()
	}
	wg.Wait()
	if entries, _ := os.ReadDir(filepath.Join(proj, ".godot", "godot_mcp")); len(entries) != 1 {
		t.Errorf("script_check left files behind: %v", entries)
	}

	// Godot hands back a script object even when parsing fails, so the error
	// must be tied to the file even when its name has parentheses.
	write(t, filepath.Join(proj, "scripts", "enemy (copy).gd"), "extends Node\n\nfunc _ready() -> void:\n\tundefined_call()\n")
	odd := ok("script_check", map[string]any{"paths": []any{"scripts/enemy (copy).gd"}})
	oddJSON, _ := json.Marshal(odd)
	if odd["ok"] != false || !strings.Contains(string(oddJSON), `"file":"res://scripts/enemy (copy).gd"`) {
		t.Errorf("broken script with parentheses in its name: %s", oddJSON)
	}
	os.Remove(filepath.Join(proj, "scripts", "enemy (copy).gd"))

	if runtime.GOOS != "linux" {
		fails("script_check", map[string]any{"paths": []any{"Scripts/user.gd"}}, "letter case")
	}

	// Running the game.
	run := ok("game_run", map[string]any{"scene": "res://demo/main.tscn", "headless": true, "wait_seconds": 3})
	lines, _ := json.Marshal(run["lines"])
	if !strings.Contains(string(lines), "Demo started: 4 children") {
		t.Errorf("game_run output = %s", lines)
	}
	// The game starts through the harness, whose bridge dials back.
	if st := run["status"].(map[string]any); st["bridge"] != "connected" {
		t.Errorf("game_run status = %v, want bridge connected", st)
	}
	// A headless window is 64x64; the harness gives it the on-screen size.
	if p := ok("game_node_get", map[string]any{"node_path": "/root", "properties": []any{"size"}})["properties"].(map[string]any); p["size"] != "Vector2i(1152, 648)" {
		t.Errorf("headless game window size = %v, want the project's 1152x648", p["size"])
	}
	if issues, _ := json.Marshal(run["errors"]); string(issues) != "[]" && string(issues) != "null" {
		t.Errorf("the harness run printed errors: %s", issues)
	}
	fails("game_run", map[string]any{"headless": true}, "already running")

	// The live game, through the bridge.
	live := ok("game_scene_tree", nil)
	liveJSON, _ := json.Marshal(live)
	for _, want := range []string{`"scene":"res://demo/main.tscn"`, `"name":"Main"`, `"name":"Player"`, `"name":"Title"`} {
		if !strings.Contains(string(liveJSON), want) {
			t.Errorf("game_scene_tree lacks %s: %s", want, liveJSON)
		}
	}
	s.checkPaging("game_scene_tree", nil)
	rootJSON, _ := json.Marshal(ok("game_scene_tree", map[string]any{"node_path": "/root", "max_depth": 1}))
	if !strings.Contains(string(rootJSON), `"name":"Main"`) || strings.Contains(string(rootJSON), "GodotMCPBridge") {
		t.Errorf("game_scene_tree of /root: %s", rootJSON)
	}
	if p := ok("game_node_get", map[string]any{"node_path": "Player", "properties": []any{"position", "speed"}})["properties"].(map[string]any); p["position"] != "Vector2(320, 180)" || p["speed"] != 200.0 {
		t.Errorf("game_node_get Player = %v", p)
	}
	if p := ok("game_node_get", map[string]any{"node_path": "Player"})["properties"].(map[string]any); p["position"] != "Vector2(320, 180)" {
		t.Errorf("game_node_get Player without properties = %v", p)
	}
	fails("game_node_get", map[string]any{"node_path": "Nope"}, "NODE_NOT_FOUND")
	fails("game_node_get", map[string]any{"node_path": "Player", "properties": []any{"positon"}}, "PROPERTY_NOT_FOUND")

	// Pause holds the game still; step runs exactly the frames asked for.
	ticks := func() float64 {
		t.Helper()
		return ok("game_node_get", map[string]any{"node_path": ".", "properties": []any{"ticks"}})["properties"].(map[string]any)["ticks"].(float64)
	}
	if r := ok("game_time", map[string]any{"action": "pause"}); r["paused"] != true {
		t.Errorf("pause = %v", r)
	}
	before := ticks()
	time.Sleep(300 * time.Millisecond)
	if after := ticks(); after != before {
		t.Errorf("ticks moved while paused: %v -> %v", before, after)
	}
	if r := ok("game_time", map[string]any{"action": "step", "frames": 10}); r["paused"] != true || r["stepped_frames"] != 10.0 {
		t.Errorf("step = %v", r)
	}
	if after := ticks(); after != before+10 {
		t.Errorf("step 10 moved ticks from %v to %v", before, after)
	}
	fails("game_time", map[string]any{"action": "rewind"}, "INVALID_PARAMS")

	// Input, frame by frame: hold ui_right for exactly 30 physics frames.
	// The player moves speed/60 = 200/60 pixels per frame.
	ok("game_input", map[string]any{"action": "ui_right", "pressed": true})
	ok("game_time", map[string]any{"action": "step", "frames": 30})
	ok("game_input", map[string]any{"action": "ui_right", "pressed": false})
	pos := ok("game_node_get", map[string]any{"node_path": "Player", "properties": []any{"position"}})["properties"].(map[string]any)["position"].(string)
	var x, y float64
	if _, err := fmt.Sscanf(pos, "Vector2(%f, %f)", &x, &y); err != nil || x < 419 || x > 421 || y != 180 {
		t.Errorf("after 30 frames of ui_right the player is at %s, want about Vector2(420, 180)", pos)
	}
	ok("game_time", map[string]any{"action": "resume"})

	// A key and a click on the demo's Start button, which print.
	cursor := ok("game_output", map[string]any{"max_lines": 1})["next_since"]
	ok("game_input", map[string]any{"key": "Space"})
	ok("game_input", map[string]any{"mouse_button": "left", "position": []any{80, 80}})
	time.Sleep(300 * time.Millisecond)
	printed, _ := json.Marshal(ok("game_output", map[string]any{"since": cursor})["lines"])
	for _, want := range []string{"Key: Space", "Start pressed"} {
		if !strings.Contains(string(printed), want) {
			t.Errorf("the game did not print %q after game_input: %s", want, printed)
		}
	}
	fails("game_input", map[string]any{}, "INVALID_PARAMS")
	fails("game_input", map[string]any{"action": "teleport"}, "no input action")
	ok("game_input", map[string]any{"action": "jump"}) // added with input_action_set before the run

	// Setting up a situation in the live game: all values or none.
	set := ok("game_node_set", map[string]any{"node_path": "Player", "properties": map[string]any{"position": []any{100, 200}, "health": 1}})["properties"].(map[string]any)
	if set["position"] != "Vector2(100, 200)" || set["health"] != 1.0 {
		t.Errorf("game_node_set Player = %v", set)
	}
	ok("game_node_set", map[string]any{"node_path": "Player", "properties": map[string]any{"position": "Vector2(5, 6)"}})
	fails("game_node_set", map[string]any{"node_path": "Player", "properties": map[string]any{"positon": []any{1, 2}}}, "PROPERTY_NOT_FOUND")
	fails("game_node_set", map[string]any{"node_path": "Player", "properties": map[string]any{"position": []any{9, 9}, "health": "lots"}}, "INVALID_VALUE")
	if p := ok("game_node_get", map[string]any{"node_path": "Player", "properties": []any{"position"}})["properties"].(map[string]any); p["position"] != "Vector2(5, 6)" {
		t.Errorf("a failed game_node_set still moved the player: %v", p)
	}
	fails("game_input", map[string]any{"key": "Hyperdrive"}, "unknown key")
	fails("game_input", map[string]any{"mouse_button": "left"}, "needs position")
	fails("game_screenshot", nil, "NOT_AVAILABLE_HEADLESS")
	// Waiting for something to happen in the game.
	if r := ok("game_wait", map[string]any{"node_path": "Player"}); r["met"] != true {
		t.Errorf("game_wait for an existing node = %v", r)
	}
	if r := ok("game_wait", map[string]any{"node_path": "Ghost", "timeout_seconds": 0.3}); r["met"] != false {
		t.Errorf("game_wait for a missing node = %v", r)
	}
	if r := ok("game_wait", map[string]any{"node_path": "Tick", "signal": "timeout", "timeout_seconds": 2}); r["met"] != true {
		t.Errorf("game_wait for the timer's signal = %v", r)
	}
	now := ok("game_node_get", map[string]any{"node_path": ".", "properties": []any{"ticks"}})["properties"].(map[string]any)["ticks"].(float64)
	if r := ok("game_wait", map[string]any{"node_path": ".", "property": "ticks", "equals": now + 30, "timeout_seconds": 3}); r["met"] != true {
		t.Errorf("game_wait for ticks to reach %v = %v", now+30, r)
	}
	fails("game_wait", map[string]any{"node_path": "Player", "signal": "exploded"}, "SIGNAL_NOT_FOUND")
	ok("game_time", map[string]any{"action": "pause"})
	fails("game_wait", map[string]any{"node_path": "Player"}, "PAUSED")
	ok("game_time", map[string]any{"action": "resume"})

	perf := ok("game_performance", map[string]any{"sample_seconds": 0.5})
	if mon := perf["monitors"].(map[string]any); perf["measured_fps"].(float64) <= 0 || mon["nodes"].(float64) < 3 {
		t.Errorf("game_performance = %v", perf)
	}
	stop := ok("game_stop", nil)
	if stop["stopped"] != true {
		t.Errorf("game_stop = %v", stop)
	}
	fails("game_scene_tree", nil, "no game is running")
	out := ok("game_output", map[string]any{"since": 0, "errors_only": true})
	if _, has := out["lines"]; has {
		t.Errorf("errors_only still returned lines")
	}

	// The plugin must not have logged errors in the editor along the way.
	logText, _ := os.ReadFile(s.editorLog)
	for _, is := range godot.ParseIssues(strings.Split(string(logText), "\n")) {
		if is.Severity == "error" {
			t.Errorf("editor logged an error: %s %s (%s)", is.Kind, is.Message, is.Where)
		}
	}
}

func TestEditorScreenshot(t *testing.T) {
	if os.Getenv("GODOT_MCP_E2E_WINDOWED") != "1" {
		t.Skip("opens an editor window; set GODOT_MCP_E2E_WINDOWED=1 to run")
	}
	s := start(t, false)
	s.ok("scene_open", map[string]any{"path": "res://demo/main.tscn"})
	res := s.result("editor_screenshot", map[string]any{"max_width": 640})
	if res.IsError || len(res.Content) != 2 {
		for _, c := range res.Content {
			if tc, ok := c.(*mcp.TextContent); ok {
				t.Logf("text: %s", tc.Text)
			}
		}
		t.Fatalf("editor_screenshot = %+v", res.Content)
	}
	img, ok := res.Content[0].(*mcp.ImageContent)
	if !ok || img.MIMEType != "image/png" {
		t.Fatalf("first content is %T, want a PNG image", res.Content[0])
	}
	decoded, err := png.Decode(bytes.NewReader(img.Data))
	if err != nil {
		t.Fatalf("not a PNG: %v", err)
	}
	b := decoded.Bounds()
	if b.Dx() > 640 || b.Dx() == 0 {
		t.Errorf("width %d, want 1..640", b.Dx())
	}
	// A blank capture is one colour; the demo scene draws a label.
	if uniform(decoded) {
		t.Error("the screenshot is a single colour")
	}
	os.WriteFile(filepath.Join(os.TempDir(), "godot-mcp-e2e-screenshot.png"), img.Data, 0o644)
}

func TestGameScreenshot(t *testing.T) {
	if os.Getenv("GODOT_MCP_E2E_WINDOWED") != "1" {
		t.Skip("opens a game window; set GODOT_MCP_E2E_WINDOWED=1 to run")
	}
	s := start(t, true) // the editor stays headless; the game gets a window
	t.Cleanup(func() { s.call("game_stop", nil) })
	run := s.ok("game_run", map[string]any{"scene": "res://demo/main.tscn", "wait_seconds": 3})
	if st := run["status"].(map[string]any); st["bridge"] != "connected" {
		t.Fatalf("game_run status = %v", st)
	}
	res := s.result("game_screenshot", map[string]any{"max_width": 800})
	if res.IsError || len(res.Content) != 2 {
		t.Fatalf("game_screenshot = %+v", res.Content[0])
	}
	img := res.Content[0].(*mcp.ImageContent)
	decoded, err := png.Decode(bytes.NewReader(img.Data))
	if err != nil {
		t.Fatalf("not a PNG: %v", err)
	}
	if w := decoded.Bounds().Dx(); w == 0 || w > 800 {
		t.Errorf("width %d, want 1..800", w)
	}
	var meta map[string]any
	json.Unmarshal([]byte(res.Content[1].(*mcp.TextContent).Text), &meta)
	if meta["logical_width"] != 1152.0 || meta["logical_height"] != 648.0 {
		t.Errorf("screenshot meta = %v, want logical size 1152x648", meta)
	}
	if uniform(decoded) {
		t.Error("the game screenshot is a single colour")
	}
	os.WriteFile(filepath.Join(os.TempDir(), "godot-mcp-e2e-game.png"), img.Data, 0o644)
}

// uniform reports whether every sampled pixel has the same colour.
func uniform(img image.Image) bool {
	b := img.Bounds()
	first := img.At(b.Min.X, b.Min.Y)
	for y := b.Min.Y; y < b.Max.Y; y += 4 {
		for x := b.Min.X; x < b.Max.X; x += 4 {
			if img.At(x, y) != first {
				return false
			}
		}
	}
	return true
}

func copyTree(t *testing.T, src, dst string) {
	t.Helper()
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(target, b, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
}

func write(t *testing.T, path, content string) {
	t.Helper()
	os.MkdirAll(filepath.Dir(path), 0o755)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func waitFor(t *testing.T, path string, d time.Duration) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("%s did not appear within %s; the plugin did not start", path, d)
}
