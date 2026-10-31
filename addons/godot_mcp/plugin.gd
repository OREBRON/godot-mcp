@tool
extends EditorPlugin
## Entry point of the Godot MCP plugin. Starts the bridge, publishes its port
## and token in <project>/.godot/godot_mcp/bridge.json and runs the commands
## that godot-mcp servers send.

const BridgeServer := preload("res://addons/godot_mcp/bridge_server.gd")
const Commands := preload("res://addons/godot_mcp/commands.gd")

const VERSION := "0.1.0"
const FIRST_PORT := 6510
const PORT_TRIES := 20
const MAX_REQUESTS_PER_FRAME := 32

var _server: BridgeServer
var _commands: Commands
var _info_path := ""
var _queue := [] # [peer_id, message] pairs waiting to run
var _running := false


func _enter_tree() -> void:
	_commands = Commands.new(self)
	_server = BridgeServer.new()
	if _server.start(FIRST_PORT, PORT_TRIES) != OK:
		push_error("Godot MCP: no free port on 127.0.0.1 between %d and %d." % [FIRST_PORT, FIRST_PORT + PORT_TRIES - 1])
		_server = null
		return
	_write_info()
	print("Godot MCP: listening on 127.0.0.1:%d" % _server.port)


func _exit_tree() -> void:
	if _server != null:
		_server.stop()
		_server = null
	_remove_info()


func _process(_delta: float) -> void:
	if _server == null:
		return
	_queue.append_array(_server.poll(_hello, MAX_REQUESTS_PER_FRAME))
	if not _running and not _queue.is_empty():
		_drain()


## Runs queued requests one after another. Commands may wait for frames, and
## two sessions' commands running side by side could otherwise see the
## edited scene change under them halfway (one opens a scene while the
## other is adding a node).
func _drain() -> void:
	_running = true
	while not _queue.is_empty() and _server != null:
		var request: Array = _queue.pop_front()
		await _handle(request[0], request[1])
	_running = false


## Runs one request and sends its reply.
func _handle(peer_id: int, msg: Dictionary) -> void:
	var params = msg.get("params", {})
	if typeof(params) != TYPE_DICTIONARY:
		params = {}
	var reply: Dictionary = await _commands.run(str(msg.get("command", "")), params)
	reply["id"] = msg.get("id", "")
	if _server != null:
		_server.send(peer_id, reply)


func _hello() -> Dictionary:
	return {
		"type": "hello",
		"protocol": BridgeServer.PROTOCOL,
		"godot_version": Engine.get_version_info().string,
		"plugin_version": VERSION,
		"commands": _commands.names(),
	}


func _data_dir() -> String:
	var hidden: bool = ProjectSettings.get_setting("application/config/use_hidden_project_data_directory", true)
	return "res://.godot" if hidden else "res://godot"


func _write_info() -> void:
	var dir := ProjectSettings.globalize_path(_data_dir() + "/godot_mcp")
	DirAccess.make_dir_recursive_absolute(dir)
	var windows := OS.get_name() == "Windows"
	if not windows:
		# Only the owner may enter the folder, so no other user can open
		# bridge.json, not even before the file's own permissions are set.
		# On Windows the folder keeps the project folder's permissions.
		var err := FileAccess.set_unix_permissions(dir, FileAccess.UNIX_READ_OWNER | FileAccess.UNIX_WRITE_OWNER | FileAccess.UNIX_EXECUTE_OWNER)
		if err != OK:
			push_warning("Godot MCP: could not make %s private (%s); other users of this computer may be able to read the bridge token." % [dir, error_string(err)])
	_info_path = dir.path_join("bridge.json")
	# Write a temporary file and rename it, so that a server never reads a
	# half-written one.
	var tmp := _info_path + ".tmp"
	var f := FileAccess.open(tmp, FileAccess.WRITE)
	if f == null:
		push_error("Godot MCP: cannot write %s: %s" % [tmp, error_string(FileAccess.get_open_error())])
		return
	f.store_string(JSON.stringify({
		"protocol": BridgeServer.PROTOCOL,
		"port": _server.port,
		"token": _server.token,
		"pid": OS.get_process_id(),
		"godot_version": Engine.get_version_info().string,
		"plugin_version": VERSION,
		"project_path": ProjectSettings.globalize_path("res://"),
	}, "  "))
	f.close()
	if not windows:
		FileAccess.set_unix_permissions(tmp, FileAccess.UNIX_READ_OWNER | FileAccess.UNIX_WRITE_OWNER)
	elif FileAccess.file_exists(_info_path):
		DirAccess.remove_absolute(_info_path) # a rename does not replace a file on Windows
	var err := DirAccess.rename_absolute(tmp, _info_path)
	if err != OK:
		push_error("Godot MCP: cannot write %s: %s" % [_info_path, error_string(err)])


func _remove_info() -> void:
	if _info_path.is_empty() or not FileAccess.file_exists(_info_path):
		return
	# Another editor on the same project may have replaced the file since.
	var info = JSON.parse_string(FileAccess.get_file_as_string(_info_path))
	if typeof(info) == TYPE_DICTIONARY and int(info.get("pid", -1)) == OS.get_process_id():
		DirAccess.remove_absolute(_info_path)
