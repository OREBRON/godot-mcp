@tool
extends RefCounted
## Accepts godot-mcp servers on 127.0.0.1. Nothing here touches the editor:
## the plugin calls poll() from _process, on the main thread, and runs the
## returned requests itself.

const PROTOCOL := 1
const AUTH_TIMEOUT_MS := 5000

var port := 0
var token := ""

var _server := TCPServer.new()
var _peers := {} # peer id -> {"ws": WebSocketPeer, "authed": bool, "since": int}
var _next_id := 1


## Listens on the first free port in [first_port, first_port + tries).
func start(first_port: int, tries: int) -> Error:
	for p in range(first_port, first_port + tries):
		# Loopback only: nothing outside this machine can connect, and
		# Windows Firewall does not ask the user for permission.
		if _server.listen(p, "127.0.0.1") == OK:
			port = p
			token = Crypto.new().generate_random_bytes(32).hex_encode()
			return OK
	return ERR_CANT_CREATE


func stop() -> void:
	for id in _peers:
		_peers[id].ws.close(1001, "editor closing")
	_peers.clear()
	_server.stop()


## Accepts new connections, handles auth and returns up to max_requests
## authenticated requests as [peer_id, message] pairs.
func poll(hello: Callable, max_requests: int) -> Array:
	_accept_new()
	var requests := []
	for id in _peers.keys():
		if _keep_alive(id):
			_read_requests(id, hello, max_requests, requests)
	return requests


func _accept_new() -> void:
	while _server.is_connection_available():
		var ws := WebSocketPeer.new()
		ws.inbound_buffer_size = 1 << 20
		ws.outbound_buffer_size = 64 << 20 # screenshots
		if ws.accept_stream(_server.take_connection()) == OK:
			_peers[_next_id] = {"ws": ws, "authed": false, "since": Time.get_ticks_msec()}
			_next_id += 1


## Polls a peer and drops it when it closed or failed to authenticate in
## time. Returns whether it is open and may send requests.
func _keep_alive(id: int) -> bool:
	var peer: Dictionary = _peers[id]
	var ws: WebSocketPeer = peer.ws
	ws.poll()
	var state := ws.get_ready_state()
	if state == WebSocketPeer.STATE_CLOSED:
		_peers.erase(id)
		return false
	if not peer.authed and Time.get_ticks_msec() - int(peer.since) > AUTH_TIMEOUT_MS:
		if peer.get("closing", false):
			_peers.erase(id) # gave it a full timeout to close cleanly
		else:
			ws.close(4001, "no auth")
			peer.closing = true
			peer.since = Time.get_ticks_msec()
		return false
	return state == WebSocketPeer.STATE_OPEN and not peer.get("closing", false)


## Appends the peer's requests as [id, message]; the first message of a
## peer that is not authenticated yet is its auth.
func _read_requests(id: int, hello: Callable, max_requests: int, requests: Array) -> void:
	var peer: Dictionary = _peers[id]
	var ws: WebSocketPeer = peer.ws
	while not peer.get("closing", false) and ws.get_available_packet_count() > 0 and requests.size() < max_requests:
		var msg = JSON.parse_string(ws.get_packet().get_string_from_utf8())
		if typeof(msg) != TYPE_DICTIONARY:
			continue
		if peer.authed:
			requests.append([id, msg])
		else:
			_authenticate(id, peer, msg, hello)


func _authenticate(id: int, peer: Dictionary, msg: Dictionary, hello: Callable) -> void:
	if msg.get("type") != "auth" or not _same(str(msg.get("token", "")), token):
		send(id, {"type": "error", "error": {"code": "AUTH_FAILED", "message": "wrong token", "hint": "the server must read the token from bridge.json of this project"}})
		peer.ws.close(4001, "auth failed")
		peer.closing = true
		return
	if int(msg.get("protocol", 0)) != PROTOCOL:
		send(id, {"type": "error", "error": {"code": "PROTOCOL_MISMATCH", "message": "plugin speaks protocol %d, server sent %s" % [PROTOCOL, msg.get("protocol")], "hint": "update the plugin and the godot-mcp binary to the same release"}})
		peer.ws.close(4002, "protocol mismatch")
		peer.closing = true
		return
	peer.authed = true
	send(id, hello.call())


## Compares without stopping at the first difference.
func _same(a: String, b: String) -> bool:
	if a.length() != b.length():
		return false
	var diff := 0
	for i in a.length():
		diff |= a.unicode_at(i) ^ b.unicode_at(i)
	return diff == 0


func send(peer_id: int, data: Dictionary) -> void:
	if _peers.has(peer_id):
		_peers[peer_id].ws.send_text(JSON.stringify(data))
