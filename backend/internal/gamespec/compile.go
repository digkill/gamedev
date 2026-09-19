package gamespec

import (
	"encoding/json"
	"errors"
)

// Collision layers. Every entity the compiler emits gets one, so masks are
// predictable instead of being invented per object.
const (
	layerPlayer      = 1
	layerPlatform    = 2
	layerEnemy       = 4
	layerCollectible = 8
	layerHazard      = 16
	layerGoal        = 32
)

// Compiled is the finished project plus the identifiers the caller needs in
// order to store it.
type Compiled struct {
	ProjectID    string          `json:"project_id"`
	EntrySceneID string          `json:"entry_scene_id"`
	Title        string          `json:"title"`
	Mode         string          `json:"mode"`
	Document     json.RawMessage `json:"document"`
	Stats        Stats           `json:"stats"`
}

type Stats struct {
	Scenes       int `json:"scenes"`
	Entities     int `json:"entities"`
	Platforms    int `json:"platforms"`
	Hazards      int `json:"hazards"`
	Collectibles int `json:"collectibles"`
	Enemies      int `json:"enemies"`
	Goals        int `json:"goals"`
	UIWidgets    int `json:"ui_widgets"`
}

// NewID is injected so the compiler can be tested with a deterministic
// generator; in production it is projects.NewUUID.
type NewID func() (string, error)

type builder struct {
	spec  Spec
	newID NewID
	stats Stats
}

// Compile turns a normalised spec into a GameProject document. Every scene it
// writes has a camera, a player, a win condition, a lose condition, and a HUD,
// because those are emitted here rather than requested from the model.
func Compile(spec Spec, newID NewID) (Compiled, error) {
	return CompileWithID(spec, "", newID)
}

// CompileWithID keeps an existing project identity. A regenerated game has to
// stay the same project so its revision history survives.
func CompileWithID(spec Spec, projectID string, newID NewID) (Compiled, error) {
	if newID == nil {
		return Compiled{}, errors.New("gamespec: NewID is required")
	}
	spec = Normalize(spec)
	if len(spec.Levels) == 0 {
		return Compiled{}, errors.New("gamespec: no levels")
	}
	b := &builder{spec: spec, newID: newID}
	var err error
	if projectID == "" {
		if projectID, err = newID(); err != nil {
			return Compiled{}, err
		}
	}
	sceneIDs := make([]string, len(spec.Levels))
	for i := range spec.Levels {
		if sceneIDs[i], err = newID(); err != nil {
			return Compiled{}, err
		}
	}
	scenes := make([]any, 0, len(spec.Levels))
	for i, level := range spec.Levels {
		next := ""
		if i+1 < len(sceneIDs) {
			next = sceneIDs[i+1]
		}
		scene, err := b.scene(level, sceneIDs[i], next)
		if err != nil {
			return Compiled{}, err
		}
		scenes = append(scenes, scene)
	}
	b.stats.Scenes = len(scenes)
	document := map[string]any{
		"manifest": b.manifest(projectID, sceneIDs[0]),
		"scenes":   scenes,
		"prefabs":  []any{},
		"graphs":   []any{},
		"scripts":  []any{},
		"assets":   []any{},
	}
	raw, err := json.Marshal(document)
	if err != nil {
		return Compiled{}, err
	}
	return Compiled{
		ProjectID:    projectID,
		EntrySceneID: sceneIDs[0],
		Title:        spec.Title,
		Mode:         spec.Mode,
		Document:     raw,
		Stats:        b.stats,
	}, nil
}

func (b *builder) manifest(projectID, entrySceneID string) map[string]any {
	return map[string]any{
		"schema_version":      1,
		"project_id":          projectID,
		"title":               b.spec.Title,
		"mode":                b.spec.Mode,
		"entry_scene_id":      entrySceneID,
		"runtime_api_version": "1.0",
		"minimum_app_version": "1.0.0",
		"orientation":         b.spec.Orientation,
		"input_profile":       inputProfile(b.spec),
		"capabilities":        []string{"scene", "input", "ui", "physics", "local_save"},
		"logic":               map[string]any{"graph": false, "python_sdk_version": "1.0"},
		"limits_profile":      "mobile_standard_v1",
	}
}

func inputProfile(spec Spec) string {
	if spec.Mode == "3d" {
		if spec.Genre == "room" {
			return "touch_first_person"
		}
		return "touch_third_person"
	}
	switch spec.Genre {
	case "top_down", "puzzle", "arena":
		return "touch_top_down"
	default:
		return "touch_platformer"
	}
}

func (b *builder) scene(level Level, sceneID, nextSceneID string) (map[string]any, error) {
	dims := dimensions(b.spec.Mode)
	entities := make([]any, 0, 32)

	camera, err := b.camera(level)
	if err != nil {
		return nil, err
	}
	entities = append(entities, camera)

	if b.spec.Mode == "3d" {
		light, err := b.light(level)
		if err != nil {
			return nil, err
		}
		entities = append(entities, light)
	}

	player, err := b.player(level)
	if err != nil {
		return nil, err
	}
	entities = append(entities, player)

	for _, box := range level.Platforms {
		entity, err := b.solid(box)
		if err != nil {
			return nil, err
		}
		entities = append(entities, entity)
		b.stats.Platforms++
	}
	for _, box := range level.Decor {
		entity, err := b.decor(box)
		if err != nil {
			return nil, err
		}
		entities = append(entities, entity)
	}
	for _, box := range level.Hazards {
		entity, err := b.hazard(box)
		if err != nil {
			return nil, err
		}
		entities = append(entities, entity)
		b.stats.Hazards++
	}
	for _, pickup := range level.Collectibles {
		entity, err := b.collectible(pickup, level.Win == "collect_all")
		if err != nil {
			return nil, err
		}
		entities = append(entities, entity)
		b.stats.Collectibles++
	}
	for _, enemy := range level.Enemies {
		created, err := b.enemy(enemy, level)
		if err != nil {
			return nil, err
		}
		entities = append(entities, created...)
		b.stats.Enemies++
	}
	if level.Goal != nil {
		entity, err := b.goal(*level.Goal, level, nextSceneID)
		if err != nil {
			return nil, err
		}
		entities = append(entities, entity)
		b.stats.Goals++
	}
	hud, err := b.hud(level)
	if err != nil {
		return nil, err
	}
	entities = append(entities, hud...)

	b.stats.Entities += len(entities)
	return map[string]any{
		"id":       sceneID,
		"name":     level.Name,
		"mode":     b.spec.Mode,
		"rules":    b.rules(level, nextSceneID, dims),
		"entities": entities,
	}, nil
}

func (b *builder) rules(level Level, nextSceneID string, dims int) map[string]any {
	gravity := defaultGravity(b.spec)
	if level.Gravity != nil {
		gravity = *level.Gravity
	}
	win := map[string]any{"condition": level.Win}
	if level.Win == "survive_time" {
		win["seconds"] = level.Seconds
	}
	lose := map[string]any{"condition": level.Lose}
	if level.Lose == "timeout" {
		lose["seconds"] = level.Seconds
	}
	area := levelExtent(level, dims)
	if gravity < 0 {
		// The kill line sits below everything that was placed, so a fall into
		// empty space ends the attempt instead of dropping forever.
		lose["fall_below_y"] = round(area.min[1]-12, 3)
	}
	onWin := "show_result"
	rules := map[string]any{
		"gravity": round(gravity, 3),
		"win":     win,
		"lose":    lose,
		"on_lose": "restart",
		"bounds": map[string]any{
			"min": expand(area.min, -10, dims),
			"max": expand(area.max, 10, dims),
		},
	}
	if nextSceneID != "" {
		onWin = "next_scene"
		rules["next_scene_id"] = nextSceneID
	}
	rules["on_win"] = onWin
	return rules
}

func expand(values []float64, delta float64, dims int) []float64 {
	out := make([]float64, dims)
	for i := 0; i < dims; i++ {
		value := 0.0
		if i < len(values) {
			value = values[i]
		}
		out[i] = round(value+delta, 3)
	}
	return out
}

func (b *builder) camera(level Level) (map[string]any, error) {
	if b.spec.Mode == "3d" {
		position := []float64{level.Spawn[0], level.Spawn[1] + 5, level.Spawn[2] + 9}
		return b.entity("Камера", []any{
			transform3D(position, []float64{-20, 0, 0}, ones(3)),
			map[string]any{
				"type": "Camera", "projection": "perspective",
				"near": 0.1, "far": 2000, "fov_degrees": 65, "active": true,
			},
			map[string]any{
				"type": "CameraFollow", "target_tag": "player",
				"smoothing": 0.15, "offset": []float64{0, 5, 9},
			},
		})
	}
	return b.entity("Камера", []any{
		transform2D([]float64{level.Spawn[0], level.Spawn[1] + 1}, 0, ones(2)),
		map[string]any{
			"type": "Camera", "projection": "orthographic",
			"near": 0.1, "far": 1000, "size": 14, "active": true,
		},
		map[string]any{
			"type": "CameraFollow", "target_tag": "player",
			"smoothing": 0.15, "offset": []float64{0, 1}, "dead_zone": []float64{1.5, 1},
		},
	})
}

func (b *builder) light(level Level) (map[string]any, error) {
	return b.entity("Солнце", []any{
		transform3D([]float64{level.Spawn[0], level.Spawn[1] + 12, level.Spawn[2]}, []float64{-55, -35, 0}, ones(3)),
		map[string]any{"type": "Light", "kind": "directional", "color": "#FFF6E5FF", "intensity": 1.3},
	})
}

func (b *builder) player(level Level) (map[string]any, error) {
	size := b.spec.Player.Size
	gravityScale := 1.0
	if flatGravity(b.spec.Genre) {
		gravityScale = 0
	}
	components := []any{
		b.transform(level.Spawn, size),
		b.visual(b.spec.Palette.Player, playerShape(b.spec.Mode), 10),
		collider(colliderShape(playerShape(b.spec.Mode), b.spec.Mode), b.spec.Mode, false,
			layerPlayer, layerPlatform|layerEnemy|layerCollectible|layerHazard|layerGoal),
		map[string]any{
			"type": "Body", "kind": "kinematic", "mass": 1,
			"gravity_scale": gravityScale, "friction": 0.2, "restitution": 0,
		},
		map[string]any{
			"type": "CharacterController", "style": controllerStyle(b.spec),
			"move_speed": round(b.spec.Player.MoveSpeed, 3), "jump_speed": round(b.spec.Player.JumpSpeed, 3),
			"acceleration": 45, "air_control": 0.65, "max_slope_degrees": 46,
			"double_jump": b.spec.Player.DoubleJump,
		},
		map[string]any{
			"type": "Health", "maximum": b.spec.Player.Health,
			"initial": b.spec.Player.Health, "invulnerable_seconds": 1,
		},
		tag("player"),
	}
	return b.entity(b.spec.Player.Name, components)
}

func controllerStyle(spec Spec) string {
	if spec.Mode == "3d" {
		if spec.Genre == "room" {
			return "first_person"
		}
		if flatGravity(spec.Genre) {
			return "third_person"
		}
		return "platformer"
	}
	if flatGravity(spec.Genre) {
		return "top_down"
	}
	return "platformer"
}

func playerShape(mode string) string {
	if mode == "3d" {
		return "capsule"
	}
	return "rectangle"
}

func (b *builder) solid(box Box) (map[string]any, error) {
	return b.entity(box.Name, []any{
		b.transform(box.Position, box.Size),
		b.visual(box.Color, box.Shape, 0),
		collider(colliderShape(box.Shape, b.spec.Mode), b.spec.Mode, false, layerPlatform, layerPlayer|layerEnemy),
		map[string]any{
			"type": "Body", "kind": "static", "mass": 1,
			"gravity_scale": 0, "friction": 0.6, "restitution": 0,
		},
		tag("platform"),
	})
}

func (b *builder) decor(box Box) (map[string]any, error) {
	return b.entity(box.Name, []any{
		b.transform(box.Position, box.Size),
		b.visual(box.Color, box.Shape, -5),
		tag("decor"),
	})
}

func (b *builder) hazard(box Box) (map[string]any, error) {
	amount := box.Damage
	if amount < 1 {
		amount = 1
	}
	return b.entity(box.Name, []any{
		b.transform(box.Position, box.Size),
		b.visual(box.Color, box.Shape, 1),
		collider(colliderShape(box.Shape, b.spec.Mode), b.spec.Mode, true, layerHazard, layerPlayer),
		map[string]any{
			"type": "Damage", "amount": amount, "mode": "trigger",
			"cooldown_seconds": 1, "target_tag": "player", "destroy_self": false,
		},
		tag("hazard"),
	})
}

func (b *builder) collectible(pickup Pickup, countsToGoal bool) (map[string]any, error) {
	shape := "circle"
	if b.spec.Mode == "3d" {
		shape = "sphere"
	}
	size := sizeFor(dimensions(b.spec.Mode), 0.6, 0.6)
	return b.entity(pickup.Name, []any{
		b.transform(pickup.Position, size),
		b.visual(b.spec.Palette.Collectible, shape, 5),
		collider(colliderShape(shape, b.spec.Mode), b.spec.Mode, true, layerCollectible, layerPlayer),
		map[string]any{
			"type": "Collectible", "kind": pickup.Kind, "value": pickup.Value,
			"respawn_seconds": 0, "counts_to_goal": countsToGoal,
		},
		tag("collectible"),
	})
}

func (b *builder) enemy(enemy Enemy, level Level) ([]any, error) {
	shape := playerShape(b.spec.Mode)
	components := []any{
		b.transform(enemy.Position, enemy.Size),
		b.visual(enemy.Color, shape, 8),
		collider(colliderShape(shape, b.spec.Mode), b.spec.Mode, false, layerEnemy, layerPlayer|layerPlatform),
		map[string]any{
			"type": "Body", "kind": "kinematic", "mass": 1,
			"gravity_scale": 0, "friction": 0.2, "restitution": 0,
		},
		map[string]any{
			"type": "Damage", "amount": enemy.Damage, "mode": "contact",
			"cooldown_seconds": 1, "target_tag": "player", "destroy_self": false,
		},
		tag("enemy"),
	}
	if enemy.Health > 0 {
		components = append(components, map[string]any{
			"type": "Health", "maximum": enemy.Health, "initial": enemy.Health, "invulnerable_seconds": 0,
		})
	}
	switch enemy.Behavior {
	case "patrol":
		points := make([]any, 0, len(enemy.Patrol))
		for _, point := range enemy.Patrol {
			points = append(points, point)
		}
		components = append(components, map[string]any{
			"type": "Patrol", "points": points, "speed": round(enemy.Speed, 3), "loop": "ping_pong",
		})
	case "chase":
		components = append(components, map[string]any{
			"type": "Chase", "target_tag": "player", "speed": round(enemy.Speed, 3),
			"activation_range": 12, "stop_range": 0.5,
		})
	case "spawned":
		// The template is inert scenery until the spawner copies it.
		template, err := b.disabledEntity(enemy.Name+" (шаблон)", components)
		if err != nil {
			return nil, err
		}
		spawner, err := b.entity(enemy.Name+" — появление", []any{
			b.transform(enemy.Position, ones(dimensions(b.spec.Mode))),
			map[string]any{
				"type": "Spawner", "template_entity_id": template["id"],
				"interval_seconds": round(enemy.IntervalSeconds, 3),
				"max_alive":        minInt(enemy.Count, 12), "total": enemy.Count, "radius": 3,
			},
		})
		if err != nil {
			return nil, err
		}
		return []any{template, spawner}, nil
	}
	entity, err := b.entity(enemy.Name, components)
	if err != nil {
		return nil, err
	}
	return []any{entity}, nil
}

func (b *builder) goal(box Box, level Level, nextSceneID string) (map[string]any, error) {
	requires := "none"
	if level.Win == "collect_all" && len(level.Collectibles) > 0 {
		requires = "all_collectibles"
	}
	goal := map[string]any{"type": "Goal", "requires": requires, "outcome": "win"}
	if nextSceneID != "" {
		goal["outcome"] = "next_scene"
		goal["next_scene_id"] = nextSceneID
	}
	return b.entity(box.Name, []any{
		b.transform(box.Position, box.Size),
		b.visual(box.Color, box.Shape, 2),
		collider(colliderShape(box.Shape, b.spec.Mode), b.spec.Mode, true, layerGoal, layerPlayer),
		goal,
		tag("goal"),
	})
}

// hud builds the on-screen readouts. Only widgets that mean something in this
// level are added: no score counter in a level with nothing to collect.
func (b *builder) hud(level Level) ([]any, error) {
	widgets := make([]any, 0, 5)
	add := func(name string, widget map[string]any) error {
		entity, err := b.entity(name, []any{b.transform(zeroes(dimensions(b.spec.Mode)), ones(dimensions(b.spec.Mode))), widget})
		if err != nil {
			return err
		}
		widgets = append(widgets, entity)
		b.stats.UIWidgets++
		return nil
	}
	if level.Lose == "health_zero" {
		if err := add("Полоса здоровья", map[string]any{
			"type": "UIWidget", "widget": "health_bar", "anchor": "top_left",
			"offset": []float64{16, 16}, "size": []float64{200, 20},
			"binding": "health", "action": "none", "color": b.spec.Palette.UI,
		}); err != nil {
			return nil, err
		}
	}
	if len(level.Collectibles) > 0 {
		binding := "score"
		if level.Win == "collect_all" {
			binding = "collectibles"
		}
		if err := add("Счёт", map[string]any{
			"type": "UIWidget", "widget": "score", "anchor": "top_right",
			"offset": []float64{-16, 16}, "size": []float64{160, 24},
			"binding": binding, "action": "none", "color": b.spec.Palette.UI,
		}); err != nil {
			return nil, err
		}
	}
	if level.Seconds > 0 {
		if err := add("Таймер", map[string]any{
			"type": "UIWidget", "widget": "timer", "anchor": "top_center",
			"offset": []float64{0, 16}, "size": []float64{120, 24},
			"binding": "timer", "action": "none", "color": b.spec.Palette.UI,
		}); err != nil {
			return nil, err
		}
	}
	hint := level.Hint
	if hint == "" {
		hint = level.Name
	}
	if err := add("Подсказка", map[string]any{
		"type": "UIWidget", "widget": "label", "anchor": "bottom_center",
		"offset": []float64{0, -24}, "size": []float64{460, 26},
		"text": hint, "binding": "none", "action": "none", "color": b.spec.Palette.UI,
	}); err != nil {
		return nil, err
	}
	if err := add("Заново", map[string]any{
		"type": "UIWidget", "widget": "button", "anchor": "bottom_right",
		"offset": []float64{-16, -16}, "size": []float64{128, 44},
		"text": "Заново", "binding": "none", "action": "restart", "color": b.spec.Palette.UI,
	}); err != nil {
		return nil, err
	}
	return widgets, nil
}

func (b *builder) entity(name string, components []any) (map[string]any, error) {
	id, err := b.newID()
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"id": id, "name": clampText(name, 80, "Объект"),
		"parent_id": nil, "components": components,
	}, nil
}

func (b *builder) disabledEntity(name string, components []any) (map[string]any, error) {
	entity, err := b.entity(name, components)
	if err != nil {
		return nil, err
	}
	entity["enabled"] = false
	return entity, nil
}

func (b *builder) transform(position, size []float64) map[string]any {
	dims := dimensions(b.spec.Mode)
	position = clampVector(position, dims, -100000, 100000, zeroes(dims))
	scale := clampVector(size, dims, 0.001, 1000, ones(dims))
	for i := range position {
		position[i] = round(position[i], 3)
		scale[i] = round(scale[i], 3)
	}
	if b.spec.Mode == "3d" {
		return transform3D(position, zeroes(3), scale)
	}
	return transform2D(position, 0, scale)
}

func transform2D(position []float64, rotation float64, scale []float64) map[string]any {
	return map[string]any{
		"type": "Transform", "space": "2d",
		"position": position, "rotation": rotation, "scale": scale,
	}
}

func transform3D(position, rotation, scale []float64) map[string]any {
	return map[string]any{
		"type": "Transform", "space": "3d",
		"position": position, "rotation": rotation, "scale": scale,
	}
}

func (b *builder) visual(color, shape string, layer int) map[string]any {
	if b.spec.Mode == "3d" {
		return map[string]any{"type": "Mesh", "primitive": shape, "material_color": color}
	}
	return map[string]any{"type": "Sprite", "shape": shape, "tint": color, "layer": layer}
}

// collider sizes are in the entity's local space: the visual primitive is one
// unit across and Transform.scale gives it its world size, so a unit collider
// always matches the shape the player sees.
func collider(shape, mode string, trigger bool, layer, mask int) map[string]any {
	size := []float64{1, 1}
	if mode == "3d" {
		size = []float64{1, 1, 1}
	}
	return map[string]any{
		"type": "Collider", "shape": shape, "size": size,
		"is_trigger": trigger, "layer": layer, "mask": mask,
	}
}

func tag(values ...string) map[string]any {
	return map[string]any{"type": "Tag", "values": values}
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
