extends Node
## Connects a game started by godot-mcp's game_run back to the server and
## runs the server's commands one at a time. It does nothing unless the server
## set GODOT_MCP_PORT and GODOT_MCP_TOKEN, and it keeps working while the game
## is paused. Written to raise no GDScript warning (see harness.gd).

const PROTOCOL: int = 1
const MAX_TREE_NODES: int = 500
const MAX_TEXT: int = 4000

var _ws: WebSocketPeer = WebSocketPeer.new()
var _token: String = ""
var _greeted: bool = false
var _queue: Array[Dictionary] = []
var _running: bool = false
var _handlers: Dictionary = {}


func _ready() -> void:
	process_mode = Node.PROCESS_MODE_ALWAYS
	var port: String = OS.get_environment("GODOT_MCP_PORT")
	_token = OS.get_environment("GODOT_MCP_TOKEN")
	if port.is_empty() or _token.is_empty():
		queue_free()
		return
	_handlers = {
		"ping": _ping,
		"scene_tree": _scene_tree,
		"node_get": _node_get,
	}
	_ws.outbound_buffer_size = 64 << 20 # screenshots
	_ws.inbound_buffer_size = 1 << 20
	var err: Error = _ws.connect_to_url("ws://127.0.0.1:%s" % port)
	if err != OK:
		push_warning("godot-mcp: cannot reach the server: %s" % error_string(err))
		queue_free()


func _process(_delta: float) -> void:
	_ws.poll()
	var state: WebSocketPeer.State = _ws.get_ready_state()
	if state == WebSocketPeer.STATE_CLOSED:
		set_process(false) # the server went away; the game carries on
		return
	if state != WebSocketPeer.STATE_OPEN:
		return
	if not _greeted:
		_greeted = true
		_send({
			"type": "hello",
			"token": _token,
			"protocol": PROTOCOL,
			"godot_version": Engine.get_version_info().string,
			"headless": DisplayServer.get_name() == "headless",
			"commands": _handlers.keys(),
		})
	while _ws.get_available_packet_count() > 0:
		var msg: Variant = JSON.parse_string(_ws.get_packet().get_string_from_utf8())
		if msg is Dictionary:
			var request: Dictionary = msg
			_queue.append(request)
	if not _running and not _queue.is_empty():
		@warning_ignore("missing_await")
		_drain()


## Runs queued commands one after another; a command may wait for frames.
func _drain() -> void:
	_running = true
	while not _queue.is_empty():
		var msg: Dictionary = _queue.pop_front()
		var params: Dictionary = {}
		var raw: Variant = msg.get("params", {})
		if raw is Dictionary:
			params = raw
		var reply: Dictionary = await _run(str(msg.get("command", "")), params)
		reply["id"] = msg.get("id", "")
		_send(reply)
	_running = false


func _run(command: String, params: Dictionary) -> Dictionary:
	if not _handlers.has(command):
		return {"ok": false, "error": {"code": "UNKNOWN_COMMAND", "message": "unknown game command \"%s\"" % command, "hint": "the server and its bridge come from the same binary; restart the game with game_run"}}
	var handler: Callable = _handlers[command]
	var result: Variant = await handler.call(params)
	if result is Dictionary:
		var data: Dictionary = result
		if data.has("__error"):
			return {"ok": false, "error": data["__error"]}
	return {"ok": true, "data": result}


func _send(data: Dictionary) -> void:
	var err: Error = _ws.send_text(JSON.stringify(data))
	if err != OK:
		push_warning("godot-mcp: cannot send to the server: %s" % error_string(err))


## Reads a whole number from the params; JSON numbers arrive as floats.
func _int_param(params: Dictionary, key: String, fallback: int) -> int:
	var raw: Variant = params.get(key, fallback)
	if raw is float:
		var f: float = raw
		return int(f)
	if raw is int:
		var i: int = raw
		return i
	return fallback


func _fail(code: String, message: String, hint: String = "") -> Dictionary:
	var e: Dictionary = {"code": code, "message": message}
	if not hint.is_empty():
		e["hint"] = hint
	return {"__error": e}


func _ping(_params: Dictionary) -> Dictionary:
	var scene: Node = get_tree().current_scene
	return {
		"scene": scene.scene_file_path if scene != null else "",
		"paused": get_tree().paused,
		"headless": DisplayServer.get_name() == "headless",
	}


# ---- the live scene ----

## Finds a node: paths are relative to the current scene ("." is its root),
## or absolute ("/root/Game") to reach autoloads.
func _resolve(path: String) -> Variant:
	var scene: Node = get_tree().current_scene
	var p: String = path.strip_edges()
	var node: Node = null
	if p.begins_with("/"):
		node = get_tree().root.get_node_or_null(NodePath(p))
	elif scene == null:
		return _fail("NO_SCENE", "the game has no current scene", "it may be changing scenes; try again")
	elif p.is_empty() or p == ".":
		node = scene
	else:
		node = scene.get_node_or_null(NodePath(p))
	if node == null or node == self or is_ancestor_of(node):
		var names: Array[String] = []
		if scene != null:
			for child: Node in scene.get_children():
				names.append(str(child.name))
		var listed: String = ", ".join(PackedStringArray(names)) if not names.is_empty() else "(none)"
		return _fail("NODE_NOT_FOUND", "no node at \"%s\"" % p, "children of the current scene: %s; autoloads are under /root" % listed)
	return node


## A node's path relative to the current scene, or absolute outside it.
func _rel(node: Node) -> String:
	var scene: Node = get_tree().current_scene
	if scene != null and (node == scene or scene.is_ancestor_of(node)):
		return str(scene.get_path_to(node))
	return str(node.get_path())


func _describe(node: Node, depth_left: int, count: Array[int]) -> Dictionary:
	count[0] += 1
	var d: Dictionary = {"name": str(node.name), "type": node.get_class(), "path": _rel(node)}
	var script: Script = node.get_script()
	if script != null:
		d["script"] = script.resource_path
	if node is CanvasItem:
		var item: CanvasItem = node
		if not item.visible:
			d["visible"] = false
	elif node is Node3D:
		var spatial: Node3D = node
		if not spatial.visible:
			d["visible"] = false
	var kids: Array[Node] = []
	for child: Node in node.get_children():
		if child != self:
			kids.append(child)
	if kids.is_empty():
		return d
	if depth_left <= 0 or count[0] >= MAX_TREE_NODES:
		d["child_count"] = kids.size()
		return d
	var children: Array[Dictionary] = []
	for child: Node in kids:
		if count[0] >= MAX_TREE_NODES:
			d["children_not_shown"] = kids.size() - children.size()
			break
		children.append(_describe(child, depth_left - 1, count))
	d["children"] = children
	return d


func _scene_tree(params: Dictionary) -> Dictionary:
	var found: Variant = _resolve(str(params.get("node_path", ".")))
	if found is Dictionary:
		return found
	var node: Node = found
	var depth: int = _int_param(params, "max_depth", 4)
	if depth <= 0:
		depth = 4
	var count: Array[int] = [0]
	var tree: Dictionary = _describe(node, depth, count)
	var scene: Node = get_tree().current_scene
	return {
		"scene": scene.scene_file_path if scene != null else "",
		"paused": get_tree().paused,
		"tree": tree,
		"nodes_shown": count[0],
		"limit_reached": count[0] >= MAX_TREE_NODES,
	}


func _node_get(params: Dictionary) -> Dictionary:
	var found: Variant = _resolve(str(params.get("node_path", ".")))
	if found is Dictionary:
		return found
	var node: Node = found
	var out: Dictionary = {}
	var wanted: Array = []
	var raw: Variant = params.get("properties", [])
	if raw is Array:
		wanted = raw
	if wanted.is_empty():
		# Stored properties and script variables that differ from their
		# defaults: the node's live state without the noise.
		var script: Script = node.get_script()
		for info: Dictionary in node.get_property_list():
			var usage: int = info["usage"]
			if usage & (PROPERTY_USAGE_STORAGE | PROPERTY_USAGE_SCRIPT_VARIABLE) == 0:
				continue
			var prop: String = info["name"]
			var value: Variant = node.get(prop)
			var default: Variant = ClassDB.class_get_property_default_value(node.get_class(), prop)
			if script != null and default == null:
				default = script.get_property_default_value(prop)
			if typeof(value) == typeof(default) and value == default:
				continue
			out[prop] = _encode(value)
	else:
		for key: Variant in wanted:
			var prop: String = str(key)
			if not (StringName(prop.get_slice(":", 0)) in node):
				return _fail("PROPERTY_NOT_FOUND", "%s has no property \"%s\"" % [node.get_class(), prop], "call game_node_get without properties to see what it has")
			out[prop] = _encode(node.get_indexed(NodePath(prop)))
	return {"path": _rel(node), "type": node.get_class(), "properties": out}


## Turns a value into JSON: plain values as they are, saved resources as
## res:// paths, anything else in Godot's text form ("Vector2(1, 2)").
func _encode(v: Variant) -> Variant:
	match typeof(v):
		TYPE_NIL, TYPE_BOOL, TYPE_INT, TYPE_FLOAT, TYPE_STRING:
			return v
		TYPE_STRING_NAME, TYPE_NODE_PATH:
			return str(v)
		TYPE_OBJECT:
			if not is_instance_valid(v):
				return null
			var obj: Object = v
			if obj is Resource:
				var res: Resource = obj
				if not res.resource_path.is_empty() and not res.resource_path.contains("::"):
					return res.resource_path
				return "<%s (embedded)>" % res.get_class()
			if obj is Node:
				var n: Node = obj
				return "<Node %s>" % _rel(n)
			return "<%s>" % obj.get_class()
		TYPE_ARRAY:
			var items: Array = v
			var list: Array = []
			for item: Variant in items:
				list.append(_encode(item))
			return list
		TYPE_DICTIONARY:
			var dict: Dictionary = v
			var map: Dictionary = {}
			for k: Variant in dict:
				map[str(k)] = _encode(dict[k])
			return map
	var text: String = var_to_str(v)
	if text.length() > MAX_TEXT:
		return text.substr(0, MAX_TEXT) + "... (%d characters, cut)" % text.length()
	return text
