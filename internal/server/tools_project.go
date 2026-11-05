package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/orebron/godot-mcp/internal/bridge"
	"github.com/orebron/godot-mcp/internal/godot"
	"github.com/orebron/godot-mcp/internal/project"
)

type projectInfoIn struct{}

type testRunIn struct {
	Dirs           []string `json:"dirs,omitempty" jsonschema:"res:// folders of tests (default res://test or res://tests)"`
	Framework      string   `json:"framework,omitempty" jsonschema:"\"auto\" (default: gdUnit4 if installed, else GUT), \"gdunit4\" or \"gut\""`
	TimeoutSeconds *float64 `json:"timeout_seconds,omitempty" jsonschema:"stop the run after this many seconds (default 300)"`
}

type exportBuildIn struct {
	Preset         string   `json:"preset,omitempty" jsonschema:"name of a preset in export_presets.cfg; may be empty when there is only one"`
	Output         string   `json:"output,omitempty" jsonschema:"res:// or project-relative path of the exported file (default: the preset's export path); must be inside the project"`
	Debug          bool     `json:"debug,omitempty" jsonschema:"export a debug build (--export-debug) instead of a release build"`
	TimeoutSeconds *float64 `json:"timeout_seconds,omitempty" jsonschema:"stop the export after this many seconds (default 600)"`
}

type scriptCheckIn struct {
	Paths         []string `json:"paths,omitempty" jsonschema:"res:// or project-relative paths of .gd files to check. Leave empty to check every script outside addons/."`
	IncludeAddons bool     `json:"include_addons,omitempty" jsonschema:"also check scripts under addons/ when paths is empty"`
}

type classInfoIn struct {
	Class            string `json:"class" jsonschema:"a built-in class such as CharacterBody2D, or a class_name from the project's scripts"`
	IncludeInherited bool   `json:"include_inherited,omitempty" jsonschema:"also list the members it inherits (default: only those the class declares itself)"`
	Filter           string `json:"filter,omitempty" jsonschema:"keep only members whose name contains this, ignoring case"`
}

type projectFindIn struct {
	Path     string `json:"path,omitempty" jsonschema:"a res:// or project-relative path, or a uid://: lists every project file that uses it"`
	NodeType string `json:"node_type,omitempty" jsonschema:"a node class, built-in such as Area2D or a class_name: lists the scene nodes of that type"`
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
		Name: "test_run",
		Description: "Run the project's gdUnit4 or GUT tests headless and return the counts and each failing test with its message, read from the framework's JUnit report, " +
			"plus any script errors printed during the run. Needs gdUnit4 (addons/gdUnit4) or GUT (addons/gut) installed. A run that hangs is stopped after timeout_seconds.",
		Annotations: readOnly,
	}, s.testRun)
	mcp.AddTool(s.mcp, &mcp.Tool{
		Name: "script_check",
		Description: "Parse-check GDScript files with the project's own Godot, so types, autoloads and class_name globals resolve exactly as in the game. " +
			"Run it after creating or editing .gd files. Godot exits with 0 even when a script is broken, so use this result instead of an exit code. " +
			"Returns only the failing scripts, each with res:// file, line and message.",
		Annotations: readOnly,
	}, s.scriptCheck)
	mcp.AddTool(s.mcp, &mcp.Tool{
		Name: "export_build",
		Description: "Export the game headless with a preset from export_presets.cfg (made under Project > Export in the editor). " +
			"Returns the res:// output path, its size and the errors Godot printed, or why the export failed with the end of Godot's output. " +
			"Success means the file was written by this run, not just exit code 0. The output must be inside the project; parent folders are created.",
		Annotations: destructive,
	}, s.exportBuild)
	mcp.AddTool(s.mcp, &mcp.Tool{
		Name: "class_info",
		Description: "Look up the real API of a class before writing code against it: a built-in class or a class_name from the project's scripts, as the project's Godot sees it. " +
			"Returns the inheritance chain and the properties (type, default), methods (signatures), signals, constants and enums the class declares itself; " +
			"include_inherited adds what it inherits, and filter keeps the names containing a substring. Property setters and getters are not listed as methods. Works without the editor.",
		Annotations: readOnly,
	}, s.classInfo)
	mcp.AddTool(s.mcp, &mcp.Tool{
		Name: "project_find",
		Description: "Before changing, moving or deleting something, see what depends on it. Pass exactly one of path and node_type. " +
			"path (a file or uid://) returns its res:// path and uid and every line that uses it, with file, line and kind: " +
			"ext_resource, setting or autoload in project.godot, preload, load, uid, string, or class (a class_name script named in other scripts). " +
			"node_type returns the scene nodes of that exact type or with a class_name script of that name, with their node paths; " +
			"an instanced scene counts by its root, and subclasses are not counted. " +
			"Reads the project's text files, so it needs no editor; binary .scn/.res files are skipped and counted in notes. At most 200 hits, then truncated is true.",
		Annotations: readOnly,
	}, s.projectFind)
}

func (s *Server) projectFind(_ context.Context, _ *mcp.CallToolRequest, in projectFindIn) (*mcp.CallToolResult, any, error) {
	p, err := s.project()
	if err != nil {
		return nil, nil, err
	}
	var out any
	switch {
	case (in.Path == "") == (in.NodeType == ""):
		return nil, nil, &bridge.Error{Code: "BAD_ARGUMENTS", Message: "pass exactly one of path and node_type", Hint: "call project_find once for each question"}
	case in.Path != "":
		out, err = p.FindRefs(in.Path)
	default:
		out, err = p.FindNodes(in.NodeType)
	}
	if err != nil {
		return nil, nil, findError(err)
	}
	r, err := jsonResult(out)
	return r, nil, err
}

// findError turns a path or uid that names no file into a coded error with
// the close names.
func findError(err error) error {
	var m *project.MissingError
	if !errors.As(err, &m) {
		return err
	}
	code, hint := "FILE_NOT_FOUND", "check the path; it is relative to the project folder"
	if strings.HasPrefix(m.Target, "uid://") {
		code, hint = "UID_NOT_FOUND", "check the uid, or pass the file's res:// path"
	}
	if len(m.Similar) > 0 {
		hint = "did you mean: " + strings.Join(m.Similar, ", ")
	}
	return &bridge.Error{Code: code, Message: m.Error(), Hint: hint}
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

func (s *Server) classInfo(ctx context.Context, _ *mcp.CallToolRequest, in classInfoIn) (*mcp.CallToolResult, any, error) {
	p, err := s.project()
	if err != nil {
		return nil, nil, err
	}
	e, _, err := s.godotExec(ctx)
	if err != nil {
		return nil, nil, err
	}
	notes := s.refreshClassCache(ctx, e, p)
	info, err := e.ClassInfo(ctx, p, in.Class, in.IncludeInherited, in.Filter)
	if err != nil {
		return nil, nil, err
	}
	if len(notes) == 0 {
		return textResult(info), nil, nil
	}
	var out map[string]any
	if err := json.Unmarshal(info, &out); err != nil {
		return nil, nil, err
	}
	out["notes"] = notes
	r, err := jsonResult(out)
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

func (s *Server) testRun(ctx context.Context, _ *mcp.CallToolRequest, in testRunIn) (*mcp.CallToolResult, any, error) {
	p, err := s.project()
	if err != nil {
		return nil, nil, err
	}
	e, _, err := s.godotExec(ctx)
	if err != nil {
		return nil, nil, err
	}
	framework, err := godot.DetectFramework(p, in.Framework)
	if err != nil {
		return nil, nil, err
	}
	dirs, err := testDirs(p, in.Dirs)
	if err != nil {
		return nil, nil, err
	}
	res, err := e.RunTests(ctx, p, framework, dirs, seconds(in.TimeoutSeconds, 300*time.Second))
	if err != nil {
		return nil, nil, err
	}
	r, err := jsonResult(res)
	return r, nil, err
}

// testDirs is the given folders as checked res:// paths, or the default.
func testDirs(p *project.Project, dirs []string) ([]string, error) {
	if len(dirs) == 0 {
		return godot.DefaultTestDirs(p)
	}
	out := make([]string, 0, len(dirs))
	for _, d := range dirs {
		res, err := p.ToRes(d)
		if err == nil {
			err = p.CheckCase(res)
		}
		if err != nil {
			return nil, err
		}
		out = append(out, res)
	}
	return out, nil
}

// seconds is a timeout_seconds parameter as a duration of at least one
// second, or def when it is not given.
func seconds(v *float64, def time.Duration) time.Duration {
	if v == nil {
		return def
	}
	return time.Duration(max(*v, 1) * float64(time.Second))
}

func (s *Server) exportBuild(ctx context.Context, _ *mcp.CallToolRequest, in exportBuildIn) (*mcp.CallToolResult, any, error) {
	p, err := s.project()
	if err != nil {
		return nil, nil, err
	}
	e, _, err := s.godotExec(ctx)
	if err != nil {
		return nil, nil, err
	}
	preset, err := pickPreset(p, in.Preset)
	if err != nil {
		return nil, nil, err
	}
	res, out, err := exportTarget(p, preset, in.Output)
	if err != nil {
		return nil, nil, err
	}
	before, start := godot.OutputStat(out), time.Now()
	run, err := e.Export(ctx, p, preset.Name, out, in.Debug, seconds(in.TimeoutSeconds, 600*time.Second))
	if err != nil {
		return nil, nil, err
	}
	if missing := godot.MissingTemplates(run.Output); len(missing) > 0 {
		return nil, nil, templatesError(preset, missing)
	}
	summary := map[string]any{
		"preset": preset.Name, "platform": preset.Platform, "debug": in.Debug, "output": res,
		"exit_code": run.ExitCode, "duration_seconds": time.Since(start).Round(time.Millisecond).Seconds(),
		"errors": run.Errors,
	}
	reportOutcome(summary, run, before, godot.OutputStat(out))
	r, err := jsonResult(summary)
	return r, nil, err
}

// reportOutcome adds ok and the size, or why the export failed: a written
// file proves the export, the exit code alone does not.
func reportOutcome(summary map[string]any, run godot.ExportRun, before, after godot.FileState) {
	written := after.WrittenSince(before)
	summary["ok"] = written && run.ExitCode == 0
	switch {
	case !written:
		summary["reason"] = fmt.Sprintf("Godot wrote nothing at %s", summary["output"])
	case run.ExitCode != 0:
		summary["reason"] = fmt.Sprintf("Godot exited with %d", run.ExitCode)
	default:
		summary["size_bytes"] = after.Size
		return
	}
	summary["output_tail"] = run.Tail
}

// pickPreset finds the named preset, or the only one when name is empty.
func pickPreset(p *project.Project, name string) (project.ExportPreset, error) {
	presets, err := p.ExportPresets()
	if errors.Is(err, os.ErrNotExist) || (err == nil && len(presets) == 0) {
		return project.ExportPreset{}, &bridge.Error{Code: "NO_EXPORT_PRESETS", Message: "the project has no export presets (export_presets.cfg)",
			Hint: "add one in the editor under Project > Export, for the platform to build, then call export_build again"}
	}
	if err != nil {
		return project.ExportPreset{}, err
	}
	names := make([]string, len(presets))
	for i, pr := range presets {
		if pr.Name == name || (name == "" && len(presets) == 1) {
			return pr, nil
		}
		names[i] = strconv.Quote(pr.Name)
	}
	msg := fmt.Sprintf("no export preset named %q", name)
	if name == "" {
		msg = fmt.Sprintf("export_presets.cfg has %d presets; pass preset", len(presets))
	}
	return project.ExportPreset{}, &bridge.Error{Code: "PRESET_NOT_FOUND", Message: msg, Hint: "the presets are " + strings.Join(names, ", ")}
}

// exportTarget is where the export goes, as a res:// path and an OS path:
// output, or else the preset's export path. It must be inside the project
// and must not be a project file; its folder is created.
func exportTarget(p *project.Project, preset project.ExportPreset, output string) (res, osPath string, err error) {
	if output == "" {
		output = preset.ExportPath
	}
	if output == "" {
		return "", "", &bridge.Error{Code: "BAD_OUTPUT_PATH", Message: fmt.Sprintf("preset %q has no export path", preset.Name), Hint: "pass output, such as build/game.x86_64"}
	}
	res, err = p.ToRes(output)
	if err == nil {
		err = checkOutput(p, res)
	}
	if err == nil {
		osPath, err = p.ToOS(res)
	}
	if err == nil {
		err = os.MkdirAll(filepath.Dir(osPath), 0o755)
	}
	if err != nil {
		return "", "", &bridge.Error{Code: "BAD_OUTPUT_PATH", Message: err.Error(), Hint: "pass output as a path inside the project folder, such as build/game.x86_64"}
	}
	return res, osPath, nil
}

// projectFiles are extensions an export must never overwrite.
var projectFiles = []string{".gd", ".cs", ".tscn", ".scn", ".tres", ".res", ".godot", ".cfg", ".import", ".uid", ".gdshader"}

func checkOutput(p *project.Project, res string) error {
	if res == "res://" || strings.HasPrefix(res, "uid://") {
		return fmt.Errorf("%s is not a file path", res)
	}
	if ext := strings.ToLower(path.Ext(res)); slices.Contains(projectFiles, ext) {
		return fmt.Errorf("%s is a project file (%s); an export must not overwrite it", res, ext)
	}
	return p.CheckCaseExisting(res)
}

// templatesError says which export templates are missing and where they go.
func templatesError(preset project.ExportPreset, missing []string) error {
	dir := filepath.Dir(missing[0])
	return &bridge.Error{
		Code:    "EXPORT_TEMPLATES_MISSING",
		Message: fmt.Sprintf("the export templates for preset %q (%s) are not installed: %s", preset.Name, preset.Platform, strings.Join(missing, ", ")),
		Hint: fmt.Sprintf("install the %s export templates in the editor under Editor > Manage Export Templates > Download and Install; they go in %s. "+
			"An export needs templates of exactly the editor's version.", filepath.Base(dir), dir),
	}
}
