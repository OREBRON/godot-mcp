package server

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/orebron/godot-mcp/internal/godot"
	"github.com/orebron/godot-mcp/internal/project"
)

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
	}, s.projectInfo)
	mcp.AddTool(s.mcp, &mcp.Tool{
		Name: "script_check",
		Description: "Parse-check GDScript files with the project's own Godot, so types, autoloads and class_name globals resolve exactly as in the game. " +
			"Run it after creating or editing .gd files. Godot exits with 0 even when a script is broken, so use this result instead of an exit code. " +
			"Returns only the failing scripts, each with res:// file, line and message.",
		Annotations: readOnly,
	}, s.scriptCheck)
}

func (s *Server) projectInfo(ctx context.Context, _ *mcp.CallToolRequest, _ projectInfoIn) (*mcp.CallToolResult, any, error) {
	p, err := s.project()
	out := map[string]any{
		"server_version": Version,
		"project":        projectSummary(p, err),
		"godot":          s.godotSummary(ctx),
		"game":           s.runner.Status(),
	}
	if p != nil {
		out["editor"] = s.editorSummary(ctx)
	}
	r, err := jsonResult(out)
	return r, nil, err
}

func projectSummary(p *project.Project, err error) map[string]any {
	if err != nil {
		return map[string]any{"error": err.Error()}
	}
	return map[string]any{"name": p.Name, "folder": p.Dir, "main_scene": p.MainScene, "autoloads": p.Autoloads}
}

func (s *Server) godotSummary(ctx context.Context) map[string]any {
	e, v, err := s.godotExec(ctx)
	if err != nil {
		return map[string]any{"error": err.Error()}
	}
	return map[string]any{"path": e.String(), "found_via": e.Source, "version": v.Raw}
}

func (s *Server) editorSummary(ctx context.Context) map[string]any {
	hello, err := s.bridge.Connect(ctx)
	if err != nil {
		return map[string]any{"connected": false, "reason": err.Error(), "how_to_connect": editorHelp}
	}
	ed := map[string]any{"connected": true, "godot_version": hello.GodotVersion, "plugin_version": hello.PluginVersion}
	if state, err := s.bridge.Call(ctx, "ping", nil, 5*time.Second); err == nil {
		ed["state"] = json.RawMessage(state)
	}
	return ed
}

func (s *Server) scriptCheck(ctx context.Context, _ *mcp.CallToolRequest, in scriptCheckIn) (*mcp.CallToolResult, any, error) {
	p, err := s.project()
	if err != nil {
		return nil, nil, err
	}
	e, _, err := s.godotExec(ctx)
	if err != nil {
		return nil, nil, err
	}
	scripts, err := scriptsToCheck(p, in)
	if err != nil {
		return nil, nil, err
	}
	notes := s.refreshClassCache(ctx, e, p)
	res, err := e.CheckScripts(ctx, p, scripts)
	if err != nil {
		return nil, nil, err
	}
	r, err := jsonResult(checkSummary(res, notes))
	return r, nil, err
}

// scriptsToCheck is every script outside addons/ (or inside too) when no
// paths are given, else the given paths as checked res:// paths.
func scriptsToCheck(p *project.Project, in scriptCheckIn) ([]string, error) {
	if len(in.Paths) == 0 {
		return godot.ListScripts(p, in.IncludeAddons)
	}
	var scripts []string
	for _, path := range in.Paths {
		res, err := p.ToRes(path)
		if err == nil && !strings.HasSuffix(strings.ToLower(res), ".gd") {
			err = fmt.Errorf("%s is not a .gd file; script_check only parses GDScript", res)
		}
		if err == nil {
			err = p.CheckCase(res)
		}
		if err != nil {
			return nil, err
		}
		scripts = append(scripts, res)
	}
	return scripts, nil
}

// refreshClassCache makes new class_name scripts known before a check: the
// open editor rescans (it owns .godot/), otherwise a headless import runs.
func (s *Server) refreshClassCache(ctx context.Context, e godot.Exec, p *project.Project) []string {
	if _, err := s.bridge.Call(ctx, "filesystem_scan", nil, 2*time.Minute); err == nil {
		return []string{"the open editor rescanned the project first"}
	}
	if !godot.ClassCacheStale(p) {
		return nil
	}
	if err := e.Import(ctx, p); err != nil {
		return []string{"refreshing the class_name cache failed, so class_name lookups may be wrong: " + err.Error()}
	}
	return []string{"ran godot --import first to refresh the class_name cache"}
}

// checkSummary reports only the failing scripts.
func checkSummary(res godot.CheckResult, notes []string) map[string]any {
	failures := []godot.ScriptResult{}
	for _, sr := range res.Scripts {
		if !sr.OK {
			failures = append(failures, sr)
		}
	}
	return map[string]any{
		"ok": res.OK, "checked": res.Checked, "failed": res.Failed,
		"failures": failures, "other_errors": res.Other, "notes": append(notes, res.Notes...),
	}
}
