@tool
extends RefCounted
## The commands godot-mcp servers can run in the editor. Every scene change
## goes through EditorUndoRedoManager, so the user can revert it with Ctrl+Z.
## A handler returns a Dictionary of data, or _fail(...) for an error.

const Codec := preload("res://addons/godot_mcp/variant_codec.gd")
const ProjectCommands := preload("res://addons/godot_mcp/project_commands.gd")

const MAX_TREE_NODES := 500
const DRAW_TIMEOUT_MS := 5000

var _plugin: EditorPlugin
var _handlers := {}
var _project: ProjectCommands # kept here: its handlers do not keep it alive


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
		"node_signal_connect": _node_signal_connect,
		"node_move": _node_move,
		"node_duplicate": _node_duplicate,
		"editor_screenshot": _editor_screenshot,
	}
	_project = ProjectCommands.new(plugin)
	_handlers.merge(_project.handlers())


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


## Waits for a drawn frame. A minimised or hidden window draws none, and
## waiting for frame_post_draw alone would stall every later command.
func _drawn() -> bool:
	var drawn := [false]
	var mark := func() -> void: drawn[0] = true
	RenderingServer.frame_post_draw.connect(mark, CONNECT_ONE_SHOT)
	var start := Time.get_ticks_msec()
	while not drawn[0] and Time.get_ticks_msec() - start < DRAW_TIMEOUT_MS:
		await _frames(1)
	if RenderingServer.frame_post_draw.is_connected(mark):
		RenderingServer.frame_post_draw.disconnect(mark)
	return drawn[0]


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


func _similar_properties(obj: Object, name: String) -> String:
	var hits := []
	for info in obj.get_property_list():
		if info.usage & PROPERTY_USAGE_EDITOR and (info.name.containsn(name) or name.containsn(info.name) or info.name.similarity(name) > 0.5):
			hits.append(info.name)
	return ", ".join(hits.slice(0, 12))


## Creates a node of a built-in class or a class_name script class.
func _instantiate_type(type: String) -> Variant:
	if ClassDB.class_exists(type):
		return _instantiate_builtin(type)
	var obj := Codec.instantiate_script_class(type)
	if obj != null:
		return obj if obj is Node else _fail("INVALID_TYPE", "the script class %s does not extend Node" % type)
	var close := Array(ClassDB.get_inheriters_from_class("Node")).filter(func(c: String) -> bool: return c.similarity(type) > 0.6 or c.to_lower() == type.to_lower())
	return _fail("INVALID_TYPE", "unknown class \"%s\"" % type, ("did you mean: %s" % ", ".join(close.slice(0, 5))) if not close.is_empty() else "use a built-in class such as Node2D or Sprite2D, or a class_name from your scripts")


func _instantiate_builtin(type: String) -> Variant:
	if not ClassDB.is_parent_class(type, "Node"):
		return _fail("INVALID_TYPE", "%s is not a Node class" % type, "resources such as shapes and materials are not nodes: set them on a node property, e.g. {\"shape\": {\"type\": \"RectangleShape2D\"}}")
	if not ClassDB.can_instantiate(type):
		return _fail("INVALID_TYPE", "%s is abstract and cannot be created" % type, "pick one of its subclasses: %s" % ", ".join(ClassDB.get_inheriters_from_class(type).slice(0, 10)))
	return ClassDB.instantiate(type)


## Converts and checks every property first, so that a bad value changes
## nothing. Returns [[NodePath, old, new], ...] or an error.
func _prepare_changes(node: Node, props: Dictionary) -> Variant:
	var changes := []
	for key in props:
		var name := str(key)
		var info := Codec.property_info(node, name)
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


## A scene_tree page: the walk counts nodes in pre-order, shows at most
## limit of them from offset on, and records where the next page starts.
class TreeWalk:
	var offset := 0
	var index := 0
	var shown := 0
	var limit := MAX_TREE_NODES
	var next := -1


## Describes node and its children down to depth_left levels. A node before
## the page is kept only as a name and path above shown nodes; {} means
## nothing of it is on the page.
func _describe(node: Node, depth_left: int, walk: TreeWalk) -> Dictionary:
	var on_page := walk.index >= walk.offset
	walk.index += 1
	walk.shown += int(on_page)
	var d := _node_info(node) if on_page else {"name": str(node.name), "path": _rel(node)}
	var kids := _owned_children(node)
	if not kids.is_empty():
		d["child_count"] = kids.size()
	var children := _describe_children(kids, depth_left - 1, walk) if depth_left > 0 else []
	if not children.is_empty():
		d["children"] = children
	return d if on_page or not children.is_empty() else {}


func _describe_children(kids: Array, depth_left: int, walk: TreeWalk) -> Array:
	var out := []
	for c in kids:
		if walk.shown >= walk.limit:
			walk.next = walk.index
			break
		var d := _describe(c, depth_left, walk)
		if not d.is_empty():
			out.append(d)
	return out


func _node_info(node: Node) -> Dictionary:
	var d := {"name": str(node.name), "type": node.get_class(), "path": _rel(node)}
	var script: Script = node.get_script()
	if script != null:
		d["script"] = script.resource_path
	if node != _root() and not node.scene_file_path.is_empty():
		d["instance"] = node.scene_file_path
	return d


func _scene_tree(p: Dictionary) -> Dictionary:
	var node = _resolve(str(p.get("node_path", ".")))
	if _is_error(node):
		return node
	var depth := int(p.get("max_depth", 4))
	if depth <= 0:
		depth = 4
	var walk := TreeWalk.new()
	walk.offset = maxi(int(p.get("offset", 0)), 0)
	walk.limit = clampi(int(p.get("limit", MAX_TREE_NODES)), 1, MAX_TREE_NODES)
	var out := {"scene": _root().scene_file_path, "tree": _describe(node, depth, walk), "nodes_shown": walk.shown}
	if walk.next >= 0:
		out["next_offset"] = walk.next
	return out


func _node_get(p: Dictionary) -> Dictionary:
	var node = _resolve(str(p.get("node_path", ".")))
	if _is_error(node):
		return node
	var wanted: Array = p.get("properties", []) if p.get("properties") is Array else []
	var out := _stored_properties(node, bool(p.get("include_defaults", false))) if wanted.is_empty() else _listed_properties(node, wanted)
	if _is_error(out):
		return out
	return {"scene": _root().scene_file_path, "path": _rel(node), "type": node.get_class(), "properties": out}


## Stored properties, by default only those that differ from their defaults.
func _stored_properties(node: Node, include_defaults: bool) -> Dictionary:
	var out := {}
	for info in node.get_property_list():
		if not (info.usage & PROPERTY_USAGE_STORAGE):
			continue
		var value = node.get(info.name)
		if include_defaults or not _is_default(node, info.name, value):
			out[info.name] = Codec.encode(value)
	return out


func _is_default(node: Node, prop: String, value: Variant) -> bool:
	var def = ClassDB.class_get_property_default_value(node.get_class(), prop)
	var script: Script = node.get_script()
	if script != null and def == null:
		def = script.get_property_default_value(prop)
	return (typeof(value) == typeof(def) and value == def) or (value == null and def == null)


func _listed_properties(node: Node, wanted: Array) -> Dictionary:
	var out := {}
	for key in wanted:
		var name := str(key)
		if Codec.property_info(node, name).is_empty():
			var similar := _similar_properties(node, name.get_slice(":", 0))
			return _fail("PROPERTY_NOT_FOUND", "%s has no property \"%s\"" % [node.get_class(), name], ("similar: " + similar) if not similar.is_empty() else "")
		out[name] = Codec.encode(node.get_indexed(NodePath(name)))
	return out


func _scene_open(p: Dictionary) -> Dictionary:
	var path := Codec.to_res_path(str(p.get("path", "")))
	var bad := Codec.check_res_path(path)
	if not bad.is_empty():
		return _fail("INVALID_PATH", bad)
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
	var path := Codec.to_res_path(str(p.get("path", "")))
	var bad := Codec.check_res_path(path)
	if not bad.is_empty():
		return _fail("INVALID_PATH", bad)
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
	if opened == null or opened.scene_file_path != path:
		return _fail("OPEN_FAILED", "saved %s, but the editor did not open it" % path, "open it with scene_open")
	return {"scene": path, "root": {"name": str(opened.name), "type": opened.get_class()}}


func _scene_save(p: Dictionary) -> Dictionary:
	var root := _root()
	if root == null:
		return _fail("NO_SCENE_OPEN", "no scene is open in the editor", "open one with scene_open")
	var target := str(p.get("path", ""))
	if target.is_empty():
		if root.scene_file_path.is_empty():
			return _fail("NO_PATH", "the scene has never been saved", "pass path, e.g. \"res://scenes/level.tscn\"")
		target = root.scene_file_path
	else:
		target = Codec.to_res_path(target)
		var bad := Codec.check_res_path(target)
		if not bad.is_empty():
			return _fail("INVALID_PATH", bad)
		if not target.ends_with(".tscn") and not target.ends_with(".scn"):
			return _fail("INVALID_PARAMS", "path must end with .tscn or .scn")
		DirAccess.make_dir_recursive_absolute(ProjectSettings.globalize_path(target.get_base_dir()))
	# Neither save call reports a failed write (save_scene() returns OK
	# regardless), so check that the file can be written before saving and
	# that it was written afterwards.
	var blocked := _unwritable(target)
	if not blocked.is_empty():
		return _fail("SAVE_FAILED", blocked, "make the file and its folder writable, or save under another path")
	var since := int(Time.get_unix_time_from_system())
	var headless := DisplayServer.get_name() == "headless"
	if target == root.scene_file_path and not headless:
		EditorInterface.save_scene()
	else:
		# A headless editor cannot render the thumbnail that a save makes.
		EditorInterface.save_scene_as(target, not headless)
	await _frames(1)
	var problem := _check_saved(target, root, since)
	if not problem.is_empty():
		return _fail("SAVE_FAILED", problem, "check that the folder is writable and the disk is not full; the editor's Output panel has details")
	return {"saved": target}


## Returns why path cannot be written, or "" if it can.
func _unwritable(path: String) -> String:
	if FileAccess.file_exists(path):
		var f := FileAccess.open(path, FileAccess.READ_WRITE)
		if f == null:
			return "%s cannot be written: %s" % [path, error_string(FileAccess.get_open_error())]
		f.close()
		return ""
	# A hidden name, which the editor's file system ignores.
	var probe := path.get_base_dir().path_join(".godot_mcp_write_test")
	var f := FileAccess.open(probe, FileAccess.WRITE)
	if f == null:
		return "the folder %s cannot be written: %s" % [path.get_base_dir(), error_string(FileAccess.get_open_error())]
	f.close()
	DirAccess.remove_absolute(ProjectSettings.globalize_path(probe))
	return ""


## Returns why path does not hold a save of root made at or after since
## (Unix seconds), or "" if it does.
func _check_saved(path: String, root: Node, since: int) -> String:
	if not FileAccess.file_exists(path):
		return "the editor did not write %s" % path
	if FileAccess.get_modified_time(path) < since:
		return "%s was not rewritten; it still holds the previous save" % path
	if path.ends_with(".tscn") and not FileAccess.get_file_as_string(path).contains("[node name=\"%s\"" % root.name):
		return "%s does not contain the scene's root node %s" % [path, root.name]
	return ""


func _node_add(p: Dictionary) -> Dictionary:
	var parent = _resolve(str(p.get("parent_path", ".")))
	if _is_error(parent):
		return parent
	var foreign = _check_editable(parent, false)
	if foreign != null:
		return foreign
	var node = _new_node(p)
	if _is_error(node):
		return node
	var problem = _configure_new_node(node, p)
	if problem != null:
		node.free()
		return problem
	var root := _root()
	var ur := _plugin.get_undo_redo()
	ur.create_action("MCP: add %s" % node.name)
	ur.add_do_method(parent, "add_child", node, true)
	ur.add_do_method(node, "set_owner", root)
	ur.add_do_reference(node)
	ur.add_undo_method(parent, "remove_child", node)
	ur.commit_action()
	return {"scene": root.scene_file_path, "path": _rel(node), "name": str(node.name), "type": node.get_class()}


## A node of the given type, or an instance of the given scene.
func _new_node(p: Dictionary) -> Variant:
	var type := str(p.get("type", ""))
	var scene_path := str(p.get("scene", ""))
	if type.is_empty() == scene_path.is_empty():
		return _fail("INVALID_PARAMS", "give exactly one of type or scene")
	if type.is_empty():
		return _instance(scene_path)
	return _instantiate_type(type)


func _instance(scene_path: String) -> Variant:
	scene_path = Codec.to_res_path(scene_path)
	var bad := Codec.check_res_path(scene_path)
	if not bad.is_empty():
		return _fail("INVALID_PATH", bad)
	if scene_path == _root().scene_file_path:
		return _fail("INVALID_PARAMS", "a scene cannot contain an instance of itself")
	if not ResourceLoader.exists(scene_path):
		return _fail("FILE_NOT_FOUND", "%s does not exist" % scene_path)
	var packed := load(scene_path) as PackedScene
	if packed == null:
		return _fail("INVALID_SCENE", "%s is not a scene" % scene_path)
	# Keep the link to the source scene, as dragging it into the editor does.
	var node := packed.instantiate(PackedScene.GEN_EDIT_STATE_INSTANCE)
	node.name = scene_path.get_file().get_basename().to_pascal_case()
	return node


## Names the new node and sets its properties; returns an error or null.
func _configure_new_node(node: Node, p: Dictionary) -> Variant:
	var wanted_name := str(p.get("name", ""))
	if not wanted_name.is_empty() and wanted_name.validate_node_name() != wanted_name:
		return _fail("INVALID_PARAMS", "\"%s\" is not a valid node name" % wanted_name, "names cannot contain . : @ / \" %")
	if not wanted_name.is_empty():
		node.name = wanted_name
	var props = p.get("properties", {})
	if not (props is Dictionary) or props.is_empty():
		return null
	var changes = _prepare_changes(node, props)
	if _is_error(changes):
		return changes
	for c in changes:
		node.set_indexed(c[0], c[2])
	return null


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
	return {"scene": _root().scene_file_path, "path": _rel(node), "properties": out}


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
	return {"scene": root.scene_file_path, "deleted": path, "nodes_removed": removed}


func _node_signal_connect(p: Dictionary) -> Dictionary:
	var from = _resolve(str(p.get("from", "")))
	if _is_error(from):
		return from
	var to = _resolve(str(p.get("to", "")))
	if _is_error(to):
		return to
	var sig := str(p.get("signal", ""))
	var method := str(p.get("method", ""))
	var problem = _check_connection(from, sig, to, method)
	if problem != null:
		return problem
	var callable := Callable(to, method)
	var remove := bool(p.get("disconnect", false))
	if from.is_connected(sig, callable) == remove:
		_record_connection(from, sig, callable, remove)
	return {"scene": _root().scene_file_path, "connected": not remove, "connections": _connections(from, sig)}


func _check_connection(from: Node, sig: String, to: Node, method: String) -> Variant:
	var foreign = _check_editable(from, false)
	if foreign != null:
		return foreign
	if not from.has_signal(sig):
		var names := from.get_signal_list().map(func(s: Dictionary) -> String: return str(s.name))
		return _fail("SIGNAL_NOT_FOUND", "%s has no signal \"%s\"" % [from.get_class(), sig], "its signals: " + ", ".join(names.slice(0, 25)))
	if not _has_method(to, method):
		var script: Script = to.get_script()
		var where := script.resource_path if script != null else "a script attached to it"
		var hint := "add func %s() to %s first, then run script_check" % [method, where]
		if script != null and _open_in_script_editor(script) and _changed_on_disk(script):
			hint = "%s is open in the script editor, which holds its own copy; ask the user to reload it from disk there" % where
		return _fail("METHOD_NOT_FOUND", "\"%s\" has no method %s" % [_rel(to), method], hint)
	return null


## Whether node's class or script (or a script it extends) has method.
func _has_method(node: Node, method: String) -> bool:
	if ClassDB.class_has_method(node.get_class(), method):
		return true
	var script: Script = node.get_script()
	while script != null:
		_reload_if_changed(script)
		if script.get_script_method_list().any(func(m: Dictionary) -> bool: return m.name == method):
			return true
		script = script.get_base_script()
	return false


## Agents edit scripts with their own file tools, and the editor rereads a
## changed script only when its window gets focus. Reload it as the editor
## would, unless the script editor has it open: that copy may hold the
## user's unsaved changes.
func _reload_if_changed(script: Script) -> void:
	if _changed_on_disk(script) and not _open_in_script_editor(script):
		script.source_code = FileAccess.get_file_as_string(script.resource_path)
		script.reload(true)


func _changed_on_disk(script: Script) -> bool:
	var path := script.resource_path
	if not path.ends_with(".gd") or not FileAccess.file_exists(path):
		return false # built-in scripts live in their scene
	return FileAccess.get_file_as_string(path) != script.source_code


func _open_in_script_editor(script: Script) -> bool:
	return EditorInterface.get_script_editor().get_open_scripts().has(script)


## Connects or disconnects as one undoable action. CONNECT_PERSIST makes the
## editor save the connection with the scene, as its Node dock does.
func _record_connection(from: Node, sig: String, callable: Callable, remove: bool) -> void:
	var ur := _plugin.get_undo_redo()
	ur.create_action("MCP: %s %s" % ["disconnect" if remove else "connect", sig])
	if remove:
		ur.add_do_method(from, "disconnect", sig, callable)
		ur.add_undo_method(from, "connect", sig, callable, CONNECT_PERSIST)
	else:
		ur.add_do_method(from, "connect", sig, callable, CONNECT_PERSIST)
		ur.add_undo_method(from, "disconnect", sig, callable)
	ur.commit_action()


func _connections(from: Node, sig: String) -> Array:
	var out := []
	for c in from.get_signal_connection_list(sig):
		var target = c.callable.get_object()
		if target is Node and _inside(target):
			out.append({"to": _rel(target), "method": str(c.callable.get_method())})
	return out


## Renames, reparents or reorders a node, or toggles Editable Children on
## an instanced scene, as one undoable action.
func _node_move(p: Dictionary) -> Dictionary:
	var node = _resolve(str(p.get("node_path", "")))
	if _is_error(node):
		return node
	var problem = _check_move(node, p)
	if problem != null:
		return problem
	var ur := _plugin.get_undo_redo()
	ur.create_action("MCP: move %s" % node.name)
	_add_rename(ur, node, str(p.get("new_name", "")))
	_add_reparent(ur, node, p)
	_add_editable(ur, node, p.get("editable_children"))
	ur.commit_action()
	return {"scene": _root().scene_file_path, "path": _rel(node), "name": str(node.name)}


func _check_move(node: Node, p: Dictionary) -> Variant:
	var new_name := str(p.get("new_name", ""))
	var moving := p.has("new_parent") or p.has("index")
	if new_name.is_empty() and not moving and not p.has("editable_children"):
		return _fail("INVALID_PARAMS", "give new_name, new_parent, index or editable_children")
	if not new_name.is_empty() and new_name.validate_node_name() != new_name:
		return _fail("INVALID_PARAMS", "\"%s\" is not a valid node name" % new_name, "names cannot contain . : @ / \" %")
	if p.has("editable_children") and (node == _root() or node.scene_file_path.is_empty()):
		return _fail("INVALID_PARAMS", "editable_children only applies to an instanced scene")
	if node == _root() and moving:
		return _fail("INVALID_PARAMS", "the scene root cannot be moved")
	if new_name.is_empty() and not moving:
		return null
	var foreign = _check_editable(node, true)
	if foreign != null or not p.has("new_parent"):
		return foreign
	return _check_new_parent(node, str(p.new_parent))


func _check_new_parent(node: Node, path: String) -> Variant:
	var parent = _resolve(path)
	if _is_error(parent):
		return parent
	if parent == node or node.is_ancestor_of(parent):
		return _fail("INVALID_PARAMS", "a node cannot move into itself or its own children")
	return _check_editable(parent, false)


func _add_rename(ur: EditorUndoRedoManager, node: Node, new_name: String) -> void:
	if new_name.is_empty():
		return
	ur.add_do_method(node, "set_name", new_name)
	ur.add_undo_method(node, "set_name", node.name)


## Moves under new_parent (keeping the global transform) and to index. The
## owners in the branch are put back, since leaving the tree clears them.
func _add_reparent(ur: EditorUndoRedoManager, node: Node, p: Dictionary) -> void:
	if not (p.has("new_parent") or p.has("index")):
		return
	var old_parent := node.get_parent()
	var new_parent: Node = _resolve(str(p.new_parent)) if p.has("new_parent") else old_parent
	var owners := []
	_collect_owners(node, owners)
	if new_parent != old_parent:
		ur.add_do_method(node, "reparent", new_parent, true)
		ur.add_undo_method(node, "reparent", old_parent, true)
		for pair in owners:
			ur.add_do_method(pair[0], "set_owner", pair[1])
			ur.add_undo_method(pair[0], "set_owner", pair[1])
	if p.has("index"):
		ur.add_do_method(new_parent, "move_child", node, int(p.index))
	ur.add_undo_method(old_parent, "move_child", node, node.get_index())


func _add_editable(ur: EditorUndoRedoManager, node: Node, editable: Variant) -> void:
	if editable == null:
		return
	var root := _root()
	ur.add_do_method(root, "set_editable_instance", node, bool(editable))
	ur.add_undo_method(root, "set_editable_instance", node, root.is_editable_instance(node))


## Copies a node and its children next to it, as one undoable action.
func _node_duplicate(p: Dictionary) -> Dictionary:
	var node = _resolve(str(p.get("node_path", "")))
	if _is_error(node):
		return node
	if node == _root():
		return _fail("INVALID_PARAMS", "the scene root cannot be duplicated")
	var foreign = _check_editable(node, true)
	if foreign != null:
		return foreign
	var copy: Node = node.duplicate()
	var wanted := str(p.get("name", ""))
	if not wanted.is_empty() and wanted.validate_node_name() != wanted:
		copy.free()
		return _fail("INVALID_PARAMS", "\"%s\" is not a valid node name" % wanted)
	if not wanted.is_empty():
		copy.name = wanted
	_record_duplicate(node, copy)
	return {"scene": _root().scene_file_path, "path": _rel(copy), "name": str(copy.name)}


func _record_duplicate(node: Node, copy: Node) -> void:
	var parent := node.get_parent()
	var root := _root()
	var ur := _plugin.get_undo_redo()
	ur.create_action("MCP: duplicate %s" % node.name)
	ur.add_do_method(parent, "add_child", copy, true)
	ur.add_do_method(parent, "move_child", copy, node.get_index() + 1)
	for n in _owned_counterparts(node, copy):
		ur.add_do_method(n, "set_owner", root)
	ur.add_do_reference(copy)
	ur.add_undo_method(parent, "remove_child", copy)
	ur.commit_action()


## The nodes in copy whose original in node belongs to the edited scene;
## the insides of instances keep their instance as owner.
func _owned_counterparts(node: Node, copy: Node) -> Array:
	var owners := []
	_collect_owners(node, owners)
	var root := _root()
	return owners.filter(func(pair: Array) -> bool: return pair[1] == root).map(func(pair: Array) -> Node: return copy.get_node(node.get_path_to(pair[0])))


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
	if not await _drawn():
		return _fail("NOT_DRAWING", "the editor window drew no frame within %d ms" % DRAW_TIMEOUT_MS, "bring the editor window to the front; a minimised or hidden window draws nothing")
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
