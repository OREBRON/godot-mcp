@tool
extends RefCounted
## Commands for project settings and the input map. Changes go through
## EditorUndoRedoManager and are saved to project.godot, as the editor's
## Project Settings dialog does, so the user can undo them with Ctrl+Z.

const Codec := preload("res://addons/godot_mcp/variant_codec.gd")

const MAX_SETTINGS := 300
const DEFAULT_DEADZONE := 0.2
const MOUSE_BUTTONS := {
	"left": MOUSE_BUTTON_LEFT,
	"right": MOUSE_BUTTON_RIGHT,
	"middle": MOUSE_BUTTON_MIDDLE,
	"wheel_up": MOUSE_BUTTON_WHEEL_UP,
	"wheel_down": MOUSE_BUTTON_WHEEL_DOWN,
}

var _plugin: EditorPlugin


func _init(plugin: EditorPlugin) -> void:
	_plugin = plugin


func handlers() -> Dictionary:
	return {
		"project_settings_get": _settings_get,
		"project_settings_set": _settings_set,
		"input_action_set": _input_action_set,
		"resource_save": _resource_save,
	}


func _fail(code: String, message: String, hint := "") -> Dictionary:
	var e := {"code": code, "message": message}
	if not hint.is_empty():
		e["hint"] = hint
	return {"__error": e}


# ---- reading ----

func _settings_get(p: Dictionary) -> Dictionary:
	var prefix := str(p.get("prefix", ""))
	var names: Array = p.get("names", []) if p.get("names") is Array else []
	if prefix.is_empty() and names.is_empty():
		return _fail("INVALID_PARAMS", "give a prefix or names", "e.g. prefix \"display/window/\" or \"input/\"; there are thousands of settings")
	var changed_only := bool(p.get("changed_only", false))
	var out := {}
	for info in ProjectSettings.get_property_list():
		var name: String = info.name
		if (names.has(name) or (not prefix.is_empty() and name.begins_with(prefix))) and _wanted(name, changed_only):
			out[name] = Codec.encode(ProjectSettings.get_setting(name))
		if out.size() >= MAX_SETTINGS:
			break
	return {"settings": out, "count": out.size(), "limit_reached": out.size() >= MAX_SETTINGS}


func _wanted(name: String, changed_only: bool) -> bool:
	if not ProjectSettings.has_setting(name):
		return false
	return not changed_only or ProjectSettings.get_setting(name) != ProjectSettings.property_get_revert(name)


# ---- writing ----

func _settings_set(p: Dictionary) -> Dictionary:
	var settings = p.get("settings", {})
	if not (settings is Dictionary) or settings.is_empty():
		return _fail("INVALID_PARAMS", "settings must be a non-empty object of name to value")
	var changes := []
	for name in settings:
		var change = _prepare_setting(str(name), settings[name])
		if change is Dictionary:
			return change
		changes.append(change)
	_commit("MCP: set %s" % ", ".join(settings.keys()), changes)
	var out := {}
	for c in changes:
		out[c[0]] = Codec.encode(ProjectSettings.get_setting(c[0]))
	return {"saved": true, "settings": out}


## Returns [name, old, new] for a known setting, or an error.
func _prepare_setting(name: String, value: Variant) -> Variant:
	var info := _setting_info(name)
	if info.is_empty():
		return _fail("SETTING_NOT_FOUND", "there is no project setting \"%s\"" % name, _similar_settings(name))
	var current = ProjectSettings.get_setting(name)
	var conv := Codec.decode(value, info, current)
	if conv.has("error"):
		return _fail("INVALID_VALUE", "%s: %s" % [name, conv.error])
	return [name, current, conv.value]


func _setting_info(name: String) -> Dictionary:
	if not ProjectSettings.has_setting(name):
		return {}
	for info in ProjectSettings.get_property_list():
		if info.name == name:
			return info
	return {}


func _similar_settings(name: String) -> String:
	var hits := []
	for info in ProjectSettings.get_property_list():
		if str(info.name).similarity(name) > 0.8:
			hits.append(info.name)
	return ("similar: " + ", ".join(hits.slice(0, 8))) if not hits.is_empty() else "read the names with project_settings_get and a prefix"


## Applies [name, old, new] changes as one undoable action and saves
## project.godot after doing and after undoing.
func _commit(action: String, changes: Array) -> void:
	var ur := _plugin.get_undo_redo()
	ur.create_action(action)
	for c in changes:
		ur.add_do_method(ProjectSettings, "set_setting", c[0], c[2])
		ur.add_undo_method(ProjectSettings, "set_setting", c[0], c[1])
	ur.add_do_method(ProjectSettings, "save")
	ur.add_undo_method(ProjectSettings, "save")
	ur.commit_action()


# ---- input map ----

func _input_action_set(p: Dictionary) -> Dictionary:
	var action := str(p.get("action", "")).strip_edges()
	if action.is_empty() or action.contains("/"):
		return _fail("INVALID_PARAMS", "action must be a name such as \"jump\"")
	var name := "input/" + action
	var old = ProjectSettings.get_setting(name) if ProjectSettings.has_setting(name) else null
	if bool(p.get("remove", false)):
		_commit("MCP: remove input action %s" % action, [[name, old, null]])
		return {"action": action, "removed": true}
	var events = _events(p)
	if events is Dictionary:
		return events
	var deadzone := float(p.get("deadzone", DEFAULT_DEADZONE))
	_commit("MCP: set input action %s" % action, [[name, old, {"deadzone": deadzone, "events": events}]])
	return {"action": action, "deadzone": deadzone, "events": events.map(func(e: InputEvent) -> String: return e.as_text())}


## Builds the action's events from keys, mouse_buttons and joypad_buttons.
func _events(p: Dictionary) -> Variant:
	var events := []
	for key in _list(p, "keys"):
		var ev := _key_event(str(key))
		if ev == null:
			return _fail("INVALID_PARAMS", "unknown key \"%s\"" % key, "use Godot key names such as Space, Enter, W, Left, F1, optionally with modifiers: Ctrl+S")
		events.append(ev)
	for button in _list(p, "mouse_buttons"):
		if not MOUSE_BUTTONS.has(str(button)):
			return _fail("INVALID_PARAMS", "unknown mouse button \"%s\"" % button, "use left, right, middle, wheel_up or wheel_down")
		var mb := InputEventMouseButton.new()
		mb.button_index = MOUSE_BUTTONS[str(button)]
		events.append(mb)
	for index in _list(p, "joypad_buttons"):
		var jb := InputEventJoypadButton.new()
		jb.button_index = int(index)
		events.append(jb)
	if events.is_empty():
		return _fail("INVALID_PARAMS", "give at least one of keys, mouse_buttons or joypad_buttons, or remove: true")
	return events


func _list(p: Dictionary, key: String) -> Array:
	return p.get(key, []) if p.get(key) is Array else []


## A physical key, as the Input Map editor adds them, so the action works on
## every keyboard layout. "Ctrl+S" style modifiers are allowed.
func _key_event(spec: String) -> InputEventKey:
	var parts := spec.split("+")
	var code := OS.find_keycode_from_string(parts[-1].strip_edges())
	if code == KEY_NONE:
		return null
	var ev := InputEventKey.new()
	ev.physical_keycode = code
	for mod in parts.slice(0, -1):
		match mod.strip_edges().to_lower():
			"ctrl", "control":
				ev.ctrl_pressed = true
			"shift":
				ev.shift_pressed = true
			"alt":
				ev.alt_pressed = true
			"meta", "cmd", "command":
				ev.meta_pressed = true
			_:
				return null
	return ev


# ---- resource files ----

## Writes a new .tres of a built-in class or a class_name class. Writing a
## file is not undoable.
func _resource_save(p: Dictionary) -> Dictionary:
	var path := str(p.get("path", ""))
	if not (path.ends_with(".tres") or path.ends_with(".res")):
		return _fail("INVALID_PARAMS", "path must end with .tres or .res, got \"%s\"" % path)
	if FileAccess.file_exists(path) and not bool(p.get("overwrite", false)):
		return _fail("FILE_EXISTS", "%s already exists" % path, "pass overwrite: true to replace it")
	var spec: Dictionary = p.get("properties", {}).duplicate() if p.get("properties") is Dictionary else {}
	spec["type"] = str(p.get("type", ""))
	var made := Codec.new_resource(spec)
	if made.has("error"):
		return _fail("INVALID_VALUE", made.error)
	DirAccess.make_dir_recursive_absolute(ProjectSettings.globalize_path(path.get_base_dir()))
	made.value.take_over_path(path) # later loads of path get the new resource
	var err := ResourceSaver.save(made.value, path)
	if err != OK:
		return _fail("SAVE_FAILED", "saving %s failed: %s" % [path, error_string(err)])
	EditorInterface.get_resource_filesystem().update_file(path)
	return {"saved": path, "type": spec.type}
