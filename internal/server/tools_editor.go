package server

import (
	"context"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type sceneTreeIn struct {
	NodePath string `json:"node_path,omitempty" jsonschema:"node to start from, relative to the scene root (default \".\", the root)"`
	MaxDepth int    `json:"max_depth,omitempty" jsonschema:"levels below node_path to include (default 4); deeper nodes report only child_count"`
	Offset   int    `json:"offset,omitempty" jsonschema:"skip this many nodes, counted in tree order; pass the next_offset of the previous answer to read the next page"`
	Limit    int    `json:"limit,omitempty" jsonschema:"most nodes in one answer (default and at most 500)"`
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
	Properties map[string]any `json:"properties" jsonschema:"property name to value. Numbers, strings and booleans as JSON; vectors as [x, y] or {\"x\":..,\"y\":..}; colours as \"#ff8800\" or [r, g, b, a]; resources and scripts as res:// paths; a new resource as {\"type\": \"RectangleShape2D\", \"size\": [16, 16]}; anything else in Godot's text form, e.g. \"Rect2(0, 0, 16, 16)\". Nested values use a colon path, e.g. \"shape:size\"."`
}

type nodeDeleteIn struct {
	NodePath string `json:"node_path" jsonschema:"node to delete with all of its children, relative to the scene root; the root itself cannot be deleted"`
}

type nodeSignalIn struct {
	From       string `json:"from" jsonschema:"node that emits the signal, relative to the scene root"`
	Signal     string `json:"signal" jsonschema:"signal name, e.g. \"pressed\" or \"body_entered\""`
	To         string `json:"to" jsonschema:"node whose method the signal calls"`
	Method     string `json:"method" jsonschema:"method on the target; it must already exist in its script or class"`
	Disconnect bool   `json:"disconnect,omitempty" jsonschema:"remove the connection instead of adding it"`
}

type projectSettingsGetIn struct {
	Prefix      string   `json:"prefix,omitempty" jsonschema:"settings whose name starts with this, e.g. \"display/window/\" or \"input/\""`
	Names       []string `json:"names,omitempty" jsonschema:"exact setting names to read"`
	ChangedOnly bool     `json:"changed_only,omitempty" jsonschema:"only settings that differ from Godot's defaults"`
}

type projectSettingsSetIn struct {
	Settings map[string]any `json:"settings" jsonschema:"setting name to value, e.g. {\"application/config/description\": \"A platformer\"}; same value format as node_set_properties"`
}

type inputActionSetIn struct {
	Action        string   `json:"action" jsonschema:"action name, e.g. \"jump\""`
	Keys          []string `json:"keys,omitempty" jsonschema:"keys by Godot name, e.g. [\"Space\", \"W\", \"Ctrl+S\"]; added as physical keys, as the Input Map editor does"`
	MouseButtons  []string `json:"mouse_buttons,omitempty" jsonschema:"\"left\", \"right\", \"middle\", \"wheel_up\" or \"wheel_down\""`
	JoypadButtons []int    `json:"joypad_buttons,omitempty" jsonschema:"joypad button indices, e.g. 0 for the bottom face button"`
	Deadzone      *float64 `json:"deadzone,omitempty" jsonschema:"default 0.2"`
	Remove        bool     `json:"remove,omitempty" jsonschema:"delete the action instead"`
}

type nodeMoveIn struct {
	NodePath         string `json:"node_path" jsonschema:"node to change, relative to the scene root"`
	NewName          string `json:"new_name,omitempty" jsonschema:"rename it; Godot appends a number if a sibling has the name"`
	NewParent        string `json:"new_parent,omitempty" jsonschema:"move it under this node, relative to the scene root; its global position is kept"`
	Index            *int   `json:"index,omitempty" jsonschema:"its position among its siblings, 0 first"`
	EditableChildren *bool  `json:"editable_children,omitempty" jsonschema:"on an instanced scene: let its inner nodes be changed here, like the editor's Editable Children"`
}

type nodeDuplicateIn struct {
	NodePath string `json:"node_path" jsonschema:"node to copy with its children, relative to the scene root"`
	Name     string `json:"name,omitempty" jsonschema:"name of the copy (default: Godot numbers it, e.g. Enemy2)"`
}

type resourceSaveIn struct {
	Path       string         `json:"path" jsonschema:"res:// path of the .tres file to write"`
	Type       string         `json:"type" jsonschema:"resource class: built-in such as StandardMaterial3D or RectangleShape2D, or a class_name from your scripts"`
	Properties map[string]any `json:"properties,omitempty" jsonschema:"properties to set, in node_set_properties' value format; nested resources inline"`
	Overwrite  bool           `json:"overwrite,omitempty" jsonschema:"replace an existing file"`
}

type screenshotIn struct {
	Viewport string `json:"viewport,omitempty" jsonschema:"\"2d\", \"3d\" or \"auto\" (default: 3d when the scene root is a Node3D, else 2d)"`
	MaxWidth int    `json:"max_width,omitempty" jsonschema:"scale the image down to at most this many pixels wide (default 1280)"`
}

func (s *Server) addSceneTools() {
	addEditorTool[sceneTreeIn](s, &mcp.Tool{
		Name: "scene_tree",
		Description: "Show the node tree of the scene open in the editor: name, class, path, attached script and instanced scene of each node. " +
			"Use the returned paths as node_path in other tools. An answer shows at most 500 nodes; next_offset, when present, reads the rest.",
		Annotations: readOnly,
	}, "scene_tree", 15*time.Second)
	addEditorTool[sceneOpenIn](s, &mcp.Tool{
		Name:        "scene_open",
		Description: "Open a scene in the editor and make it the edited scene; scene_tree and node_* tools then work on it.",
		Annotations: harmless,
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
	addEditorTool[resourceSaveIn](s, &mcp.Tool{
		Name: "resource_save",
		Description: "Create a resource file (.tres) of a built-in class or a class_name class with its properties: a material, a shape, an item's data. " +
			"Use it from a node property by its res:// path. Writing a file is not undoable; an existing file needs overwrite.",
		Annotations: editorEdit,
	}, "resource_save", 30*time.Second, "path")
	mcp.AddTool(s.mcp, &mcp.Tool{
		Name:        "editor_screenshot",
		Description: "Capture the editor's 2D or 3D viewport as an image, to check layout and visuals of the edited scene. Not available when the editor runs headless.",
		Annotations: readOnly,
	}, s.editorScreenshot)
}

func (s *Server) addNodeTools() {
	addEditorTool[nodeGetIn](s, &mcp.Tool{
		Name:        "node_get",
		Description: "Read properties of a node in the edited scene. By default returns only stored properties that differ from the class default, which keeps the answer short.",
		Annotations: readOnly,
	}, "node_get", 15*time.Second)
	addEditorTool[nodeAddIn](s, &mcp.Tool{
		Name:        "node_add",
		Description: "Add a node (by class) or an instanced scene (by res:// path) under a parent in the edited scene, optionally setting properties. The user can undo it with Ctrl+Z.",
		Annotations: editorEdit,
	}, "node_add", 15*time.Second, "scene")
	addEditorTool[nodeSetPropertiesIn](s, &mcp.Tool{
		Name:        "node_set_properties",
		Description: "Set one or more properties of a node in the edited scene as a single undoable action. Attach a script with {\"script\": \"res://player.gd\"}.",
		Annotations: harmless,
	}, "node_set_properties", 15*time.Second)
	addEditorTool[nodeDeleteIn](s, &mcp.Tool{
		Name:        "node_delete",
		Description: "Delete a node and its children from the edited scene. The user can undo it with Ctrl+Z until the scene is closed.",
		Annotations: destructive,
	}, "node_delete", 15*time.Second)
	addEditorTool[nodeMoveIn](s, &mcp.Tool{
		Name: "node_move",
		Description: "Rename, reparent or reorder a node in the edited scene, or turn Editable Children on or off for an instanced scene, as one action the user can undo with Ctrl+Z. " +
			"Properties and children move with it, unlike deleting and adding it again.",
		Annotations: harmless,
	}, "node_move", 15*time.Second)
	addEditorTool[nodeDuplicateIn](s, &mcp.Tool{
		Name:        "node_duplicate",
		Description: "Copy a node with its children and properties next to the original in the edited scene, as one action the user can undo with Ctrl+Z.",
		Annotations: editorEdit,
	}, "node_duplicate", 15*time.Second)
	addEditorTool[nodeSignalIn](s, &mcp.Tool{
		Name: "node_signal_connect",
		Description: "Connect a signal of one node in the edited scene to a method of another, or disconnect it, as the editor's Node dock does. " +
			"The connection is saved with the scene and the user can undo it with Ctrl+Z. Add the method to the target's script first.",
		Annotations: harmless,
	}, "node_signal_connect", 15*time.Second)
}

func (s *Server) addSettingsTools() {
	addEditorTool[projectSettingsGetIn](s, &mcp.Tool{
		Name:        "project_settings_get",
		Description: "Read project settings (project.godot) through the open editor. Give a prefix or names; changed_only shows only what the project overrides. Input actions are the input/ settings.",
		Annotations: readOnly,
	}, "project_settings_get", 15*time.Second)
	addEditorTool[projectSettingsSetIn](s, &mcp.Tool{
		Name: "project_settings_set",
		Description: "Change existing project settings through the editor and save project.godot, as one action the user can undo with Ctrl+Z. " +
			"Unknown names get suggestions. Some settings, such as the renderer, apply only after the editor or the game restarts.",
		Annotations: harmless,
	}, "project_settings_set", 30*time.Second)
	addEditorTool[inputActionSetIn](s, &mcp.Tool{
		Name: "input_action_set",
		Description: "Create or replace an input action with its keys, mouse buttons and joypad buttons, or remove it. " +
			"Saved to project.godot through the editor, undoable with Ctrl+Z. Games started afterwards with game_run have it, and game_input can press it.",
		Annotations: &mcp.ToolAnnotations{DestructiveHint: ptr(true), IdempotentHint: true, OpenWorldHint: ptr(false)},
	}, "input_action_set", 30*time.Second)
}

func (s *Server) editorScreenshot(ctx context.Context, _ *mcp.CallToolRequest, in screenshotIn) (*mcp.CallToolResult, any, error) {
	data, err := s.call(ctx, "editor_screenshot", in, 20*time.Second)
	if err != nil {
		return nil, nil, err
	}
	r, err := imageResult(data)
	return r, nil, err
}
