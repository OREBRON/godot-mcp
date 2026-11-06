package e2e

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// evalTasks are the tasks TestAgentEval gives the agent, in a user's words.
// The prompts never name a tool: finding the right one is part of the task.
var evalTasks = []evalTask{
	{
		name:   "fix_script",
		setup:  writeBrokenScore,
		prompt: "res://demo/score.gd stopped compiling after my last edit. Please fix it, but don't change what add() does: negative points are ignored, other points are added to the total, and it returns the new total.",
		verify: verifyScore,
	},
	{
		name:   "add_coin",
		prompt: "Add a coin to the main scene (res://demo/main.tscn): an Area2D called Coin at position (400, 200) with a circle collision shape of radius 8. Save the scene when you're done.",
		verify: verifyCoin,
	},
	{
		name:   "input_jump",
		prompt: `Add a "jump" input action to the project, bound to the Space key.`,
		verify: verifyJump,
	},
	{
		name:   "find_dependents",
		prompt: "I'm thinking about deleting res://demo/player.tscn. Which scenes would break if I did? Don't delete anything, just tell me.",
		verify: verifyDependents,
	},
	{
		name:   "runtime_speed",
		setup:  func(t *testing.T, proj string) { patchMain(t, proj, "$Player.speed = 260.0") },
		prompt: "Run the game and tell me the player's movement speed right after it starts.",
		verify: answerHas("speed", 260, 260),
	},
	{
		name:   "step_frames",
		prompt: "Run the game, hold the right arrow for exactly 30 physics frames, then release it and tell me the player's x position.",
		verify: answerHas("x position", 419, 421),
	},
	{
		name:   "item_resource",
		prompt: `Create a sword item as a reusable ItemData resource file (title "Sword", damage 5) and equip it as the Player's weapon in the main scene. Save the scene.`,
		verify: verifySword,
	},
	{
		name:   "connect_signal",
		prompt: `When the StartButton in the main scene is pressed, Main should print "go". Add a new method to Main's script for this and connect the button's pressed signal to it in the scene, not in code. Save the scene.`,
		verify: verifyStartSignal,
	},
	{
		name:   "rename_node",
		setup:  func(t *testing.T, proj string) { patchMain(t, proj, "$Title.modulate = Color(1, 1, 0.8)") },
		prompt: "Rename the Title label in the main scene to Heading, and make sure nothing breaks. Save the scene.",
		verify: verifyRename,
	},
	{
		name:   "project_settings",
		prompt: `Change the game's base resolution to 1280x720 and rename the project to "Coin Rush".`,
		verify: verifySettings,
	},
}

// The graders' file parser runs without Godot.
func TestParseGodotFile(t *testing.T) {
	b, _ := os.ReadFile(filepath.Join("..", "demo", "main.tscn"))
	sc := parseGodotFile(string(b))
	if ext := sc.extResource(sc.node("Player").attrs["instance"]); ext == nil || ext.attrs["path"] != "res://demo/player.tscn" {
		t.Errorf("Player's instance = %v", ext)
	}
	if p := sc.node("StartButton"); p == nil || p.attrs["parent"] != "." || !near(numbers(p.props["offset_left"]), 20) {
		t.Errorf("StartButton = %v", p)
	}
	b, _ = os.ReadFile(filepath.Join("..", "demo", "player.tscn"))
	player := parseGodotFile(string(b))
	if r := player.subResource(player.node("Shape").props["shape"]); r == nil || r.attrs["type"] != "RectangleShape2D" || !near(numbers(r.props["size"]), 16, 16) {
		t.Errorf("Shape's shape = %v", r)
	}
}

// patchMain adds a line at the start of main.gd's _ready.
func patchMain(t *testing.T, proj, line string) {
	path := filepath.Join(proj, "demo", "main.gd")
	b, _ := os.ReadFile(path)
	const anchor = "\t$StartButton.pressed.connect"
	if !strings.Contains(string(b), anchor) {
		t.Fatalf("main.gd lacks %q", anchor)
	}
	write(t, path, strings.Replace(string(b), anchor, "\t"+line+"\n"+anchor, 1))
}

func writeBrokenScore(t *testing.T, proj string) {
	write(t, filepath.Join(proj, "demo", "score.gd"), `extends Node

var total := 0


func add(points: int) -> int:
	if points < 0
		return total
	total += point
	return total
`)
}

func verifyScore(s *session, _ string) []string {
	problems := s.scriptProblems("res://demo/score.gd")
	write(s.t, filepath.Join(s.proj, "eval_score.gd"), `extends SceneTree
func _initialize() -> void:
	var s: Object = load("res://demo/score.gd").new()
	print("SCORE ", s.call("add", 5), " ", s.call("add", -1), " ", s.call("add", 3))
	s.free()
	quit()
`)
	out, _ := s.godot.Command(s.ctx, "--headless", "--path", s.proj, "--script", "res://eval_score.gd").CombinedOutput()
	if !strings.Contains(string(out), "SCORE 5 5 8") {
		problems = append(problems, "add(5), add(-1), add(3) should return 5, 5, 8; Godot printed: "+oneLine(string(out), 400))
	}
	return problems
}

func verifyCoin(s *session, _ string) []string {
	sc := parseGodotFile(s.read("demo/main.tscn"))
	coin := sc.node("Coin")
	if coin == nil || coin.attrs["type"] != "Area2D" || coin.attrs["parent"] != "." {
		return []string{fmt.Sprintf("main.tscn has no Area2D Coin under the root: %v", coin)}
	}
	var problems []string
	if !near(numbers(coin.props["position"]), 400, 200) {
		problems = append(problems, "Coin's position is "+coin.props["position"]+", want (400, 200)")
	}
	shape := sc.find(func(x *section) bool { return x.attrs["parent"] == "Coin" && x.attrs["type"] == "CollisionShape2D" })
	if shape == nil {
		return append(problems, "Coin has no CollisionShape2D child")
	}
	res := sc.subResource(shape.props["shape"])
	if res == nil || res.attrs["type"] != "CircleShape2D" || !near(numbers(res.props["radius"]), 8) {
		problems = append(problems, fmt.Sprintf("Coin's shape is not a CircleShape2D of radius 8 saved in the scene: %s = %v", shape.props["shape"], res))
	}
	return problems
}

func verifyJump(s *session, _ string) []string {
	text := s.read("project.godot")
	start := strings.Index(text, "\njump={")
	if start < 0 {
		return []string{"project.godot has no jump action"}
	}
	block, _, _ := strings.Cut(text[start+1:], "\n}")
	if !regexp.MustCompile(`"(physical_)?keycode":32\b`).MatchString(block) {
		return []string{"the jump action is not bound to Space: " + oneLine(block, 400)}
	}
	return nil
}

func verifyDependents(s *session, answer string) []string {
	var problems []string
	if !strings.Contains(answer, "main.tscn") {
		problems = append(problems, "the answer does not name main.tscn")
	}
	if _, err := os.Stat(filepath.Join(s.proj, "demo", "player.tscn")); err != nil {
		problems = append(problems, "player.tscn is gone: "+err.Error())
	}
	return problems
}

// answerHas passes when the answer holds a number from lo to hi.
func answerHas(what string, lo, hi float64) func(*session, string) []string {
	return func(_ *session, answer string) []string {
		if slices.ContainsFunc(numbers(answer), func(f float64) bool { return f >= lo && f <= hi }) {
			return nil
		}
		return []string{fmt.Sprintf("the answer has no %s from %g to %g", what, lo, hi)}
	}
}

func verifySword(s *session, _ string) []string {
	sc := parseGodotFile(s.read("demo/main.tscn"))
	player := sc.node("Player")
	if player == nil {
		return []string{"main.tscn has no Player"}
	}
	ext := sc.extResource(player.props["weapon"])
	if ext == nil {
		return []string{"the Player's weapon is not a resource file: weapon = " + player.props["weapon"]}
	}
	path := ext.attrs["path"]
	item := parseGodotFile(s.read(strings.TrimPrefix(path, "res://")))
	res := item.find(func(x *section) bool { return x.kind == "resource" })
	script := item.find(func(x *section) bool { return x.attrs["path"] == "res://demo/item_data.gd" })
	if res == nil || script == nil || res.props["title"] != `"Sword"` || !near(numbers(res.props["damage"]), 5) {
		return []string{fmt.Sprintf("%s is not an ItemData with title Sword and damage 5: %v", path, res)}
	}
	return nil
}

func verifyStartSignal(s *session, _ string) []string {
	sc := parseGodotFile(s.read("demo/main.tscn"))
	conn := sc.find(func(x *section) bool {
		return x.kind == "connection" && x.attrs["signal"] == "pressed" && x.attrs["from"] == "StartButton" && x.attrs["to"] == "."
	})
	if conn == nil {
		return []string{"main.tscn does not connect StartButton's pressed signal to Main"}
	}
	problems := s.scriptProblems("res://demo/main.gd")
	src := s.read("demo/main.gd")
	_, body, found := strings.Cut(src, "func "+conn.attrs["method"]+"(")
	body, _, _ = strings.Cut(body, "\nfunc ")
	if !found || !regexp.MustCompile(`print\(\s*["']go["']\s*\)`).MatchString(body) {
		problems = append(problems, "main.gd has no method "+conn.attrs["method"]+" that prints go")
	}
	return problems
}

func verifyRename(s *session, _ string) []string {
	sc := parseGodotFile(s.read("demo/main.tscn"))
	var problems []string
	if h := sc.node("Heading"); h == nil || h.attrs["type"] != "Label" || h.props["text"] != `"Godot MCP demo"` {
		problems = append(problems, fmt.Sprintf("main.tscn has no Label Heading with the old text: %v", h))
	}
	if sc.node("Title") != nil {
		problems = append(problems, "main.tscn still has a Title node")
	}
	if src := s.read("demo/main.gd"); strings.Contains(src, "Title") || !strings.Contains(src, "Heading") {
		problems = append(problems, "main.gd still uses Title instead of Heading")
	}
	return problems
}

func verifySettings(s *session, _ string) []string {
	text := s.read("project.godot")
	var problems []string
	for _, want := range []string{"window/size/viewport_width=1280", "window/size/viewport_height=720", `config/name="Coin Rush"`} {
		if !slices.Contains(strings.Split(text, "\n"), want) {
			problems = append(problems, "project.godot lacks "+want)
		}
	}
	return problems
}

// read returns a project file, empty when it is missing.
func (s *session) read(rel string) string {
	b, _ := os.ReadFile(filepath.Join(s.proj, rel))
	return string(b)
}

// scriptProblems runs script_check on one script through the grader's own
// server connection.
func (s *session) scriptProblems(path string) []string {
	text, isErr := s.call("script_check", map[string]any{"paths": []any{path}})
	var r struct{ OK bool }
	if isErr || json.Unmarshal([]byte(text), &r) != nil || !r.OK {
		return []string{"script_check " + path + ": " + oneLine(text, 400)}
	}
	return nil
}

var numberRE = regexp.MustCompile(`-?\b\d+(\.\d+)?`)

// numbers reads every number in s that is not part of a word, so
// "Vector2(400, 200)" gives 400 and 200.
func numbers(s string) []float64 {
	var out []float64
	for _, m := range numberRE.FindAllString(s, -1) {
		f, _ := strconv.ParseFloat(m, 64)
		out = append(out, f)
	}
	return out
}

func near(got []float64, want ...float64) bool {
	return slices.EqualFunc(got, want, func(a, b float64) bool { return math.Abs(a-b) < 1e-3 })
}

// section is one [header] of a .tscn, .tres or project.godot file with the
// "key = value" lines under it.
type section struct {
	kind  string
	attrs map[string]string
	props map[string]string
}

type godotFile []*section

var (
	headerRE = regexp.MustCompile(`^\[(\w+)(.*)\]$`)
	attrRE   = regexp.MustCompile(`(\w+)=("([^"]*)"|[^\s\]]+)`)
)

// parseGodotFile reads the sections of a Godot text resource; values that
// span several lines keep only their first line.
func parseGodotFile(text string) godotFile {
	var f godotFile
	for _, line := range strings.Split(text, "\n") {
		if m := headerRE.FindStringSubmatch(line); m != nil {
			sec := &section{kind: m[1], attrs: map[string]string{}, props: map[string]string{}}
			for _, a := range attrRE.FindAllStringSubmatch(m[2], -1) {
				sec.attrs[a[1]] = strings.Trim(a[2], `"`)
			}
			f = append(f, sec)
		} else if key, value, ok := strings.Cut(line, " = "); ok && len(f) > 0 {
			f[len(f)-1].props[key] = value
		}
	}
	return f
}

func (f godotFile) find(match func(*section) bool) *section {
	if i := slices.IndexFunc(f, match); i >= 0 {
		return f[i]
	}
	return nil
}

func (f godotFile) node(name string) *section {
	return f.find(func(x *section) bool { return x.kind == "node" && x.attrs["name"] == name })
}

// subResource and extResource look up the section a SubResource("id") or
// ExtResource("id") value points at.
func (f godotFile) subResource(ref string) *section {
	return f.byID("sub_resource", ref, "SubResource")
}

func (f godotFile) extResource(ref string) *section {
	return f.byID("ext_resource", ref, "ExtResource")
}

func (f godotFile) byID(kind, ref, fn string) *section {
	id, ok := strings.CutPrefix(ref, fn+`("`)
	id, _ = strings.CutSuffix(id, `")`)
	return f.find(func(x *section) bool { return ok && x.kind == kind && x.attrs["id"] == id })
}
