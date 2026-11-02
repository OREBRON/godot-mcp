package server

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/orebron/godot-mcp/internal/game"
	"github.com/orebron/godot-mcp/internal/project"
)

type gameRunIn struct {
	Scene       string   `json:"scene,omitempty" jsonschema:"res:// path of the scene to run. Empty runs the project's main scene."`
	Headless    bool     `json:"headless,omitempty" jsonschema:"run without a window and without rendering: fast for logic tests and CI, but nothing is drawn"`
	WaitSeconds *float64 `json:"wait_seconds,omitempty" jsonschema:"seconds to wait before returning the first output (0 to 30, default 3). Returns early if the game exits."`
	Args        []string `json:"args,omitempty" jsonschema:"extra arguments for the game, readable with OS.get_cmdline_user_args()"`
}

type gameOutputIn struct {
	Since      int  `json:"since,omitempty" jsonschema:"line number to start from: pass next_since from the previous call to get only new lines (default 0)"`
	MaxLines   int  `json:"max_lines,omitempty" jsonschema:"most lines to return (default 200, at most 2000)"`
	ErrorsOnly bool `json:"errors_only,omitempty" jsonschema:"return only parsed errors and warnings, not the raw lines"`
}

type gameStopIn struct{}

func (s *Server) outputPayload(since, max int, errorsOnly bool) map[string]any {
	if max <= 0 {
		max = 200
	}
	if max > 2000 {
		max = 2000
	}
	lines, next, dropped := s.runner.Output(since, max)
	texts := make([]string, len(lines))
	for i, l := range lines {
		texts[i] = l.Text
	}
	issues := game.Issues(lines)
	out := map[string]any{"status": s.runner.Status(), "next_since": next, "errors": issues}
	if !errorsOnly {
		out["lines"] = texts
	}
	if dropped > 0 {
		out["dropped_lines"] = dropped
	}
	return out
}

func (s *Server) addGameTools() {
	mcp.AddTool(s.mcp, &mcp.Tool{
		Name: "game_run",
		Description: "Run the project (or one scene) as a separate Godot process and return its first output and parsed errors. " +
			"Only one game runs at a time: call game_stop first to restart. The game keeps running after this returns; read more with game_output. " +
			"Output comes straight from the process, so it is complete and current. " +
			"status.bridge becomes \"connected\" once the game can be driven with the other game_* tools.",
		Annotations: &mcp.ToolAnnotations{DestructiveHint: ptr(false), OpenWorldHint: ptr(false)},
	}, s.gameRun)
	mcp.AddTool(s.mcp, &mcp.Tool{
		Name: "game_output",
		Description: "Read output and parsed errors (with res:// file and line) of the game started by game_run, also after it exited. " +
			"Pass next_since from the previous call to read only new lines.",
		Annotations: readOnly,
	}, s.gameOutput)
	mcp.AddTool(s.mcp, &mcp.Tool{
		Name:        "game_stop",
		Description: "Stop the game started by game_run, including any processes it started. Its output stays readable with game_output.",
		Annotations: harmless,
	}, s.gameStop)
}

func (s *Server) gameRun(ctx context.Context, _ *mcp.CallToolRequest, in gameRunIn) (*mcp.CallToolResult, any, error) {
	p, err := s.project()
	if err != nil {
		return nil, nil, err
	}
	e, _, err := s.godotExec(ctx)
	if err != nil {
		return nil, nil, err
	}
	scene, err := runScene(p, in.Scene)
	if err != nil {
		return nil, nil, err
	}
	opts, err := harnessOptions(p, scene, in)
	if err != nil {
		return nil, nil, err
	}
	if err := s.runner.Start(e, p.Dir, opts); err != nil {
		opts.Link.Close()
		if errors.Is(err, game.ErrRunning) {
			err = errors.New("a game is already running; call game_stop first, or game_output to read it")
		}
		return nil, nil, err
	}
	s.runner.Wait(ctx, waitTime(in.WaitSeconds))
	r, err := jsonResult(s.outputPayload(0, 200, false))
	return r, nil, err
}

// runScene checks the requested scene; empty means the main scene, which
// must then exist.
func runScene(p *project.Project, requested string) (string, error) {
	if requested == "" {
		if p.MainScene == "" {
			return "", errors.New("the project has no main scene (application/run/main_scene); pass scene with a res:// path to a .tscn")
		}
		return "", nil
	}
	scene, err := p.ToRes(requested)
	if err == nil && !strings.HasPrefix(scene, "uid://") {
		err = p.CheckCase(scene)
	}
	return scene, err
}

// harnessOptions starts the game through a harness scene in the project's
// data folder, which adds the bridge for the game_* tools and then changes
// to the requested scene (GODOT_MCP_SCENE empty means the main scene).
func harnessOptions(p *project.Project, scene string, in gameRunIn) (game.StartOptions, error) {
	runDir := filepath.Join(p.MCPDir(), "run")
	runRes, err := p.ToRes(runDir)
	if err != nil {
		return game.StartOptions{}, err
	}
	launch, err := game.InstallHarness(runDir, runRes)
	if err != nil {
		return game.StartOptions{}, fmt.Errorf("writing the game harness: %w", err)
	}
	link, err := game.NewLink()
	if err != nil {
		return game.StartOptions{}, err
	}
	return game.StartOptions{
		Scene:    launch,
		Headless: in.Headless,
		Args:     in.Args,
		Env:      append(link.Env(), "GODOT_MCP_SCENE="+scene),
		Link:     link,
	}, nil
}

func waitTime(seconds *float64) time.Duration {
	wait := 3.0
	if seconds != nil {
		wait = min(max(*seconds, 0), 30)
	}
	return time.Duration(wait * float64(time.Second))
}

func (s *Server) gameOutput(ctx context.Context, _ *mcp.CallToolRequest, in gameOutputIn) (*mcp.CallToolResult, any, error) {
	r, err := jsonResult(s.outputPayload(in.Since, in.MaxLines, in.ErrorsOnly))
	return r, nil, err
}

func (s *Server) gameStop(ctx context.Context, _ *mcp.CallToolRequest, _ gameStopIn) (*mcp.CallToolResult, any, error) {
	stopped, err := s.runner.Stop()
	if err != nil {
		return nil, nil, err
	}
	r, err := jsonResult(map[string]any{"stopped": stopped, "status": s.runner.Status()})
	return r, nil, err
}

// addLiveGameTools registers the tools that work inside the running game,
// through the bridge that game_run's harness adds to it.
func (s *Server) addLiveGameTools() {
	addGameTool[gameSceneTreeIn](s, &mcp.Tool{
		Name: "game_scene_tree",
		Description: "Show the live node tree of the game started by game_run: what exists right now, including nodes created at runtime. " +
			"Paths are relative to the current scene; absolute paths such as \"/root/Game\" reach autoloads. Read values with game_node_get.",
		Annotations: readOnly,
	}, "scene_tree", 15*time.Second)
	addGameTool[gameNodeGetIn](s, &mcp.Tool{
		Name: "game_node_get",
		Description: "Read properties of a node in the running game, such as a player's position or a script variable like health. " +
			"Without properties, returns the stored properties and script variables that differ from their defaults.",
		Annotations: readOnly,
	}, "node_get", 15*time.Second)
	addGameTool[gameNodeSetIn](s, &mcp.Tool{
		Name: "game_node_set",
		Description: "Set properties of a node in the running game to set up a situation to test, such as moving the player next to a door or lowering its health. " +
			"All values are checked first, so a bad one changes nothing. The change lives only in the running game; nothing is saved.",
		Annotations: harmless,
	}, "node_set", 15*time.Second)
}

// addGameControlTools registers the tools that act on the running game's
// time and input, and measure or capture it.
func (s *Server) addGameControlTools() {
	addGameTool[gameTimeIn](s, &mcp.Tool{
		Name: "game_time",
		Description: "Pause, resume or step the game started by game_run. step runs exactly the given number of physics frames and pauses again, " +
			"so what you read afterwards with game_node_get is the same on every run. Input sent while paused takes effect on the next step.",
		Annotations: &mcp.ToolAnnotations{DestructiveHint: ptr(false), OpenWorldHint: ptr(false)},
	}, "time", time.Minute)
	addGameTool[gameInputIn](s, &mcp.Tool{
		Name: "game_input",
		Description: "Send input to the game started by game_run: an InputMap action, a key, a mouse click or a mouse move. " +
			"Give exactly one of action, key, or mouse_button with position (position alone moves the mouse). " +
			"By default the input is pressed, held for duration and released. Paused nodes receive no input: " +
			"to watch input act frame by frame, send it with pressed true, call game_time step, then send pressed false.",
		Annotations: &mcp.ToolAnnotations{DestructiveHint: ptr(false), OpenWorldHint: ptr(false)},
	}, "input", time.Minute)
	addGameTool[gamePerformanceIn](s, &mcp.Tool{
		Name: "game_performance",
		Description: "Measure the game started by game_run: frame rate over sample_seconds of real time, process and physics time, node and orphan-node counts, memory and draw calls. " +
			"Measure a windowed game: a headless one is paced by Godot's sleep and draws nothing, so its numbers say little.",
		Annotations: readOnly,
	}, "performance", 30*time.Second)
	addGameTool[gameWaitIn](s, &mcp.Tool{
		Name: "game_wait",
		Description: "Wait in the game started by game_run until node_path exists, until its signal is emitted, or until its property equals a value, for up to timeout_seconds (default 5). " +
			"A timeout returns met: false rather than an error. The game must be running, not paused; other game_* calls wait until this one returns.",
		Annotations: readOnly,
	}, "wait", 70*time.Second)
	mcp.AddTool(s.mcp, &mcp.Tool{
		Name: "game_screenshot",
		Description: "Capture the current frame of the game started by game_run, to see what the player sees. " +
			"Needs a game started without headless. Also reports logical_width and logical_height, the coordinate space " +
			"game_input positions use; on a HiDPI screen the image has more pixels than that.",
		Annotations: readOnly,
	}, s.gameScreenshot)
}

func (s *Server) gameScreenshot(ctx context.Context, _ *mcp.CallToolRequest, in gameScreenshotIn) (*mcp.CallToolResult, any, error) {
	data, err := s.gameCall(ctx, "screenshot", in, 20*time.Second)
	if err != nil {
		return nil, nil, err
	}
	r, err := imageResult(data)
	return r, nil, err
}

type gameInputIn struct {
	Action      string    `json:"action,omitempty" jsonschema:"an InputMap action, e.g. \"jump\" or \"ui_accept\""`
	Key         string    `json:"key,omitempty" jsonschema:"a key by Godot name, e.g. \"Space\", \"Enter\", \"A\", \"Ctrl+S\""`
	MouseButton string    `json:"mouse_button,omitempty" jsonschema:"\"left\", \"right\", \"middle\", \"wheel_up\" or \"wheel_down\"; needs position"`
	Position    []float64 `json:"position,omitempty" jsonschema:"[x, y] in the game's viewport coordinates; on its own it moves the mouse there"`
	Duration    *float64  `json:"duration,omitempty" jsonschema:"seconds to hold before releasing (default 0.1)"`
	Pressed     *bool     `json:"pressed,omitempty" jsonschema:"send only the press (true) or only the release (false), to hold an input across calls such as game_time steps"`
}

type gameWaitIn struct {
	NodePath       string   `json:"node_path" jsonschema:"node to wait for or on, relative to the current scene or absolute such as \"/root/Game\""`
	Signal         string   `json:"signal,omitempty" jsonschema:"wait until the node emits this signal"`
	Property       string   `json:"property,omitempty" jsonschema:"wait until this property equals equals"`
	Equals         any      `json:"equals,omitempty" jsonschema:"the value property must reach, in game_node_set's value format"`
	TimeoutSeconds *float64 `json:"timeout_seconds,omitempty" jsonschema:"seconds of real time to wait at most (default 5, at most 60)"`
}

type gamePerformanceIn struct {
	SampleSeconds *float64 `json:"sample_seconds,omitempty" jsonschema:"seconds of real time to count frames over (default 1, 0.1 to 10)"`
}

type gameNodeSetIn struct {
	NodePath   string         `json:"node_path" jsonschema:"node relative to the current scene, e.g. \"Player\", or absolute such as \"/root/Game\""`
	Properties map[string]any `json:"properties" jsonschema:"property to value: numbers, strings and booleans as JSON, [x, y] or [x, y, z] for vectors, anything else in Godot's text form such as \"Color(1, 0, 0, 1)\"; a colon reaches into a resource, e.g. \"shape:size\""`
}

type gameScreenshotIn struct {
	MaxWidth int `json:"max_width,omitempty" jsonschema:"scale the image down to at most this many pixels wide (default 1280)"`
}

type gameTimeIn struct {
	Action string `json:"action" jsonschema:"\"pause\" freezes the game, \"resume\" lets it run, \"step\" runs exactly frames physics frames and pauses again, \"status\" only reports"`
	Frames int    `json:"frames,omitempty" jsonschema:"physics frames to run for step (default 1, at most 600)"`
}

type gameSceneTreeIn struct {
	NodePath string `json:"node_path,omitempty" jsonschema:"node to start from: relative to the current scene (\".\", the default, is its root) or absolute such as \"/root/Game\" for an autoload"`
	MaxDepth int    `json:"max_depth,omitempty" jsonschema:"levels below node_path to include (default 4)"`
}

type gameNodeGetIn struct {
	NodePath   string   `json:"node_path" jsonschema:"node relative to the current scene, e.g. \"Player\", or absolute such as \"/root/Game\""`
	Properties []string `json:"properties,omitempty" jsonschema:"properties to read, e.g. [\"position\", \"velocity\"]; a colon reads into a resource, e.g. \"shape:size\""`
}
