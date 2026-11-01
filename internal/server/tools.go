package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/orebron/godot-mcp/internal/game"
	"github.com/orebron/godot-mcp/internal/godot"
)

func ptr[T any](v T) *T { return &v }

// Annotation presets.
var (
	readOnly    = &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: ptr(false)}
	editorEdit  = &mcp.ToolAnnotations{DestructiveHint: ptr(false), OpenWorldHint: ptr(false)}
	destructive = &mcp.ToolAnnotations{DestructiveHint: ptr(true), OpenWorldHint: ptr(false)}
)

func (s *Server) registerTools() {
	s.addProjectTools()
	s.addGameTools()
	s.addEditorTools()
}

// ---- project and scripts (no editor needed) ----

type projectInfoIn struct{}

type scriptCheckIn struct {
	Paths         []string `json:"paths,omitempty" jsonschema:"res:// or project-relative paths of .gd files to check. Leave empty to check every script outside addons/."`
	IncludeAddons bool     `json:"include_addons,omitempty" jsonschema:"also check scripts under addons/ when paths is empty"`
}

func (s *Server) addProjectTools() {
	mcp.AddTool(s.mcp, &mcp.Tool{
		Name: "project_info",
		Description: "Start here. Reports the Godot project this server works on (name, folder, main scene, autoloads), " +
			"the Godot binary and its version, whether the editor plugin is connected (scene_*, node_* and editor_* tools need it), " +
			"and whether a game started by game_run is running.",
		Annotations: readOnly,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ projectInfoIn) (*mcp.CallToolResult, any, error) {
		out := map[string]any{"server_version": Version}
		p, err := s.project()
		if err != nil {
			out["project"] = map[string]any{"error": err.Error()}
		} else {
			out["project"] = map[string]any{"name": p.Name, "folder": p.Dir, "main_scene": p.MainScene, "autoloads": p.Autoloads}
		}
		if e, v, err := s.godotExec(ctx); err != nil {
			out["godot"] = map[string]any{"error": err.Error()}
		} else {
			out["godot"] = map[string]any{"path": e.String(), "found_via": e.Source, "version": v.Raw}
		}
		if p != nil {
			if hello, err := s.bridge.Connect(ctx); err != nil {
				out["editor"] = map[string]any{"connected": false, "reason": err.Error(), "how_to_connect": editorHelp}
			} else {
				ed := map[string]any{"connected": true, "godot_version": hello.GodotVersion, "plugin_version": hello.PluginVersion}
				if state, err := s.bridge.Call(ctx, "ping", nil, 5*time.Second); err == nil {
					ed["state"] = json.RawMessage(state)
				}
				out["editor"] = ed
			}
		}
		out["game"] = s.runner.Status()
		r, err := jsonResult(out)
		return r, nil, err
	})

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name: "script_check",
		Description: "Parse-check GDScript files with the project's own Godot, so types, autoloads and class_name globals resolve exactly as in the game. " +
			"Run it after creating or editing .gd files. Godot exits with 0 even when a script is broken, so use this result instead of an exit code. " +
			"Returns only the failing scripts, each with res:// file, line and message.",
		Annotations: readOnly,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in scriptCheckIn) (*mcp.CallToolResult, any, error) {
		p, err := s.project()
		if err != nil {
			return nil, nil, err
		}
		e, _, err := s.godotExec(ctx)
		if err != nil {
			return nil, nil, err
		}
		var scripts []string
		if len(in.Paths) == 0 {
			if scripts, err = godot.ListScripts(p, in.IncludeAddons); err != nil {
				return nil, nil, err
			}
		}
		for _, path := range in.Paths {
			res, err := p.ToRes(path)
			if err != nil {
				return nil, nil, err
			}
			if !strings.HasSuffix(strings.ToLower(res), ".gd") {
				return nil, nil, fmt.Errorf("%s is not a .gd file; script_check only parses GDScript", res)
			}
			if err := p.CheckCase(res); err != nil {
				return nil, nil, err
			}
			scripts = append(scripts, res)
		}

		var notes []string
		// New class_name scripts are only known after Godot rescans. Let the
		// open editor do it (it owns .godot/); otherwise import headless.
		if _, err := s.bridge.Call(ctx, "filesystem_scan", nil, 2*time.Minute); err == nil {
			notes = append(notes, "the open editor rescanned the project first")
		} else if godot.ClassCacheStale(p) {
			if err := e.Import(ctx, p); err != nil {
				notes = append(notes, "refreshing the class_name cache failed, so class_name lookups may be wrong: "+err.Error())
			} else {
				notes = append(notes, "ran godot --import first to refresh the class_name cache")
			}
		}

		res, err := e.CheckScripts(ctx, p, scripts)
		if err != nil {
			return nil, nil, err
		}
		failures := []godot.ScriptResult{}
		for _, sr := range res.Scripts {
			if !sr.OK {
				failures = append(failures, sr)
			}
		}
		r, err := jsonResult(map[string]any{
			"ok": res.OK, "checked": res.Checked, "failed": res.Failed,
			"failures": failures, "other_errors": res.Other, "notes": append(notes, res.Notes...),
		})
		return r, nil, err
	})
}

// ---- running the game ----

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
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in gameRunIn) (*mcp.CallToolResult, any, error) {
		p, err := s.project()
		if err != nil {
			return nil, nil, err
		}
		e, _, err := s.godotExec(ctx)
		if err != nil {
			return nil, nil, err
		}
		scene := ""
		if in.Scene != "" {
			if scene, err = p.ToRes(in.Scene); err != nil {
				return nil, nil, err
			}
			if !strings.HasPrefix(scene, "uid://") {
				if err := p.CheckCase(scene); err != nil {
					return nil, nil, err
				}
			}
		} else if p.MainScene == "" {
			return nil, nil, errors.New("the project has no main scene (application/run/main_scene); pass scene with a res:// path to a .tscn")
		}
		wait := 3.0
		if in.WaitSeconds != nil {
			wait = min(max(*in.WaitSeconds, 0), 30)
		}
		// The game starts through a harness scene in the project's data folder,
		// which adds the bridge for the game_* tools and then changes to the
		// requested scene. GODOT_MCP_SCENE empty means the main scene.
		runDir := filepath.Join(p.MCPDir(), "run")
		runRes, err := p.ToRes(runDir)
		if err != nil {
			return nil, nil, err
		}
		launch, err := game.InstallHarness(runDir, runRes)
		if err != nil {
			return nil, nil, fmt.Errorf("writing the game harness: %w", err)
		}
		link, err := game.NewLink()
		if err != nil {
			return nil, nil, err
		}
		opts := game.StartOptions{
			Scene:    launch,
			Headless: in.Headless,
			Args:     in.Args,
			Env:      append(link.Env(), "GODOT_MCP_SCENE="+scene),
			Link:     link,
		}
		if err := s.runner.Start(e, p.Dir, opts); err != nil {
			link.Close()
			if errors.Is(err, game.ErrRunning) {
				return nil, nil, errors.New("a game is already running; call game_stop first, or game_output to read it")
			}
			return nil, nil, err
		}
		s.runner.Wait(ctx, time.Duration(wait*float64(time.Second)))
		r, err := jsonResult(s.outputPayload(0, 200, false))
		return r, nil, err
	})

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name: "game_output",
		Description: "Read output and parsed errors (with res:// file and line) of the game started by game_run, also after it exited. " +
			"Pass next_since from the previous call to read only new lines.",
		Annotations: readOnly,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in gameOutputIn) (*mcp.CallToolResult, any, error) {
		r, err := jsonResult(s.outputPayload(in.Since, in.MaxLines, in.ErrorsOnly))
		return r, nil, err
	})

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name:        "game_stop",
		Description: "Stop the game started by game_run, including any processes it started. Its output stays readable with game_output.",
		Annotations: &mcp.ToolAnnotations{DestructiveHint: ptr(false), IdempotentHint: true, OpenWorldHint: ptr(false)},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ gameStopIn) (*mcp.CallToolResult, any, error) {
		stopped, err := s.runner.Stop()
		if err != nil {
			return nil, nil, err
		}
		r, err := jsonResult(map[string]any{"stopped": stopped, "status": s.runner.Status()})
		return r, nil, err
	})
}

// ---- editor (needs the plugin) ----

type sceneTreeIn struct {
	NodePath string `json:"node_path,omitempty" jsonschema:"node to start from, relative to the scene root (default \".\", the root)"`
	MaxDepth int    `json:"max_depth,omitempty" jsonschema:"levels below node_path to include (default 4); deeper nodes report only child_count"`
}

type nodeGetIn struct {
	NodePath        string   `json:"node_path" jsonschema:"node relative to the scene root, e.g. \".\" or \"Player/Sprite2D\""`
	Properties      []string `json:"properties,omitempty" jsonschema:"property names to read; empty returns every stored property that differs from its default"`
	IncludeDefaults bool     `json:"include_defaults,omitempty" jsonschema:"with empty properties, also return properties that still have their default value"`
}

type sceneOpenIn struct {
	Path string `json:"path" jsonschema:"res:// path of a .tscn or .scn file"`
}

type sceneCreateIn struct {
	Path     string `json:"path" jsonschema:"res:// path for the new .tscn file; it must not exist yet"`
	RootType string `json:"root_type" jsonschema:"class of the root node, e.g. Node2D, Node3D, Control, CharacterBody2D"`
	RootName string `json:"root_name,omitempty" jsonschema:"name of the root node (default: the file name in PascalCase)"`
}

type sceneSaveIn struct {
	Path string `json:"path,omitempty" jsonschema:"res:// path to save the edited scene as a new file; empty saves it in place"`
}

type nodeAddIn struct {
	ParentPath string         `json:"parent_path,omitempty" jsonschema:"parent node relative to the scene root (default \".\", the root)"`
	Type       string         `json:"type,omitempty" jsonschema:"class to create, e.g. Sprite2D, CollisionShape2D, Label. Give type or scene, not both."`
	Scene      string         `json:"scene,omitempty" jsonschema:"res:// path of a .tscn to instance as a child, instead of type"`
	Name       string         `json:"name,omitempty" jsonschema:"node name (default: the class or scene name); Godot appends a number if it is taken"`
	Properties map[string]any `json:"properties,omitempty" jsonschema:"properties to set, same value format as node_set_properties"`
}

type nodeSetPropertiesIn struct {
	NodePath   string         `json:"node_path" jsonschema:"node relative to the scene root, e.g. \".\" or \"Player/Sprite2D\""`
	Properties map[string]any `json:"properties" jsonschema:"property name to value. Numbers, strings and booleans as JSON; vectors as [x, y] or {\"x\":..,\"y\":..}; colours as \"#ff8800\" or [r, g, b, a]; resources and scripts as res:// paths; anything else in Godot's text form, e.g. \"Rect2(0, 0, 16, 16)\". Nested values use a colon path, e.g. \"shape:size\"."`
}

type nodeDeleteIn struct {
	NodePath string `json:"node_path" jsonschema:"node to delete with all of its children, relative to the scene root; the root itself cannot be deleted"`
}

type screenshotIn struct {
	Viewport string `json:"viewport,omitempty" jsonschema:"\"2d\", \"3d\" or \"auto\" (default: 3d when the scene root is a Node3D, else 2d)"`
	MaxWidth int    `json:"max_width,omitempty" jsonschema:"scale the image down to at most this many pixels wide (default 1280)"`
}

// addEditorTool registers a bridge tool whose input schema comes from In.
func addEditorTool[In any](s *Server, t *mcp.Tool, command string, timeout time.Duration, resPaths ...string) {
	mcp.AddTool(s.mcp, t, func(ctx context.Context, req *mcp.CallToolRequest, in In) (*mcp.CallToolResult, any, error) {
		var params map[string]any
		b, _ := json.Marshal(in)
		if err := json.Unmarshal(b, &params); err != nil {
			return nil, nil, err
		}
		if err := s.normalizePaths(params, resPaths); err != nil {
			return nil, nil, err
		}
		data, err := s.call(ctx, command, params, timeout)
		if err != nil {
			return nil, nil, err
		}
		return textResult(data), nil, nil
	})
}

// normalizePaths turns the named path parameters into clean res:// paths and
// checks the letter case of the parts that already exist.
func (s *Server) normalizePaths(params map[string]any, keys []string) error {
	p, err := s.project()
	if err != nil {
		return err
	}
	for _, k := range keys {
		v, _ := params[k].(string)
		if v == "" {
			continue
		}
		res, err := p.ToRes(v)
		if err != nil {
			return fmt.Errorf("%s: %w", k, err)
		}
		if !strings.HasPrefix(res, "uid://") {
			if err := p.CheckCaseExisting(res); err != nil {
				return fmt.Errorf("%s: %w", k, err)
			}
		}
		params[k] = res
	}
	return nil
}

func (s *Server) addEditorTools() {
	addEditorTool[sceneTreeIn](s, &mcp.Tool{
		Name: "scene_tree",
		Description: "Show the node tree of the scene open in the editor: name, class, path, attached script and instanced scene of each node. " +
			"Use the returned paths as node_path in other tools.",
		Annotations: readOnly,
	}, "scene_tree", 15*time.Second)

	addEditorTool[nodeGetIn](s, &mcp.Tool{
		Name:        "node_get",
		Description: "Read properties of a node in the edited scene. By default returns only stored properties that differ from the class default, which keeps the answer short.",
		Annotations: readOnly,
	}, "node_get", 15*time.Second)

	addEditorTool[sceneOpenIn](s, &mcp.Tool{
		Name:        "scene_open",
		Description: "Open a scene in the editor and make it the edited scene; scene_tree and node_* tools then work on it.",
		Annotations: &mcp.ToolAnnotations{DestructiveHint: ptr(false), IdempotentHint: true, OpenWorldHint: ptr(false)},
	}, "scene_open", time.Minute, "path")

	addEditorTool[sceneCreateIn](s, &mcp.Tool{
		Name:        "scene_create",
		Description: "Create a new scene file with one root node, save it and open it in the editor.",
		Annotations: editorEdit,
	}, "scene_create", time.Minute, "path")

	addEditorTool[sceneSaveIn](s, &mcp.Tool{
		Name:        "scene_save",
		Description: "Save the edited scene to disk. Node edits change nothing on disk until this is called.",
		Annotations: &mcp.ToolAnnotations{DestructiveHint: ptr(true), IdempotentHint: true, OpenWorldHint: ptr(false)},
	}, "scene_save", time.Minute, "path")

	addEditorTool[nodeAddIn](s, &mcp.Tool{
		Name:        "node_add",
		Description: "Add a node (by class) or an instanced scene (by res:// path) under a parent in the edited scene, optionally setting properties. The user can undo it with Ctrl+Z.",
		Annotations: editorEdit,
	}, "node_add", 15*time.Second, "scene")

	addEditorTool[nodeSetPropertiesIn](s, &mcp.Tool{
		Name:        "node_set_properties",
		Description: "Set one or more properties of a node in the edited scene as a single undoable action. Attach a script with {\"script\": \"res://player.gd\"}.",
		Annotations: &mcp.ToolAnnotations{DestructiveHint: ptr(false), IdempotentHint: true, OpenWorldHint: ptr(false)},
	}, "node_set_properties", 15*time.Second)

	addEditorTool[nodeDeleteIn](s, &mcp.Tool{
		Name:        "node_delete",
		Description: "Delete a node and its children from the edited scene. The user can undo it with Ctrl+Z until the scene is closed.",
		Annotations: destructive,
	}, "node_delete", 15*time.Second)

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name:        "editor_screenshot",
		Description: "Capture the editor's 2D or 3D viewport as an image, to check layout and visuals of the edited scene. Not available when the editor runs headless.",
		Annotations: readOnly,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in screenshotIn) (*mcp.CallToolResult, any, error) {
		data, err := s.call(ctx, "editor_screenshot", in, 20*time.Second)
		if err != nil {
			return nil, nil, err
		}
		var shot struct {
			PNG      string `json:"png_base64"`
			Width    int    `json:"width"`
			Height   int    `json:"height"`
			Viewport string `json:"viewport"`
		}
		if err := json.Unmarshal(data, &shot); err != nil {
			return nil, nil, err
		}
		png, err := base64.StdEncoding.DecodeString(shot.PNG)
		if err != nil {
			return nil, nil, fmt.Errorf("the plugin sent an invalid image: %w", err)
		}
		meta := fmt.Sprintf(`{"viewport":%q,"width":%d,"height":%d}`, shot.Viewport, shot.Width, shot.Height)
		return &mcp.CallToolResult{Content: []mcp.Content{
			&mcp.ImageContent{Data: png, MIMEType: "image/png"},
			&mcp.TextContent{Text: meta},
		}}, nil, nil
	})
}
