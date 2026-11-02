package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/orebron/godot-mcp/internal/game"
)

func ptr[T any](v T) *T { return &v }

// Annotation presets.
var (
	readOnly    = &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: ptr(false)}
	editorEdit  = &mcp.ToolAnnotations{DestructiveHint: ptr(false), OpenWorldHint: ptr(false)}
	destructive = &mcp.ToolAnnotations{DestructiveHint: ptr(true), OpenWorldHint: ptr(false)}
	harmless    = &mcp.ToolAnnotations{DestructiveHint: ptr(false), IdempotentHint: true, OpenWorldHint: ptr(false)}
)

// toolGroups are the sets of tools --tools chooses from, in the order they
// are registered.
var toolGroups = []struct {
	name string
	add  func(*Server)
}{
	{"project", (*Server).addProjectTools},
	{"game", func(s *Server) { s.addGameTools(); s.addLiveGameTools(); s.addGameControlTools() }},
	{"scene", (*Server).addSceneTools},
	{"node", (*Server).addNodeTools},
	{"settings", (*Server).addSettingsTools},
}

// ToolGroups lists the group names, for help texts and errors.
func ToolGroups() []string {
	names := make([]string, len(toolGroups))
	for i, g := range toolGroups {
		names[i] = g.name
	}
	return names
}

// CheckToolGroups reports the first name that is not a tool group.
func CheckToolGroups(names []string) error {
	for _, n := range names {
		if !slices.Contains(ToolGroups(), n) {
			return fmt.Errorf("unknown tool group %q; the groups are %s", n, strings.Join(ToolGroups(), ", "))
		}
	}
	return nil
}

// registerTools adds the chosen groups, or all of them when none is chosen.
func (s *Server) registerTools(groups []string) {
	for _, g := range toolGroups {
		if len(groups) == 0 || slices.Contains(groups, g.name) {
			g.add(s)
		}
	}
}

// addGameTool registers a tool that runs a command in the game started by
// game_run, through its bridge.
func addGameTool[In any](s *Server, t *mcp.Tool, command string, timeout time.Duration) {
	mcp.AddTool(s.mcp, t, func(ctx context.Context, _ *mcp.CallToolRequest, in In) (*mcp.CallToolResult, any, error) {
		data, err := s.gameCall(ctx, command, in, timeout)
		if err != nil {
			return nil, nil, err
		}
		return textResult(data), nil, nil
	})
}

// gameCall sends a command to the running game's bridge.
func (s *Server) gameCall(ctx context.Context, command string, params any, timeout time.Duration) (json.RawMessage, error) {
	link := s.runner.Link()
	if link == nil {
		return nil, game.ErrNoGame
	}
	return link.Call(ctx, command, params, timeout)
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

// imageResult turns an answer holding png_base64 into an image, followed by
// the answer's other fields as JSON text.
func imageResult(data json.RawMessage) (*mcp.CallToolResult, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return nil, err
	}
	var b64 string
	if err := json.Unmarshal(fields["png_base64"], &b64); err != nil {
		return nil, fmt.Errorf("the answer holds no image: %w", err)
	}
	png, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil, fmt.Errorf("the answer holds an invalid image: %w", err)
	}
	delete(fields, "png_base64")
	meta, err := json.Marshal(fields)
	if err != nil {
		return nil, err
	}
	return &mcp.CallToolResult{Content: []mcp.Content{
		&mcp.ImageContent{Data: png, MIMEType: "image/png"},
		&mcp.TextContent{Text: string(meta)},
	}}, nil
}
