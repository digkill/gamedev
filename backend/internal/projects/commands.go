package projects

import (
	"encoding/json"
)

// Properties an editor or an agent may set one at a time. Anything outside the
// list must go through SetComponent, which revalidates the whole component.
var propertyAllowlist = map[string]bool{
	"Transform.position":               true,
	"Transform.rotation":               true,
	"Transform.scale":                  true,
	"Sprite.tint":                      true,
	"Sprite.layer":                     true,
	"Mesh.material_color":              true,
	"Camera.active":                    true,
	"Camera.fov_degrees":               true,
	"Camera.size":                      true,
	"Light.color":                      true,
	"Light.intensity":                  true,
	"Collider.size":                    true,
	"Collider.is_trigger":              true,
	"Body.kind":                        true,
	"Body.mass":                        true,
	"Body.gravity_scale":               true,
	"Body.friction":                    true,
	"Body.restitution":                 true,
	"CharacterController.move_speed":   true,
	"CharacterController.jump_speed":   true,
	"CharacterController.acceleration": true,
	"CharacterController.air_control":  true,
	"CharacterController.double_jump":  true,
	"Health.maximum":                   true,
	"Health.initial":                   true,
	"Health.invulnerable_seconds":      true,
	"Damage.amount":                    true,
	"Damage.cooldown_seconds":          true,
	"Collectible.value":                true,
	"Collectible.respawn_seconds":      true,
	"Patrol.speed":                     true,
	"Patrol.points":                    true,
	"Chase.speed":                      true,
	"Chase.activation_range":           true,
	"Spawner.interval_seconds":         true,
	"Spawner.max_alive":                true,
	"Spawner.total":                    true,
	"Timer.duration_seconds":           true,
	"UIWidget.text":                    true,
	"UIWidget.color":                   true,
	"CameraFollow.smoothing":           true,
	"CameraFollow.offset":              true,
}

func validateScene(raw json.RawMessage) error {
	var scene projectScene
	if len(raw) == 0 || len(raw) > 2<<20 || strictJSON(raw, &scene) != nil || !IsUUID(scene.ID) || !shortName(scene.Name, 80) || (scene.Mode != "2d" && scene.Mode != "3d") || scene.Entities == nil || len(scene.Entities) > 2000 {
		return ErrInvalid
	}
	return validateEntities(scene)
}

func validateCommand(op Command) error {
	if !IsUUID(op.OperationID) {
		return ErrInvalid
	}
	switch op.Type {
	case "CreateScene":
		if !exactly(op, "scene") {
			return ErrInvalid
		}
		return validateScene(op.Scene)
	case "SetSceneRules":
		if !exactly(op, "scene_id", "rules") || !IsUUID(op.SceneID) {
			return ErrInvalid
		}
		var rules sceneRules
		if strictJSON(op.Rules, &rules) != nil {
			return ErrInvalid
		}
		return nil
	case "CreateEntity":
		if !exactly(op, "scene_id", "entity") || !IsUUID(op.SceneID) {
			return ErrInvalid
		}
		var entity projectEntity
		if strictJSON(op.Entity, &entity) != nil {
			return ErrInvalid
		}
		return nil
	case "DeleteEntity":
		if !exactly(op, "scene_id", "entity_id") || !IsUUID(op.SceneID) || !IsUUID(op.EntityID) {
			return ErrInvalid
		}
		return nil
	case "SetComponent":
		if !exactly(op, "scene_id", "entity_id", "component") || !IsUUID(op.SceneID) || !IsUUID(op.EntityID) {
			return ErrInvalid
		}
		var header struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(op.Component, &header) != nil || header.Type == "" {
			return ErrInvalid
		}
		return nil
	case "SetProperty":
		if !exactly(op, "scene_id", "entity_id", "property", "value") || !IsUUID(op.SceneID) || !IsUUID(op.EntityID) || !propertyAllowlist[op.Property] {
			return ErrInvalid
		}
		return nil
	case "SetProjectSetting":
		if !exactly(op, "value", "setting") {
			return ErrInvalid
		}
		switch op.Setting {
		case "title":
			var title string
			if json.Unmarshal(op.Value, &title) != nil || !shortName(title, 120) {
				return ErrInvalid
			}
		case "orientation":
			var value string
			if json.Unmarshal(op.Value, &value) != nil || (value != "portrait" && value != "landscape" && value != "auto") {
				return ErrInvalid
			}
		case "input_profile":
			var value string
			if json.Unmarshal(op.Value, &value) != nil {
				return ErrInvalid
			}
			switch value {
			case "touch_platformer", "touch_top_down", "touch_first_person", "touch_third_person", "touch_generic":
			default:
				return ErrInvalid
			}
		default:
			return ErrInvalid
		}
		return nil
	default:
		return ErrInvalid
	}
}

// exactly reports whether the command carries the listed payload fields and
// nothing else. Commands are rejected rather than silently trimmed, so a
// mistaken field cannot be mistaken for an accepted instruction.
func exactly(op Command, names ...string) bool {
	present := map[string]bool{
		"scene":     len(op.Scene) > 0,
		"scene_id":  op.SceneID != "",
		"entity":    len(op.Entity) > 0,
		"entity_id": op.EntityID != "",
		"component": len(op.Component) > 0,
		"property":  op.Property != "",
		"value":     len(op.Value) > 0,
		"setting":   op.Setting != "",
		"rules":     len(op.Rules) > 0,
	}
	want := make(map[string]bool, len(names))
	for _, name := range names {
		want[name] = true
	}
	for name, has := range present {
		if has != want[name] {
			return false
		}
	}
	return true
}

func ApplyCommands(current json.RawMessage, projectMode string, ops []Command) (json.RawMessage, error) {
	if len(ops) < 1 || len(ops) > 100 {
		return nil, ErrInvalid
	}
	doc, err := validateProject(current)
	if err != nil {
		return nil, err
	}
	if doc.Manifest.Mode != projectMode {
		return nil, ErrInvalid
	}
	seen := make(map[string]bool, len(ops))
	for _, op := range ops {
		if seen[op.OperationID] || validateCommand(op) != nil {
			return nil, ErrInvalid
		}
		seen[op.OperationID] = true
		if err := applyOne(&doc, op); err != nil {
			return nil, err
		}
	}
	result, err := json.Marshal(doc)
	if err != nil {
		return nil, err
	}
	if _, err := validateProject(result); err != nil {
		return nil, err
	}
	return result, nil
}

func applyOne(doc *projectDocument, op Command) error {
	switch op.Type {
	case "CreateScene":
		return applyCreateScene(doc, op)
	case "SetSceneRules":
		return applySetSceneRules(doc, op)
	case "CreateEntity":
		return applyCreateEntity(doc, op)
	case "DeleteEntity":
		return applyDeleteEntity(doc, op)
	case "SetComponent":
		return applySetComponent(doc, op)
	case "SetProperty":
		return applySetProperty(doc, op)
	case "SetProjectSetting":
		return applySetProjectSetting(doc, op)
	default:
		return ErrInvalid
	}
}

func applyCreateScene(doc *projectDocument, op Command) error {
	if len(doc.Scenes) >= 20 {
		return ErrInvalid
	}
	var scene projectScene
	if strictJSON(op.Scene, &scene) != nil || scene.Mode != doc.Manifest.Mode {
		return ErrInvalid
	}
	for _, existing := range doc.Scenes {
		if existing.ID == scene.ID {
			return ErrInvalid
		}
	}
	if err := validateEntities(scene); err != nil {
		return err
	}
	doc.Scenes = append(doc.Scenes, scene)
	return nil
}

func applySetSceneRules(doc *projectDocument, op Command) error {
	scene, err := sceneByID(doc, op.SceneID)
	if err != nil {
		return err
	}
	var rules sceneRules
	if strictJSON(op.Rules, &rules) != nil {
		return ErrInvalid
	}
	if err := validateSceneRules(&rules, scene.Mode); err != nil {
		return err
	}
	scene.Rules = &rules
	return nil
}

func applyCreateEntity(doc *projectDocument, op Command) error {
	scene, err := sceneByID(doc, op.SceneID)
	if err != nil {
		return err
	}
	if len(scene.Entities) >= 2000 {
		return ErrInvalid
	}
	var entity projectEntity
	if strictJSON(op.Entity, &entity) != nil {
		return ErrInvalid
	}
	for _, existing := range scene.Entities {
		if existing.ID == entity.ID {
			return ErrInvalid
		}
	}
	scene.Entities = append(scene.Entities, entity)
	return validateEntities(*scene)
}

func applyDeleteEntity(doc *projectDocument, op Command) error {
	scene, err := sceneByID(doc, op.SceneID)
	if err != nil {
		return err
	}
	index := -1
	for i, entity := range scene.Entities {
		if entity.ID == op.EntityID {
			index = i
		}
		if entity.ParentID != nil && *entity.ParentID == op.EntityID {
			return ErrInvalid
		}
	}
	if index < 0 {
		return ErrInvalid
	}
	scene.Entities = append(scene.Entities[:index], scene.Entities[index+1:]...)
	return validateEntities(*scene)
}

func applySetComponent(doc *projectDocument, op Command) error {
	entity, scene, err := entityByID(doc, op.SceneID, op.EntityID)
	if err != nil {
		return err
	}
	var header struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(op.Component, &header) != nil || header.Type == "" {
		return ErrInvalid
	}
	replaced := false
	for i, raw := range entity.Components {
		var existing struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(raw, &existing) != nil {
			return ErrInvalid
		}
		if existing.Type == header.Type {
			entity.Components[i] = append(json.RawMessage(nil), op.Component...)
			replaced = true
			break
		}
	}
	if !replaced {
		if len(entity.Components) >= 32 {
			return ErrInvalid
		}
		entity.Components = append(entity.Components, append(json.RawMessage(nil), op.Component...))
	}
	return validateEntities(*scene)
}

func applySetProperty(doc *projectDocument, op Command) error {
	entity, scene, err := entityByID(doc, op.SceneID, op.EntityID)
	if err != nil {
		return err
	}
	typeName, field, ok := splitProperty(op.Property)
	if !ok {
		return ErrInvalid
	}
	var next any
	if json.Unmarshal(op.Value, &next) != nil {
		return ErrInvalid
	}
	updated := false
	for i, raw := range entity.Components {
		var obj map[string]any
		if json.Unmarshal(raw, &obj) != nil || obj["type"] != typeName {
			continue
		}
		obj[field] = next
		encoded, err := json.Marshal(obj)
		if err != nil {
			return ErrInvalid
		}
		entity.Components[i] = encoded
		updated = true
		break
	}
	if !updated {
		return ErrInvalid
	}
	return validateEntities(*scene)
}

func applySetProjectSetting(doc *projectDocument, op Command) error {
	switch op.Setting {
	case "title":
		var title string
		if json.Unmarshal(op.Value, &title) != nil || !shortName(title, 120) {
			return ErrInvalid
		}
		doc.Manifest.Title = title
	case "orientation":
		var value string
		if json.Unmarshal(op.Value, &value) != nil {
			return ErrInvalid
		}
		doc.Manifest.Orientation = value
	case "input_profile":
		var value string
		if json.Unmarshal(op.Value, &value) != nil {
			return ErrInvalid
		}
		doc.Manifest.InputProfile = value
	default:
		return ErrInvalid
	}
	return nil
}

func sceneByID(doc *projectDocument, id string) (*projectScene, error) {
	for i := range doc.Scenes {
		if doc.Scenes[i].ID == id {
			return &doc.Scenes[i], nil
		}
	}
	return nil, ErrInvalid
}

func entityByID(doc *projectDocument, sceneID, entityID string) (*projectEntity, *projectScene, error) {
	scene, err := sceneByID(doc, sceneID)
	if err != nil {
		return nil, nil, err
	}
	for i := range scene.Entities {
		if scene.Entities[i].ID == entityID {
			return &scene.Entities[i], scene, nil
		}
	}
	return nil, nil, ErrInvalid
}

func splitProperty(property string) (string, string, bool) {
	for i := 0; i < len(property); i++ {
		if property[i] == '.' {
			if i == 0 || i == len(property)-1 {
				return "", "", false
			}
			return property[:i], property[i+1:], true
		}
	}
	return "", "", false
}
