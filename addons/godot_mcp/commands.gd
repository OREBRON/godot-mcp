@tool
extends RefCounted
## The commands godot-mcp servers can run in the editor. Every scene change
## goes through EditorUndoRedoManager, so the user can revert it with Ctrl+Z.
## A handler returns a Dictionary of data, or _fail(...) for an error.

const Codec := preload("res://addons/godot_mcp/variant_codec.gd")

const MAX_TREE_NODES := 500

var _plugin: EditorPlugin
var _handlers := {}


func _init(plugin: EditorPlugin) -> void:
	_plugin = plugin
	_handlers = {
		"ping": _ping,
		"filesystem_scan": _filesystem_scan,
		"scene_tree": _scene_tree,
		"node_get": _node_get,
		"scene_open": _scene_open,
		"scene_create": _scene_create,
		"scene_save": _scene_save,
		"node_add": _node_add,
		"node_set_properties": _node_set_properties,
		"node_delete": _node_delete,
		"editor_screenshot": _editor_screenshot,
	}


func names() -> Array:
	return _handlers.keys()


## Runs a command and wraps the result as {"ok": true, "data": ...} or
## {"ok": false, "error": {...}}.
func run(command: String, params: Dictionary) -> Dictionary:
	if not _handlers.has(command):
		return {"ok": false, "error": {"code": "UNKNOWN_COMMAND", "message": "unknown command \"%s\"" % command, "hint": "update the plugin and the godot-mcp binary to the same release"}}
	var result = await _handlers[command].call(params)
	if result is Dictionary and result.has("__error"):
		return {"ok": false, "error": result["__error"]}
	return {"ok": true, "data": result}


func _fail(code: String, message: String, hint := "") -> Dictionary:
	var e := {"code": code, "message": message}
	if not hint.is_empty():
		e["hint"] = hint
	return {"__error": e}


func _is_error(v: Variant) -> bool:
	return v is Dictionary and v.has("__error")


func _frames(n: int) -> void:
	for i in n:
		await _plugin.get_tree().process_frame


# ---- lookups ----

func _root() -> Node:
	return EditorInterface.get_edited_scene_root()


func _rel(node: Node) -> String:
	return str(_root().get_path_to(node))


## Children that belong to the edited scene (not the insides of instances).
func _owned_children(node: Node) -> Array:
	var root := _root()
	return node.get_children().filter(func(c: Node) -> bool: return c.owner == root)


## Whether node is the edited scene's root or below it.
func _inside(node: Node) -> bool:
	var root := _root()
	return node == root or root.is_ancestor_of(node)


## Refuses edits to nodes that come from an instanced scene. Godot saves
## changes to them only when the instance has Editable Children, and never
## lets them be deleted or moved: they belong to the other scene's file.
## structural is true for deleting the node, false for changing it or adding
## children to it.
func _check_editable(node: Node, structural: bool) -> Variant:
	var root := _root()
	if node == root or node.owner == root:
		return null
	var instance := node.owner
	if not structural and instance != null and root.is_editable_instance(instance):
		return null
	var source := instance.scene_file_path if instance != null else "another scene"
	var hint := "change it in %s (open it with scene_open)" % source
	if not structural and instance != null:
		hint += ", or enable Editable Children on \"%s\" in the editor" % _rel(instance)
	return _fail("FOREIGN_NODE", "\"%s\" belongs to the instanced scene %s" % [_rel(node), source], hint)


## Returns the node at a path relative to the scene root, or an error that
## lists what does exist there.
func _resolve(path: String) -> Variant:
	var root := _root()
	if root == null:
		return _fail("NO_SCENE_OPEN", "no scene is open in the editor", "open one with scene_open or create one with scene_create")
	var p := path.strip_edges()
	if p.is_empty() or p == ".":
		return root
	if p.begins_with("/"):
		return _fail("INVALID_PATH", "node paths are relative to the scene root, got \"%s\"" % p, "use \".\" for the root and e.g. \"Player/Sprite2D\" for a child")
	var node := root.get_node_or_null(NodePath(p))
	if node != null:
		# ".." would otherwise climb into the editor's own nodes, which hold
		# the edited scene.
		if not _inside(node):
			return _fail("INVALID_PATH", "\"%s\" leads outside the edited scene" % p, "node paths start at the scene root and cannot go above it with \"..\"")
		return node
	var cur := root
	for part in p.split("/"):
		var next := cur.get_node_or_null(NodePath(part))
		if next == null or not _inside(next):
			break
		cur = next
	var kids := _owned_children(cur).map(func(c: Node) -> String: return str(c.name))
	return _fail("NODE_NOT_FOUND", "no node at \"%s\"" % p, "children of \"%s\": %s" % [_rel(cur), ", ".join(kids) if not kids.is_empty() else "(none)"])


## The property-list entry of a property (type, hint, hint_string,
## class_name), following "a:b" sub-paths into resources; {} if the
## property does not exist.
func _property_info(obj: Object, path: String) -> Dictionary:
	var parts := path.split(":")
	for i in parts.size():
		var found := {}
		for info in obj.get_property_list():
			if info.name == parts[i]:
				found = info
				break
		if found.is_empty() or i == parts.size() - 1:
			return found
		var next = obj.get(parts[i])
		if not (next is Object):
			return {}
		obj = next
	return {}


func _similar_properties(obj: Object, name: String) -> String:
	var hits := []
	for info in obj.get_property_list():
		if info.usage & PROPERTY_USAGE_EDITOR and (info.name.containsn(name) or name.containsn(info.name) or info.name.similarity(name) > 0.5):
			hits.append(info.name)
	return ", ".join(hits.slice(0, 12))


## Creates a node of a built-in class or a class_name script class.
func _instantiate_type(type: String) -> Variant:
	if ClassDB.class_exists(type):
		if not ClassDB.is_parent_class(type, "Node"):
			return _fail("INVALID_TYPE", "%s is not a Node class" % type, "resources such as shapes and materials are not nodes: save them as .tres and set them on a node property with their res:// path")
		if not ClassDB.can_instantiate(type):
			return _fail("INVALID_TYPE", "%s is abstract and cannot be created" % type, "pick one of its subclasses: %s" % ", ".join(ClassDB.get_inheriters_from_class(type).slice(0, 10)))
		return ClassDB.instantiate(type)
	for g in ProjectSettings.get_global_class_list():
		if g["class"] == type:
			var script := load(g["path"]) as Script
			if script == null or not script.can_instantiate():
				return _fail("INVALID_TYPE", "the script class %s (%s) cannot be instantiated" % [type, g["path"]], "run script_check on it")
			var obj = script.new()
			if not (obj is Node):
				return _fail("INVALID_TYPE", "the script class %s does not extend Node" % type)
			return obj
	var close := []
	for c in ClassDB.get_inheriters_from_class("Node"):
		if c.similarity(type) > 0.6 or c.to_lower() == type.to_lower():
			close.append(c)
	return _fail("INVALID_TYPE", "unknown class \"%s\"" % type, ("did you mean: %s" % ", ".join(close.slice(0, 5))) if not close.is_empty() else "use a built-in class such as Node2D or Sprite2D, or a class_name from your scripts")


## Converts and checks every property first, so that a bad value changes
## nothing. Returns [[NodePath, old, new], ...] or an error.
func _prepare_changes(node: Node, props: Dictionary) -> Variant:
	var changes := []
	for key in props:
		var name := str(key)
		var info := _property_info(node, name)
		if info.is_empty():
			var similar := _similar_properties(node, name.get_slice(":", 0))
			return _fail("PROPERTY_NOT_FOUND", "%s has no property \"%s\"" % [node.get_class(), name], ("similar: " + similar) if not similar.is_empty() else "read the node's properties with node_get")
		var np := NodePath(name)
		var current = node.get_indexed(np)
		var conv := Codec.decode(props[key], info, current)
		if conv.has("error"):
			return _fail("INVALID_VALUE", "%s: %s" % [name, conv.error])
		# A script only fits a node of the class it extends (or a subclass).
		if name == "script" and conv.value is Script:
			var base: StringName = conv.value.get_instance_base_type()
			if not node.is_class(base):
				return _fail("INVALID_VALUE", "script: %s extends %s, which %s is not" % [conv.value.resource_path, base, node.get_class()], "attach it to a %s, or change its extends line" % base)
		changes.append([np, current, conv.value])
	return changes


# ---- commands ----

func _ping(_p: Dictionary) -> Dictionary:
	var root := _root()
	return {
		"godot_version": Engine.get_version_info().string,
		"edited_scene": root.scene_file_path if root != null else "",
		"open_scenes": EditorInterface.get_open_scenes(),
		"playing_scene": EditorInterface.get_playing_scene(),
		"scanning": EditorInterface.get_resource_filesystem().is_scanning(),
		"headless": DisplayServer.get_name() == "headless",
	}


func _filesystem_scan(_p: Dictionary) -> Dictionary:
	var fs := EditorInterface.get_resource_filesystem()
	if not fs.is_scanning():
		fs.scan()
	var start := Time.get_ticks_msec()
	await _frames(2)
	while fs.is_scanning() and Time.get_ticks_msec() - start < 110000:
		await _frames(1)
	# Script classes are saved a few frames after the scan ends.
	await _frames(3)
	return {"scanned": true, "ms": Time.get_ticks_msec() - start}


func _describe(node: Node, depth_left: int, count: Array) -> Dictionary:
	count[0] += 1
	var d := {"name": str(node.name), "type": node.get_class(), "path": _rel(node)}
	var script: Script = node.get_script()
	if script != null:
		d["script"] = script.resource_path
	if node != _root() and not node.scene_file_path.is_empty():
		d["instance"] = node.scene_file_path
	var kids := _owned_children(node)
	if kids.is_empty():
		return d
	if depth_left <= 0 or count[0] >= MAX_TREE_NODES:
		d["child_count"] = kids.size()
		return d
	var children := []
	for c in kids:
		if count[0] >= MAX_TREE_NODES:
			d["children_not_shown"] = kids.size() - children.size()
			break
		children.append(_describe(c, depth_left - 1, count))
	d["children"] = children
	return d


func _scene_tree(p: Dictionary) -> Dictionary:
	var node = _resolve(str(p.get("node_path", ".")))
	if _is_error(node):
		return node
	var depth := int(p.get("max_depth", 4))
	if depth <= 0:
		depth = 4
	var count := [0]
	var tree := _describe(node, depth, count)
	return {"scene": _root().scene_file_path, "tree": tree, "nodes_shown": count[0], "limit_reached": count[0] >= MAX_TREE_NODES}


func _node_get(p: Dictionary) -> Dictionary:
	var node = _resolve(str(p.get("node_path", ".")))
	if _is_error(node):
		return node
	var out := {}
	var wanted: Array = p.get("properties", []) if p.get("properties") is Array else []
	if wanted.is_empty():
		var include_defaults := bool(p.get("include_defaults", false))
		var script: Script = node.get_script()
		for info in node.get_property_list():
			if not (info.usage & PROPERTY_USAGE_STORAGE):
				continue
			var value = node.get(info.name)
			if not include_defaults:
				var def = ClassDB.class_get_property_default_value(node.get_class(), info.name)
				if script != null and def == null:
					def = script.get_property_default_value(info.name)
				if typeof(value) == typeof(def) and value == def:
					continue
				if value == null and def == null:
					continue
			out[info.name] = Codec.encode(value)
	else:
		for key in wanted:
			var name := str(key)
			if _property_info(node, name).is_empty():
				var similar := _similar_properties(node, name.get_slice(":", 0))
				return _fail("PROPERTY_NOT_FOUND", "%s has no property \"%s\"" % [node.get_class(), name], ("similar: " + similar) if not similar.is_empty() else "")
			out[name] = Codec.encode(node.get_indexed(NodePath(name)))
	return {"path": _rel(node), "type": node.get_class(), "properties": out}


func _scene_open(p: Dictionary) -> Dictionary:
	var path := str(p.get("path", ""))
	if not (path.ends_with(".tscn") or path.ends_with(".scn")):
		return _fail("INVALID_PARAMS", "%s is not a scene file (.tscn or .scn)" % path)
	if not FileAccess.file_exists(path):
		return _fail("FILE_NOT_FOUND", "%s does not exist" % path, "create it with scene_create")
	EditorInterface.open_scene_from_path(path)
	await _frames(2)
	var root := _root()
	if root == null or root.scene_file_path != path:
		return _fail("OPEN_FAILED", "the editor did not open %s" % path, "the file may not parse; check the editor's Output panel")
	return {"scene": path, "root": {"name": str(root.name), "type": root.get_class()}}


func _scene_create(p: Dictionary) -> Dictionary:
	var path := str(p.get("path", ""))
	if not path.ends_with(".tscn"):
		return _fail("INVALID_PARAMS", "path must end with .tscn, got \"%s\"" % path)
	if FileAccess.file_exists(path):
		return _fail("FILE_EXISTS", "%s already exists" % path, "open it with scene_open")
	var root = _instantiate_type(str(p.get("root_type", "")))
	if _is_error(root):
		return root
	var root_name := str(p.get("root_name", ""))
	root.name = root_name if not root_name.is_empty() else path.get_file().get_basename().to_pascal_case()
	var packed := PackedScene.new()
	var err := packed.pack(root)
	root.free()
	if err != OK:
		return _fail("SAVE_FAILED", "packing the scene failed: %s" % error_string(err))
	DirAccess.make_dir_recursive_absolute(ProjectSettings.globalize_path(path.get_base_dir()))
	err = ResourceSaver.save(packed, path)
	if err != OK:
		return _fail("SAVE_FAILED", "saving %s failed: %s" % [path, error_string(err)])
	EditorInterface.get_resource_filesystem().update_file(path)
	EditorInterface.open_scene_from_path(path)
	await _frames(2)
	var opened := _root()
	return {"scene": path, "root": {"name": str(opened.name), "type": opened.get_class()} if opened != null else null}


func _scene_save(p: Dictionary) -> Dictionary:
	var root := _root()
	if root == null:
		return _fail("NO_SCENE_OPEN", "no scene is open in the editor", "open one with scene_open")
	var target := str(p.get("path", ""))
	if target.is_empty():
		if root.scene_file_path.is_empty():
			return _fail("NO_PATH", "the scene has never been saved", "pass path, e.g. \"res://scenes/level.tscn\"")
		if DisplayServer.get_name() == "headless":
			# save_scene() renders a thumbnail, which a headless editor cannot.
			EditorInterface.save_scene_as(root.scene_file_path, false)
		else:
			var err := EditorInterface.save_scene()
			if err != OK:
				return _fail("SAVE_FAILED", "saving failed: %s" % error_string(err))
		return {"saved": root.scene_file_path}
	if not target.ends_with(".tscn") and not target.ends_with(".scn"):
		return _fail("INVALID_PARAMS", "path must end with .tscn or .scn")
	DirAccess.make_dir_recursive_absolute(ProjectSettings.globalize_path(target.get_base_dir()))
	EditorInterface.save_scene_as(target, false)
	await _frames(1)
	if not FileAccess.file_exists(target):
		return _fail("SAVE_FAILED", "the editor did not write %s" % target)
	return {"saved": target}


func _node_add(p: Dictionary) -> Dictionary:
	var parent = _resolve(str(p.get("parent_path", ".")))
	if _is_error(parent):
		return parent
	var foreign = _check_editable(parent, false)
	if foreign != null:
		return foreign
	var root := _root()
	var type := str(p.get("type", ""))
	var scene_path := str(p.get("scene", ""))
	if type.is_empty() == scene_path.is_empty():
		return _fail("INVALID_PARAMS", "give exactly one of type or scene")
	var node: Node
	if not scene_path.is_empty():
		if scene_path == root.scene_file_path:
			return _fail("INVALID_PARAMS", "a scene cannot contain an instance of itself")
		if not ResourceLoader.exists(scene_path):
			return _fail("FILE_NOT_FOUND", "%s does not exist" % scene_path)
		var packed := load(scene_path) as PackedScene
		if packed == null:
			return _fail("INVALID_SCENE", "%s is not a scene" % scene_path)
		# Keep the link to the source scene, as dragging it into the editor does.
		node = packed.instantiate(PackedScene.GEN_EDIT_STATE_INSTANCE)
	else:
		var created = _instantiate_type(type)
		if _is_error(created):
			return created
		node = created
	var wanted_name := str(p.get("name", ""))
	if not wanted_name.is_empty():
		if not wanted_name.validate_node_name() == wanted_name:
			node.free()
			return _fail("INVALID_PARAMS", "\"%s\" is not a valid node name" % wanted_name, "names cannot contain . : @ / \" %")
		node.name = wanted_name
	elif not scene_path.is_empty():
		node.name = scene_path.get_file().get_basename().to_pascal_case()
	var props = p.get("properties", {})
	if props is Dictionary and not props.is_empty():
		var changes = _prepare_changes(node, props)
		if _is_error(changes):
			node.free()
			return changes
		for c in changes:
			node.set_indexed(c[0], c[2])

	var ur := _plugin.get_undo_redo()
	ur.create_action("MCP: add %s" % node.name)
	ur.add_do_method(parent, "add_child", node, true)
	ur.add_do_method(node, "set_owner", root)
	ur.add_do_reference(node)
	ur.add_undo_method(parent, "remove_child", node)
	ur.commit_action()
	return {"path": _rel(node), "name": str(node.name), "type": node.get_class()}


func _node_set_properties(p: Dictionary) -> Dictionary:
	var node = _resolve(str(p.get("node_path", "")))
	if _is_error(node):
		return node
	var foreign = _check_editable(node, false)
	if foreign != null:
		return foreign
	var props = p.get("properties", {})
	if not (props is Dictionary) or props.is_empty():
		return _fail("INVALID_PARAMS", "properties must be a non-empty object")
	var changes = _prepare_changes(node, props)
	if _is_error(changes):
		return changes
	var ur := _plugin.get_undo_redo()
	ur.create_action("MCP: set %s on %s" % [", ".join(props.keys()), node.name])
	for c in changes:
		ur.add_do_method(node, "set_indexed", c[0], c[2])
		ur.add_undo_method(node, "set_indexed", c[0], c[1])
	ur.commit_action()
	var out := {}
	for c in changes:
		out[str(c[0])] = Codec.encode(node.get_indexed(c[0]))
	return {"path": _rel(node), "properties": out}


## Collects [node, owner] for node and everything below it that has an
## owner. remove_child() clears owners that end up outside the removed
## branch, so undo has to put each one back.
func _collect_owners(node: Node, out: Array) -> void:
	if node.owner != null:
		out.append([node, node.owner])
	for c in node.get_children():
		_collect_owners(c, out)


func _node_delete(p: Dictionary) -> Dictionary:
	var node = _resolve(str(p.get("node_path", "")))
	if _is_error(node):
		return node
	var root := _root()
	if node == root:
		return _fail("INVALID_PARAMS", "the scene root cannot be deleted", "delete its children, or create a new scene with scene_create")
	var foreign = _check_editable(node, true)
	if foreign != null:
		return foreign
	var path := _rel(node)
	var parent: Node = node.get_parent()
	var owners := []
	_collect_owners(node, owners)
	var ur := _plugin.get_undo_redo()
	ur.create_action("MCP: delete %s" % node.name)
	ur.add_do_method(parent, "remove_child", node)
	ur.add_undo_method(parent, "add_child", node, true)
	ur.add_undo_method(parent, "move_child", node, node.get_index())
	for pair in owners:
		ur.add_undo_method(pair[0], "set_owner", pair[1])
	ur.add_undo_reference(node)
	ur.commit_action()
	var removed := owners.filter(func(pair: Array) -> bool: return pair[1] == root).size()
	return {"deleted": path, "nodes_removed": removed}


func _editor_screenshot(p: Dictionary) -> Dictionary:
	if DisplayServer.get_name() == "headless":
		return _fail("NOT_AVAILABLE_HEADLESS", "the editor runs headless and draws nothing", "open the project in a normal editor window")
	var which := str(p.get("viewport", "auto"))
	if which == "auto" or which.is_empty():
		which = "3d" if _root() is Node3D else "2d"
	if which != "2d" and which != "3d":
		return _fail("INVALID_PARAMS", "viewport must be \"2d\", \"3d\" or \"auto\"")
	# A viewport only renders while its main screen is shown.
	EditorInterface.set_main_screen_editor("3D" if which == "3d" else "2D")
	await _frames(2)
	await RenderingServer.frame_post_draw
	var vp: SubViewport = EditorInterface.get_editor_viewport_3d(0) if which == "3d" else EditorInterface.get_editor_viewport_2d()
	var img := vp.get_texture().get_image()
	if img == null or img.is_empty():
		return _fail("CAPTURE_FAILED", "the %s viewport returned no image" % which, "make sure the editor window is not minimised")
	var max_width := int(p.get("max_width", 1280))
	if max_width <= 0:
		max_width = 1280
	if img.get_width() > max_width:
		img.resize(max_width, int(img.get_height() * float(max_width) / img.get_width()), Image.INTERPOLATE_LANCZOS)
	return {
		"png_base64": Marshalls.raw_to_base64(img.save_png_to_buffer()),
		"width": img.get_width(),
		"height": img.get_height(),
		"viewport": which,
	}
