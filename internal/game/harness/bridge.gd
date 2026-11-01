extends Node
## Connects a game started by godot-mcp's game_run back to the server and
## runs the server's commands one at a time. It does nothing unless the server
## set GODOT_MCP_PORT and GODOT_MCP_TOKEN, and it keeps working while the game
## is paused. Written to raise no GDScript warning (see harness.gd).

const PROTOCOL: int = 1

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
