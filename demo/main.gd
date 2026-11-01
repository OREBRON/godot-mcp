extends Node2D

## Physics frames this scene has processed. The end-to-end tests read it to
## check that game_time pauses and steps exactly.
var ticks := 0


func _ready() -> void:
	print("Demo started: %d children" % get_child_count())


func _physics_process(_delta: float) -> void:
	ticks += 1
