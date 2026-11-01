extends Node2D

## Physics frames this scene has processed. The end-to-end tests read it to
## check that game_time pauses and steps exactly.
var ticks := 0


func _ready() -> void:
	print("Demo started: %d children" % get_child_count())
	$StartButton.pressed.connect(func() -> void: print("Start pressed"))


func _physics_process(_delta: float) -> void:
	ticks += 1


func _unhandled_key_input(event: InputEvent) -> void:
	var key := event as InputEventKey
	if key != null and key.pressed and not key.echo:
		print("Key: %s" % key.as_text_keycode())
