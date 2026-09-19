extends SceneTree

func _initialize() -> void:
	var scene: PackedScene = load("res://main.tscn")
	if scene == null:
		push_error("main.tscn failed to load")
		quit(1)
		return
	var host: Control = scene.instantiate()
	root.add_child.call_deferred(host)
	_run.call_deferred(host)


func _run(host: Control) -> void:
	if not is_instance_valid(host):
		quit(1)
		return
	for mode in ["2d", "3d"]:
		var path := "res://demo/empty_%s.project.json" % mode
		var data: Variant = JSON.parse_string(FileAccess.get_file_as_string(path))
		if typeof(data) != TYPE_DICTIONARY:
			push_error("Invalid fixture: " + path)
			quit(1)
			return
		var world: Node = host._build_world(data, mode)
		if world == null:
			push_error("Could not build " + mode)
			quit(1)
			return
		world.free()
		var bad: Dictionary = data.duplicate(true)
		bad["scenes"][0]["entities"][0]["components"].append({"type": "Script", "source": "pass"})
		if host._build_world(bad, mode) != null:
			push_error("Unsafe script component accepted")
			quit(1)
			return
	print("Godot stage-0 smoke OK: 2D, 3D, unknown component rejection")
	quit(0)
