// Package server wires the MCP tools to the project, the Godot binary, the
// game runner and the editor bridge.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/orebron/godot-mcp/internal/bridge"
	"github.com/orebron/godot-mcp/internal/game"
	"github.com/orebron/godot-mcp/internal/godot"
	"github.com/orebron/godot-mcp/internal/project"
)

// Version is set at build time with -ldflags "-X .../server.Version=v1.2.3".
var Version = "dev"

const instructions = `Godot MCP drives a Godot 4 project.
- Call project_info first: it shows the project, the Godot version and whether the editor plugin is connected.
- Scripts: write .gd files with your own file tools, then run script_check. Godot exits with 0 even when a script is broken, so trust script_check, not an exit code.
- Scenes: edit through scene_* and node_* tools while the editor is open. They go through the editor's undo history, so the user can revert them with Ctrl+Z, and change nothing on disk until scene_save. To revert your own change, make the opposite change (node_delete what you added, set properties back).
- Do not edit a .tscn file by hand while the editor has it open; the editor's next save overwrites it.
- Verify: game_run starts the game and returns its first output and errors; game_output reads more; editor_screenshot shows the editor viewport.
- While a game started by game_run runs, game_scene_tree and game_node_get show its live state, game_input sends it actions, keys and clicks, game_screenshot shows its frame (not when headless), and game_time pauses it or steps it frame by frame for checks that come out the same every time.
- Paths are res:// paths. Node paths are relative to the edited scene's root ("." is the root, "Player/Sprite2D" a child).`

// Options configure a server.
type Options struct {
	ProjectDir string // folder holding project.godot; empty means not found
	ProjectErr error  // why ProjectDir is empty
	GodotPath  string // explicit Godot binary, overrides discovery
}

// Server is the godot-mcp server state.
type Server struct {
	mcp    *mcp.Server
	opts   Options
	runner *game.Runner

	mu      sync.Mutex
	proj    *project.Project
	bridge  *bridge.Client
	exec    *godot.Exec
	version *godot.Version
}

// New creates the server and registers all tools.
func New(opts Options) *Server {
	s := &Server{
		opts:   opts,
		runner: game.New(),
		mcp: mcp.NewServer(
			&mcp.Implementation{Name: "godot-mcp", Title: "Godot MCP", Version: Version},
			&mcp.ServerOptions{Instructions: instructions, Capabilities: &mcp.ServerCapabilities{}},
		),
	}
	s.registerTools()
	return s
}

// Run serves MCP over stdin/stdout until the client disconnects.
func (s *Server) Run(ctx context.Context) error {
	defer s.shutdown()
	return s.mcp.Run(ctx, &mcp.StdioTransport{})
}

// MCP exposes the underlying server, for tests with in-memory transports.
func (s *Server) MCP() *mcp.Server { return s.mcp }

func (s *Server) shutdown() {
	s.runner.Stop()
	s.mu.Lock()
	b := s.bridge
	s.mu.Unlock()
	if b != nil {
		b.Close()
	}
}

func (s *Server) project() (*project.Project, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.proj != nil {
		return s.proj, nil
	}
	if s.opts.ProjectDir == "" {
		err := s.opts.ProjectErr
		if err == nil {
			err = project.ErrNotFound
		}
		return nil, fmt.Errorf("%v. Start the server inside the project folder or pass --project <folder holding project.godot>", err)
	}
	p, err := project.Load(s.opts.ProjectDir)
	if err != nil {
		return nil, fmt.Errorf("reading project.godot in %s: %w", s.opts.ProjectDir, err)
	}
	s.proj = p
	s.bridge = bridge.New(p.MCPDir())
	return p, nil
}

func (s *Server) godotExec(ctx context.Context) (godot.Exec, godot.Version, error) {
	s.mu.Lock()
	if s.exec != nil {
		e, v := *s.exec, *s.version
		s.mu.Unlock()
		return e, v, nil
	}
	s.mu.Unlock()
	e, err := godot.Locate(s.opts.GodotPath)
	if err != nil {
		return godot.Exec{}, godot.Version{}, err
	}
	v, err := e.GetVersion(ctx)
	if err != nil {
		return e, godot.Version{}, err
	}
	if !v.AtLeast(godot.MinMajor, godot.MinMinor) {
		return e, v, fmt.Errorf("Godot %s at %s is too old: godot-mcp needs %d.%d or newer", v.Raw, e.Path, godot.MinMajor, godot.MinMinor)
	}
	s.mu.Lock()
	s.exec, s.version = &e, &v
	s.mu.Unlock()
	return e, v, nil
}

// editorHelp is appended to every "editor not connected" error.
const editorHelp = "Open this project in the Godot editor (4.4 or newer) and enable the plugin under Project > Project Settings > Plugins > Godot MCP. " +
	"These tools work without the editor: project_info, script_check, game_run, game_output, game_stop."

// call sends a command to the editor plugin.
func (s *Server) call(ctx context.Context, command string, params any, timeout time.Duration) (json.RawMessage, error) {
	if _, err := s.project(); err != nil {
		return nil, err
	}
	data, err := s.bridge.Call(ctx, command, params, timeout)
	if errors.Is(err, bridge.ErrNotConnected) {
		return nil, fmt.Errorf("%v. %s", err, editorHelp)
	}
	return data, err
}

func textResult(data []byte) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(data)}}}
}

func jsonResult(v any) (*mcp.CallToolResult, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return textResult(b), nil
}
