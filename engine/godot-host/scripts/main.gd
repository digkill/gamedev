extends Control

# Trusted stage-0 host. Never loads scripts, .tscn, arbitrary URLs, or model paths
# from a user project. Full project validation remains a separate required gate.

const SAMPLE_2D := "res://demo/empty_2d.project.json"
const SAMPLE_3D := "res://demo/empty_3d.project.json"
const MAX_PROJECT_BYTES := 2097152
const MAX_ENTITIES := 2000

var _viewport: SubViewport
var _world: Node
var _status: Label
var _mode := "2d"
var _playing := false


func _ready() -> void:
	_build_ui()
	var host := Engine.get_singleton("SandboxHost") if Engine.has_singleton("SandboxHost") else null
	var start_mode := "2d"
	if host != null:
		if host.has_signal("select_sample"):
			host.select_sample.connect(_on_select_sample)
		if host.has_signal("set_playing"):
			host.set_playing.connect(_on_set_playing)
		if host.has_signal("load_project"):
			host.load_project.connect(_on_load_project)
		if host.has_method("getSelectedMode"):
			start_mode = str(host.call("getSelectedMode"))
		if host.has_method("isPlaying"):
			_playing = bool(host.call("isPlaying"))
		if host.has_method("getPendingProject"):
			var pending := str(host.call("getPendingProject"))
			if pending != "":
				_on_load_project(pending)
				return
	_load_sample(start_mode)


func _build_ui() -> void:
	var root := VBoxContainer.new()
	root.set_anchors_and_offsets_preset(Control.PRESET_FULL_RECT)
	add_child(root)

	var toolbar := HBoxContainer.new()
	toolbar.visible = not Engine.has_singleton("SandboxHost")
	root.add_child(toolbar)
	var choose_2d := Button.new()
	choose_2d.text = "2D"
	choose_2d.pressed.connect(func() -> void: _load_sample("2d"))
	toolbar.add_child(choose_2d)
	var choose_3d := Button.new()
	choose_3d.text = "3D"
	choose_3d.pressed.connect(func() -> void: _load_sample("3d"))
	toolbar.add_child(choose_3d)
	var play := Button.new()
	play.text = "Play"
	play.pressed.connect(func() -> void: _on_set_playing(true))
	toolbar.add_child(play)
	var stop := Button.new()
	stop.text = "Stop"
	stop.pressed.connect(func() -> void: _on_set_playing(false))
	toolbar.add_child(stop)
	_status = Label.new()
	_status.text = "Loading"
	toolbar.add_child(_status)

	var container := SubViewportContainer.new()
	container.stretch = true
	container.size_flags_horizontal = Control.SIZE_EXPAND_FILL
	container.size_flags_vertical = Control.SIZE_EXPAND_FILL
	root.add_child(container)
	_viewport = SubViewport.new()
	_viewport.size = Vector2i(1280, 720)
	_viewport.disable_3d = false
	_viewport.render_target_update_mode = SubViewport.UPDATE_ALWAYS
	container.add_child(_viewport)


func _on_select_sample(mode: String) -> void:
	_load_sample(mode)


func _on_load_project(text: String) -> void:
	if text.length() > MAX_PROJECT_BYTES:
		_status.text = "Project too large"
		return
	var data: Variant = JSON.parse_string(text)
	if typeof(data) != TYPE_DICTIONARY:
		_status.text = "Invalid project JSON"
		return
	var manifest: Variant = data.get("manifest")
	if typeof(manifest) != TYPE_DICTIONARY:
		_status.text = "Invalid project JSON"
		return
	var mode := str(manifest.get("mode"))
	if not _present_world(data, mode):
		_status.text = "Invalid project data"


func _on_set_playing(playing: bool) -> void:
	_playing = playing
	if _world != null:
		_world.process_mode = Node.PROCESS_MODE_INHERIT if playing else Node.PROCESS_MODE_DISABLED
	_status.text = ("Playing " if playing else "Editing ") + _mode


func _load_sample(mode: String) -> void:
	if mode != "2d" and mode != "3d":
		_status.text = "Unsupported mode"
		return
	var path := SAMPLE_2D if mode == "2d" else SAMPLE_3D
	var file := FileAccess.open(path, FileAccess.READ)
	if file == null or file.get_length() > MAX_PROJECT_BYTES:
		_status.text = "Sample unavailable or too large"
		return
	var data: Variant = JSON.parse_string(file.get_as_text())
	if typeof(data) != TYPE_DICTIONARY:
		_status.text = "Invalid project JSON"
		return
	if not _present_world(data, mode):
		_status.text = "Invalid project data"


func _present_world(data: Variant, mode: String) -> bool:
	if typeof(data) != TYPE_DICTIONARY:
		return false
	var built := _build_world(data, mode)
	if built == null:
		return false
	if _world != null:
		_viewport.remove_child(_world)
		_world.queue_free()
	_world = built
	_viewport.add_child(_world)
	_mode = mode
	_on_set_playing(_playing)
	return true


func _build_world(project: Dictionary, mode: String) -> Node:
	var manifest: Variant = project.get("manifest")
	var scenes: Variant = project.get("scenes")
	if typeof(manifest) != TYPE_DICTIONARY or typeof(scenes) != TYPE_ARRAY:
		return null
	if manifest.get("schema_version") != 1 or manifest.get("mode") != mode:
		return null
	var entry_id: Variant = manifest.get("entry_scene_id")
	var selected: Dictionary = {}
	for scene in scenes:
		if typeof(scene) == TYPE_DICTIONARY and scene.get("id") == entry_id:
			selected = scene
			break
	if selected.is_empty() or selected.get("mode") != mode:
		return null
	var entities: Variant = selected.get("entities")
	if typeof(entities) != TYPE_ARRAY or entities.size() > MAX_ENTITIES:
		return null

	var world: Node = Node2D.new() if mode == "2d" else Node3D.new()
	world.name = "ProjectScene"
	var nodes := {}
	var parent_ids := {}
	for entity in entities:
		if typeof(entity) != TYPE_DICTIONARY:
			_free_staged_nodes(nodes)
			world.free()
			return null
		var id: Variant = entity.get("id")
		var components: Variant = entity.get("components")
		if typeof(id) != TYPE_STRING or nodes.has(id) or typeof(components) != TYPE_ARRAY:
			_free_staged_nodes(nodes)
			world.free()
			return null
		var node: Node = Node2D.new() if mode == "2d" else Node3D.new()
		node.name = str(entity.get("name", "Entity"))
		for component in components:
			if typeof(component) != TYPE_DICTIONARY or not _apply_component(node, component, mode):
				node.free()
				_free_staged_nodes(nodes)
				world.free()
				return null
		nodes[id] = node
		parent_ids[id] = entity.get("parent_id")

	# Catch missing parents and cycles before attaching anything to the tree.
	for id in nodes:
		var seen := {id: true}
		var cursor: Variant = parent_ids[id]
		while cursor != null:
			if typeof(cursor) != TYPE_STRING or not nodes.has(cursor) or seen.has(cursor):
				_free_staged_nodes(nodes)
				world.free()
				return null
			seen[cursor] = true
			cursor = parent_ids[cursor]

	for entity in entities:
		var node: Node = nodes[entity["id"]]
		var parent_id: Variant = entity.get("parent_id")
		if parent_id == null:
			world.add_child(node)
		else:
			nodes[parent_id].add_child(node)

	if mode == "2d":
		if world.find_children("*", "Camera2D", true, false).is_empty():
			var camera := Camera2D.new()
			camera.enabled = true
			world.add_child(camera)
	else:
		if world.find_children("*", "Camera3D", true, false).is_empty():
			var camera := Camera3D.new()
			camera.position = Vector3(0.0, 4.0, 8.0)
			world.add_child(camera)
			camera.look_at(Vector3.ZERO)
			camera.current = true
		if world.find_children("*", "Light3D", true, false).is_empty():
			var light := DirectionalLight3D.new()
			light.rotation_degrees = Vector3(-45.0, -35.0, 0.0)
			world.add_child(light)
	return world


func _free_staged_nodes(nodes: Dictionary) -> void:
	for node in nodes.values():
		if is_instance_valid(node) and node.get_parent() == null:
			node.free()


func _apply_component(node: Node, component: Dictionary, mode: String) -> bool:
	match component.get("type"):
		"Transform":
			return _apply_transform(node, component, mode)
		"Sprite":
			return mode == "2d" and _apply_sprite(node, component)
		"Mesh":
			return mode == "3d" and _apply_mesh(node, component)
		"Camera":
			return _apply_camera(node, component, mode)
		"Light":
			return mode == "3d" and _apply_light(node, component)
		_:
			return false


func _apply_transform(node: Node, component: Dictionary, mode: String) -> bool:
	var position: Variant = component.get("position")
	var scale_value: Variant = component.get("scale")
	if typeof(position) != TYPE_ARRAY or typeof(scale_value) != TYPE_ARRAY:
		return false
	if mode == "2d" and component.get("space") == "2d" and position.size() == 2 and scale_value.size() == 2:
		if not _finite_numbers(position) or not _finite_numbers(scale_value):
			return false
		var two_d := node as Node2D
		two_d.position = Vector2(position[0], position[1])
		two_d.rotation_degrees = float(component.get("rotation", 0.0))
		two_d.scale = Vector2(scale_value[0], scale_value[1])
		return true
	var rotation: Variant = component.get("rotation")
	if mode == "3d" and component.get("space") == "3d" and position.size() == 3 and scale_value.size() == 3 and typeof(rotation) == TYPE_ARRAY and rotation.size() == 3:
		if not _finite_numbers(position) or not _finite_numbers(scale_value) or not _finite_numbers(rotation):
			return false
		var three_d := node as Node3D
		three_d.position = Vector3(position[0], position[1], position[2])
		three_d.rotation_degrees = Vector3(rotation[0], rotation[1], rotation[2])
		three_d.scale = Vector3(scale_value[0], scale_value[1], scale_value[2])
		return true
	return false


func _apply_sprite(node: Node, component: Dictionary) -> bool:
	var shape: Variant = component.get("shape")
	if shape != "rectangle" and shape != "circle":
		return false
	var polygon := Polygon2D.new()
	var points := PackedVector2Array()
	if shape == "rectangle":
		points = PackedVector2Array([Vector2(-32, -32), Vector2(32, -32), Vector2(32, 32), Vector2(-32, 32)])
	else:
		for i in range(32):
			var angle := TAU * float(i) / 32.0
			points.append(Vector2(cos(angle), sin(angle)) * 32.0)
	polygon.polygon = points
	polygon.color = Color.from_string(str(component.get("tint", "#FFFFFFFF")), Color.WHITE)
	polygon.z_index = int(component.get("layer", 0))
	node.add_child(polygon)
	return true


func _apply_mesh(node: Node, component: Dictionary) -> bool:
	var visual := MeshInstance3D.new()
	match component.get("primitive"):
		"box": visual.mesh = BoxMesh.new()
		"sphere": visual.mesh = SphereMesh.new()
		"capsule": visual.mesh = CapsuleMesh.new()
		"plane": visual.mesh = PlaneMesh.new()
		_:
			visual.free()
			return false
	var material := StandardMaterial3D.new()
	material.albedo_color = Color.from_string(str(component.get("material_color", "#FFFFFFFF")), Color.WHITE)
	visual.material_override = material
	node.add_child(visual)
	return true


func _apply_camera(node: Node, component: Dictionary, mode: String) -> bool:
	if mode == "2d":
		if component.get("projection") != "orthographic":
			return false
		var camera := Camera2D.new()
		camera.enabled = bool(component.get("active", true))
		node.add_child(camera)
		return true
	var projection: Variant = component.get("projection")
	if projection != "perspective" and projection != "orthographic":
		return false
	var camera := Camera3D.new()
	camera.near = float(component.get("near", 0.1))
	camera.far = float(component.get("far", 1000.0))
	if camera.near <= 0 or camera.far <= camera.near:
		camera.free()
		return false
	if projection == "orthographic":
		camera.projection = Camera3D.PROJECTION_ORTHOGONAL
		camera.size = float(component.get("size", 10.0))
	else:
		camera.projection = Camera3D.PROJECTION_PERSPECTIVE
		camera.fov = float(component.get("fov_degrees", 70.0))
	camera.current = bool(component.get("active", true))
	node.add_child(camera)
	return true


func _apply_light(node: Node, component: Dictionary) -> bool:
	var light: Light3D
	match component.get("kind"):
		"directional": light = DirectionalLight3D.new()
		"point": light = OmniLight3D.new()
		"spot": light = SpotLight3D.new()
		_: return false
	light.light_color = Color.from_string(str(component.get("color", "#FFFFFFFF")), Color.WHITE)
	light.light_energy = float(component.get("intensity", 1.0))
	if light is OmniLight3D:
		light.omni_range = float(component.get("range", 10.0))
	elif light is SpotLight3D:
		light.spot_range = float(component.get("range", 10.0))
		light.spot_angle = float(component.get("spot_angle_degrees", 45.0))
	node.add_child(light)
	return true


func _finite_numbers(values: Array) -> bool:
	for value in values:
		if typeof(value) != TYPE_FLOAT and typeof(value) != TYPE_INT:
			return false
		if not is_finite(float(value)):
			return false
	return true
