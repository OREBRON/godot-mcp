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
		"node_set": _node_set,
		"time": _time,
		"input": _input_cmd,
		"screenshot": _screenshot,
		"performance": _performance,
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


## Reads a number from the params.
func _float_param(params: Dictionary, key: String, fallback: float) -> float:
	var raw: Variant = params.get(key, fallback)
	if raw is float or raw is int:
		var f: float = raw
		return f
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
	var wanted: Array = []
	var raw: Variant = params.get("properties", [])
	if raw is Array:
		wanted = raw
	var out: Dictionary = _changed_properties(node) if wanted.is_empty() else _listed_properties(node, wanted)
	if out.has("__error"):
		return out
	return {"path": _rel(node), "type": node.get_class(), "properties": out}


## Stored properties and script variables that differ from their defaults:
## the node's live state without the noise.
func _changed_properties(node: Node) -> Dictionary:
	var out: Dictionary = {}
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
		if typeof(value) != typeof(default) or value != default:
			out[prop] = _encode(value)
	return out


func _listed_properties(node: Node, wanted: Array) -> Dictionary:
	var out: Dictionary = {}
	for key: Variant in wanted:
		var prop: String = str(key)
		if not _has_property(node, prop):
			return _fail("PROPERTY_NOT_FOUND", "%s has no property \"%s\"" % [node.get_class(), prop], "call game_node_get without properties to see what it has")
		out[prop] = _encode(node.get_indexed(NodePath(prop)))
	return out


func _has_property(node: Node, prop: String) -> bool:
	return StringName(prop.get_slice(":", 0)) in node


## Sets properties of a live node, all or none: every value is converted
## first, so a bad one changes nothing. Nothing is saved.
func _node_set(params: Dictionary) -> Dictionary:
	var found: Variant = _resolve(str(params.get("node_path", "")))
	if found is Dictionary:
		return found
	var node: Node = found
	var props: Dictionary = {}
	var raw: Variant = params.get("properties", {})
	if raw is Dictionary:
		props = raw
	if props.is_empty():
		return _fail("INVALID_PARAMS", "properties must be a non-empty object")
	var changes: Array[Array] = []
	for key: Variant in props:
		var change: Variant = _live_change(node, str(key), props[key])
		if change is Dictionary:
			return change
		changes.append(change)
	for change: Array in changes:
		var path: NodePath = change[0]
		node.set_indexed(path, change[1])
	return {"path": _rel(node), "properties": _listed_properties(node, props.keys())}


## [NodePath, value] for one property, or an error.
func _live_change(node: Node, prop: String, value: Variant) -> Variant:
	if not _has_property(node, prop):
		return _fail("PROPERTY_NOT_FOUND", "%s has no property \"%s\"" % [node.get_class(), prop], "call game_node_get without properties to see what it has")
	var path: NodePath = NodePath(prop)
	var current: Variant = node.get_indexed(path)
	var converted: Variant = _convert(value, current)
	if converted == null and value != null:
		return _fail("INVALID_VALUE", "%s: expected %s, e.g. %s" % [prop, type_string(typeof(current)), var_to_str(current)])
	return [path, converted]


## value in the type of current, or null if it does not fit: JSON numbers,
## strings and booleans, [x, y] or [x, y, z] for vectors, anything else in
## Godot's text form such as "Color(1, 0, 0, 1)".
func _convert(value: Variant, current: Variant) -> Variant:
	var want: int = typeof(current)
	if want == TYPE_NIL or typeof(value) == want:
		return value
	if (want == TYPE_INT or want == TYPE_FLOAT) and (value is int or value is float):
		return type_convert(value, want)
	if value is String and (want == TYPE_STRING_NAME or want == TYPE_NODE_PATH):
		return type_convert(value, want)
	if value is String:
		var text: String = value
		var parsed: Variant = str_to_var(text)
		return parsed if typeof(parsed) == want else null
	if value is Array:
		var items: Array = value
		return _vector_from(items, want)
	return null


func _vector_from(items: Array, want: int) -> Variant:
	var nums: Array[float] = []
	for item: Variant in items:
		if not (item is int or item is float):
			return null
		var f: float = item
		nums.append(f)
	if want == TYPE_VECTOR2 and nums.size() == 2:
		return Vector2(nums[0], nums[1])
	if want == TYPE_VECTOR3 and nums.size() == 3:
		return Vector3(nums[0], nums[1], nums[2])
	return null


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


# ---- time ----

## Pauses, resumes or steps the game. The bridge keeps running while the
## tree is paused (PROCESS_MODE_ALWAYS), so it can always answer.
func _time(params: Dictionary) -> Variant:
	var tree: SceneTree = get_tree()
	var action: String = str(params.get("action", "status"))
	var stepped: int = 0
	match action:
		"pause":
			tree.paused = true
		"resume":
			tree.paused = false
		"step":
			stepped = clampi(_int_param(params, "frames", 1), 1, 600)
			tree.paused = false
			# physics_frame is emitted before the nodes' _physics_process, so
			# after the (n+1)-th signal exactly n frames have run, and pausing
			# here stops the next one.
			for i: int in stepped + 1:
				await tree.physics_frame
			tree.paused = true
		"status":
			pass
		_:
			return _fail("INVALID_PARAMS", "unknown action \"%s\"" % action, "use pause, resume, step or status")
	return {
		"paused": tree.paused,
		"stepped_frames": stepped,
		"physics_ticks_per_second": Engine.physics_ticks_per_second,
		"time_scale": Engine.time_scale,
	}


# ---- input ----

const MOUSE_BUTTONS: Dictionary = {
	"left": MOUSE_BUTTON_LEFT,
	"right": MOUSE_BUTTON_RIGHT,
	"middle": MOUSE_BUTTON_MIDDLE,
	"wheel_up": MOUSE_BUTTON_WHEEL_UP,
	"wheel_down": MOUSE_BUTTON_WHEEL_DOWN,
}


## Sends an action, a key, a mouse click or a mouse move. By default the
## input is pressed, held for duration seconds and released; "pressed" sends
## only one half, to hold an input across calls.
func _input_cmd(params: Dictionary) -> Variant:
	var at: Variant = _vec2(params.get("position", null))
	var built: Variant = _build_event(params, at)
	if built is Dictionary:
		return built # an error
	if at != null:
		var pos: Vector2 = at
		_move_mouse(pos)
	if built == null:
		return {"sent": "mouse move to %s" % at}
	var event: InputEvent = built
	return await _press(event, params)


## The pressed event the params describe, null for a mouse move alone, or
## an error.
func _build_event(params: Dictionary, at: Variant) -> Variant:
	var action: String = str(params.get("action", ""))
	var key: String = str(params.get("key", ""))
	var button: String = str(params.get("mouse_button", ""))
	var kinds: int = int(not action.is_empty()) + int(not key.is_empty()) + int(not button.is_empty())
	if kinds > 1 or (kinds == 0 and at == null):
		return _fail("INVALID_PARAMS", "give exactly one of action, key or mouse_button (with position), or position alone to move the mouse")
	if not action.is_empty():
		return _action_event(action)
	if not key.is_empty():
		var ev: InputEventKey = _key_event(key)
		if ev == null:
			return _fail("INVALID_PARAMS", "unknown key \"%s\"" % key, "use Godot key names such as Space, Enter, Escape, Left, A, F1, optionally with modifiers: Ctrl+S, Shift+Tab")
		return ev
	if not button.is_empty():
		return _mouse_event(button, at)
	return null


func _action_event(action: String) -> Variant:
	if not InputMap.has_action(StringName(action)):
		var custom: Array[String] = []
		for a: StringName in InputMap.get_actions():
			if not str(a).begins_with("ui_"):
				custom.append(str(a))
		var listed: String = ", ".join(PackedStringArray(custom)) if not custom.is_empty() else "(none of its own)"
		return _fail("INVALID_PARAMS", "the project has no input action \"%s\"" % action, "its actions: %s, plus the built-in ui_* actions" % listed)
	var ev: InputEventAction = InputEventAction.new()
	ev.action = StringName(action)
	ev.strength = 1.0
	return ev


func _mouse_event(button: String, at: Variant) -> Variant:
	if not MOUSE_BUTTONS.has(button):
		return _fail("INVALID_PARAMS", "unknown mouse_button \"%s\"" % button, "use left, right, middle, wheel_up or wheel_down")
	if at == null:
		return _fail("INVALID_PARAMS", "mouse_button needs position, as [x, y] in the game's viewport coordinates")
	var ev: InputEventMouseButton = InputEventMouseButton.new()
	var index: MouseButton = MOUSE_BUTTONS[button]
	ev.button_index = index
	ev.position = at
	ev.global_position = at
	return ev


## Mouse input goes to the viewport directly, which reaches Controls that
## stop the mouse; Input.parse_input_event would not.
func _move_mouse(at: Vector2) -> void:
	var motion: InputEventMouseMotion = InputEventMouseMotion.new()
	motion.position = at
	motion.global_position = at
	get_viewport().push_input(motion, true)


## Presses and releases event, or sends one half when "pressed" is given.
func _press(event: InputEvent, params: Dictionary) -> Dictionary:
	var described: String = event.as_text()
	var pressed_raw: Variant = params.get("pressed", null)
	if pressed_raw is bool:
		var only: bool = pressed_raw
		_dispatch(event, only)
		return {"sent": "%s %s" % [described, "pressed" if only else "released"]}
	_dispatch(event, true)
	var hold: float = _float_param(params, "duration", 0.1)
	if hold > 0.0:
		# Real seconds, also while paused and whatever the time scale.
		await get_tree().create_timer(hold, true, false, true).timeout
	else:
		await get_tree().process_frame
	_dispatch(event, false)
	return {"sent": "%s pressed and released" % described, "held_seconds": hold}


func _dispatch(event: InputEvent, pressed: bool) -> void:
	var ev: InputEvent = event.duplicate()
	if ev is InputEventAction:
		var a: InputEventAction = ev
		a.pressed = pressed
		Input.parse_input_event(a)
	elif ev is InputEventKey:
		var k: InputEventKey = ev
		k.pressed = pressed
		Input.parse_input_event(k)
	elif ev is InputEventMouseButton:
		var m: InputEventMouseButton = ev
		m.pressed = pressed
		get_viewport().push_input(m, true)


## Builds a key event from "Space", "A", "Ctrl+Shift+S"; null if unknown.
func _key_event(spec: String) -> InputEventKey:
	var parts: PackedStringArray = spec.split("+")
	var code: Key = OS.find_keycode_from_string(parts[parts.size() - 1].strip_edges())
	if code == KEY_NONE:
		return null
	var ev: InputEventKey = InputEventKey.new()
	ev.keycode = code
	ev.physical_keycode = code
	ev.key_label = code
	for i: int in parts.size() - 1:
		match parts[i].strip_edges().to_lower():
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


## [x, y] or {"x": x, "y": y} as a Vector2, or null.
func _vec2(raw: Variant) -> Variant:
	if raw is Array:
		var a: Array = raw
		if a.size() == 2 and (a[0] is float or a[0] is int) and (a[1] is float or a[1] is int):
			var x: float = a[0]
			var y: float = a[1]
			return Vector2(x, y)
	elif raw is Dictionary:
		var d: Dictionary = raw
		if d.has("x") and d.has("y"):
			var x: float = d["x"]
			var y: float = d["y"]
			return Vector2(x, y)
	return null


# ---- screenshot ----

## The game's frame as a PNG, scaled down to max_width. Also reports the
## viewport's logical size, the coordinate space game_input uses: on a HiDPI
## screen the image has more pixels than that.
func _screenshot(params: Dictionary) -> Variant:
	if DisplayServer.get_name() == "headless":
		return _fail("NOT_AVAILABLE_HEADLESS", "the game runs headless and draws nothing", "stop it and start it again with game_run without headless")
	await RenderingServer.frame_post_draw
	var vp: Viewport = get_viewport()
	var img: Image = vp.get_texture().get_image()
	if img == null or img.is_empty():
		return _fail("CAPTURE_FAILED", "the game's viewport returned no image", "make sure the game window is not minimised")
	var logical: Vector2 = vp.get_visible_rect().size
	var max_width: int = _int_param(params, "max_width", 1280)
	if max_width <= 0:
		max_width = 1280
	if img.get_width() > max_width:
		var height: int = roundi(float(img.get_height()) * float(max_width) / float(img.get_width()))
		img.resize(max_width, height, Image.INTERPOLATE_LANCZOS)
	return {
		"png_base64": Marshalls.raw_to_base64(img.save_png_to_buffer()),
		"width": img.get_width(),
		"height": img.get_height(),
		"logical_width": logical.x,
		"logical_height": logical.y,
		"paused": get_tree().paused,
	}


# ---- performance ----

## Counts the frames drawn in sample_seconds of real time and reads the
## engine's monitors. A headless run is paced by Godot's low-processor
## sleep, so its frame rate says little about the game.
func _performance(params: Dictionary) -> Dictionary:
	var seconds: float = clampf(_float_param(params, "sample_seconds", 1.0), 0.1, 10.0)
	var frames_before: int = Engine.get_process_frames()
	var start: int = Time.get_ticks_usec()
	await get_tree().create_timer(seconds, true, false, true).timeout
	var frames: float = float(Engine.get_process_frames() - frames_before)
	var elapsed: float = float(Time.get_ticks_usec() - start) / 1000000.0
	return {
		"measured_fps": frames / elapsed,
		"avg_frame_ms": elapsed * 1000.0 / maxf(frames, 1.0),
		"monitors": _monitors(),
		"headless": DisplayServer.get_name() == "headless",
		"paused": get_tree().paused,
	}


func _monitors() -> Dictionary:
	return {
		"fps": Performance.get_monitor(Performance.TIME_FPS),
		"process_ms": Performance.get_monitor(Performance.TIME_PROCESS) * 1000.0,
		"physics_ms": Performance.get_monitor(Performance.TIME_PHYSICS_PROCESS) * 1000.0,
		"nodes": Performance.get_monitor(Performance.OBJECT_NODE_COUNT),
		"orphan_nodes": Performance.get_monitor(Performance.OBJECT_ORPHAN_NODE_COUNT),
		"objects": Performance.get_monitor(Performance.OBJECT_COUNT),
		"static_memory_mb": Performance.get_monitor(Performance.MEMORY_STATIC) / 1048576.0,
		"draw_calls": Performance.get_monitor(Performance.RENDER_TOTAL_DRAW_CALLS_IN_FRAME),
		"video_memory_mb": Performance.get_monitor(Performance.RENDER_VIDEO_MEM_USED) / 1048576.0,
	}
