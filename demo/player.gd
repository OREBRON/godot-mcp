class_name DemoPlayer
extends CharacterBody2D

signal died

@export var speed := 200.0
var health := 3


func _physics_process(_delta: float) -> void:
	var dir := Input.get_vector("ui_left", "ui_right", "ui_up", "ui_down")
	velocity = dir * speed
	move_and_slide()


func take_hit() -> void:
	health -= 1
	if health <= 0:
		died.emit()
