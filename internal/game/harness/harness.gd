extends Node
## Written by godot-mcp into <project>/.godot/godot_mcp/run/ and launched by
## game_run instead of the scene the agent asked for. It adds the bridge,
## which talks to the server, and then changes to the real scene, so the game
## runs as it would on its own. Nothing is added to the project itself.
##
## This file and bridge.gd are compiled with the project's own warning
## settings, which may turn any warning into an error, so they are written
## to raise none: explicit types, no casts from Variant, every result used.

const Bridge: GDScript = preload("bridge.gd")


func _ready() -> void:
	var tree: SceneTree = get_tree()
	if DisplayServer.get_name() == "headless":
		# The headless window is 64x64. Give the game the size its window
		# would have on screen, so the UI lays out, and clicks land, as they
		# would there.
		var width: int = ProjectSettings.get_setting("display/window/size/window_width_override", 0)
		var height: int = ProjectSettings.get_setting("display/window/size/window_height_override", 0)
		if width <= 0 or height <= 0:
			width = ProjectSettings.get_setting("display/window/size/viewport_width", 1152)
			height = ProjectSettings.get_setting("display/window/size/viewport_height", 648)
		tree.root.size = Vector2i(width, height)
	var bridge: Node = Bridge.new()
	bridge.name = "GodotMCPBridge"
	tree.root.add_child.call_deferred(bridge)
	var path: String = OS.get_environment("GODOT_MCP_SCENE")
	if path.is_empty():
		path = str(ProjectSettings.get_setting("application/run/main_scene", ""))
	var packed: PackedScene = load(path) as PackedScene
	if packed == null:
		push_error("godot-mcp: cannot load the scene \"%s\"" % path)
		tree.quit(1)
		return
	tree.change_scene_to_packed.call_deferred(packed)
