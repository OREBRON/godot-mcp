extends SceneTree
# Written into <project>/.godot/godot_mcp/ by the godot-mcp server and run as
#   godot --headless --path <project> --script <this file> -- <list file>
# Loads every script named in the list file. Godot prints parse errors on its
# own; the server reads them from the output, and this script only reports
# which files could be loaded at all.


func _initialize() -> void:
	var results := []
	var args := OS.get_cmdline_user_args()
	if args.size() > 0:
		var f := FileAccess.open(args[0], FileAccess.READ)
		if f != null:
			while not f.eof_reached():
				var path := f.get_line().strip_edges()
				if path.is_empty():
					continue
				var script = ResourceLoader.load(path, "", ResourceLoader.CACHE_MODE_IGNORE)
				results.append({"path": path, "loaded": script != null})
	print("GODOT_MCP_RESULT " + JSON.stringify(results))
	quit()
