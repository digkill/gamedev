package ai

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/digkill/gamedev/backend/internal/projects"
)

func sanitizeOps(mode string, manifest json.RawMessage, ops []projects.Command) []projects.Command {
	sceneID, known := sceneState(manifest)
	if sceneID == "" || len(ops) == 0 {
		return nil
	}
	out := make([]projects.Command, 0, len(ops))
	for _, raw := range ops {
		op := projects.NormalizeCommand(raw)
		switch op.Type {
		case "CreateEntity":
			entity, id, ok := sanitizeEntity(mode, known, op.Entity)
			if !ok {
				continue
			}
			known[id] = true
			out = append(out, clearUnused(projects.Command{
				OperationID: ensureUUID(op.OperationID),
				Type:        "CreateEntity",
				SceneID:     sceneID,
				Entity:      entity,
			}))
		case "DeleteEntity":
			entityID := strings.TrimSpace(op.EntityID)
			if !known[entityID] {
				continue
			}
			out = append(out, clearUnused(projects.Command{
				OperationID: ensureUUID(op.OperationID),
				Type:        "DeleteEntity",
				SceneID:     sceneID,
				EntityID:    entityID,
			}))
		case "SetComponent":
			entityID := strings.TrimSpace(op.EntityID)
			component, ok := sanitizeComponent(mode, op.Component)
			if !ok || !known[entityID] {
				continue
			}
			out = append(out, clearUnused(projects.Command{
				OperationID: ensureUUID(op.OperationID),
				Type:        "SetComponent",
				SceneID:     sceneID,
				EntityID:    entityID,
				Component:   component,
			}))
		case "SetProperty":
			entityID := strings.TrimSpace(op.EntityID)
			if !known[entityID] || !allowedProperty(op.Property) {
				continue
			}
			value := sanitizeValue(op.Property, op.Value)
			if len(value) == 0 {
				continue
			}
			out = append(out, clearUnused(projects.Command{
				OperationID: ensureUUID(op.OperationID),
				Type:        "SetProperty",
				SceneID:     sceneID,
				EntityID:    entityID,
				Property:    op.Property,
				Value:       value,
			}))
		case "SetProjectSetting":
			if op.Setting != "title" && op.Setting != "orientation" && op.Setting != "input_profile" {
				continue
			}
			if len(op.Value) == 0 {
				continue
			}
			out = append(out, clearUnused(projects.Command{
				OperationID: ensureUUID(op.OperationID),
				Type:        "SetProjectSetting",
				Setting:     op.Setting,
				Value:       op.Value,
			}))
		default:
			continue
		}
	}
	return out
}

func fallbackOps(mode, sceneID, prompt string) []projects.Command {
	if sceneID == "" {
		return nil
	}
	tint := "#4DA3FFFF"
	if strings.Contains(strings.ToLower(prompt), "платформ") {
		tint = "#8B5A2FFF"
	}
	ops := make([]projects.Command, 0, 4)
	if mode == "2d" {
		ops = append(ops,
			createPrimitive(sceneID, "Hero", []float64{0, 1}, []float64{1, 1.4}, "#FFDD55FF", "rectangle"),
			createPrimitive(sceneID, "Ground", []float64{0, -2}, []float64{8, 1}, tint, "rectangle"),
			createPrimitive(sceneID, "Block", []float64{3, 0}, []float64{2, 0.6}, "#6D8B74FF", "rectangle"),
		)
	} else {
		ops = append(ops,
			createMesh(sceneID, "Hero", []float64{0, 1, 0}, []float64{1, 1.6, 1}, "#FFDD55FF", "capsule"),
			createMesh(sceneID, "Ground", []float64{0, -1, 0}, []float64{8, 0.4, 8}, tint, "box"),
			createMesh(sceneID, "Block", []float64{2, 0.5, 0}, []float64{2, 1, 2}, "#6D8B74FF", "box"),
		)
	}
	return ops
}

func createPrimitive(sceneID, name string, position, scale []float64, tint, shape string) projects.Command {
	entity, _, _ := sanitizeEntity("2d", map[string]bool{}, mustJSON(map[string]any{
		"name":      name,
		"parent_id": nil,
		"components": []any{
			map[string]any{"type": "Transform", "space": "2d", "position": position, "rotation": 0, "scale": scale},
			map[string]any{"type": "Sprite", "shape": shape, "tint": tint, "layer": 0},
		},
	}))
	return clearUnused(projects.Command{
		OperationID: ensureUUID(""),
		Type:        "CreateEntity",
		SceneID:     sceneID,
		Entity:      entity,
	})
}

func createMesh(sceneID, name string, position, scale []float64, color, primitive string) projects.Command {
	entity, _, _ := sanitizeEntity("3d", map[string]bool{}, mustJSON(map[string]any{
		"name":      name,
		"parent_id": nil,
		"components": []any{
			map[string]any{"type": "Transform", "space": "3d", "position": position, "rotation": []float64{0, 0, 0}, "scale": scale},
			map[string]any{"type": "Mesh", "primitive": primitive, "material_color": color},
		},
	}))
	return clearUnused(projects.Command{
		OperationID: ensureUUID(""),
		Type:        "CreateEntity",
		SceneID:     sceneID,
		Entity:      entity,
	})
}

func sanitizeEntity(mode string, known map[string]bool, raw json.RawMessage) (json.RawMessage, string, bool) {
	var loose map[string]any
	if json.Unmarshal(raw, &loose) != nil {
		loose = map[string]any{}
	}
	id := fmt.Sprint(loose["id"])
	if !projects.IsUUID(id) {
		id = ensureUUID("")
	}
	name := strings.TrimSpace(fmt.Sprint(loose["name"]))
	if name == "" || len([]rune(name)) > 80 {
		name = "Object"
	}
	var parent *string
	if value, ok := loose["parent_id"].(string); ok && known[value] {
		parent = &value
	}
	components, _ := loose["components"].([]any)
	sanitized := make([]json.RawMessage, 0, 4)
	hasTransform := false
	for _, item := range components {
		encoded, err := json.Marshal(item)
		if err != nil {
			continue
		}
		component, ok := sanitizeComponent(mode, encoded)
		if !ok {
			continue
		}
		var header struct {
			Type string `json:"type"`
		}
		_ = json.Unmarshal(component, &header)
		if header.Type == "Transform" {
			hasTransform = true
		}
		sanitized = append(sanitized, component)
	}
	if !hasTransform {
		sanitized = append([]json.RawMessage{defaultTransform(mode)}, sanitized...)
	}
	if len(sanitized) < 1 {
		return nil, "", false
	}
	entity := map[string]any{"id": id, "name": name, "parent_id": parent, "components": sanitized}
	encoded, err := json.Marshal(entity)
	if err != nil {
		return nil, "", false
	}
	return encoded, id, true
}

func sanitizeComponent(mode string, raw json.RawMessage) (json.RawMessage, bool) {
	var header struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(raw, &header) != nil {
		return nil, false
	}
	var loose map[string]any
	if json.Unmarshal(raw, &loose) != nil {
		return nil, false
	}
	switch header.Type {
	case "Transform":
		n := 2
		if mode == "3d" {
			n = 3
		}
		obj := map[string]any{
			"type":     "Transform",
			"space":    mode,
			"position": vector(loose["position"], n, 0),
			"scale":    vector(loose["scale"], n, 1),
		}
		if mode == "2d" {
			obj["rotation"] = number(loose["rotation"], 0)
		} else {
			obj["rotation"] = vector(loose["rotation"], 3, 0)
		}
		return mustJSON(obj), true
	case "Sprite":
		if mode != "2d" {
			return nil, false
		}
		shape := fmt.Sprint(loose["shape"])
		if shape != "circle" {
			shape = "rectangle"
		}
		return mustJSON(map[string]any{
			"type":  "Sprite",
			"shape": shape,
			"tint":  color(loose["tint"], "#FFFFFFFF"),
			"layer": int(number(loose["layer"], 0)),
		}), true
	case "Mesh":
		if mode != "3d" {
			return nil, false
		}
		primitive := fmt.Sprint(loose["primitive"])
		switch primitive {
		case "sphere", "capsule", "plane":
		default:
			primitive = "box"
		}
		return mustJSON(map[string]any{
			"type":           "Mesh",
			"primitive":      primitive,
			"material_color": color(loose["material_color"], "#FFFFFFFF"),
		}), true
	case "Camera":
		projection := "orthographic"
		if mode == "3d" && fmt.Sprint(loose["projection"]) == "perspective" {
			projection = "perspective"
		}
		active := true
		if v, ok := loose["active"].(bool); ok {
			active = v
		}
		return mustJSON(map[string]any{
			"type":        "Camera",
			"projection":  projection,
			"near":        positive(loose["near"], 0.1),
			"far":         positive(loose["far"], 1000),
			"fov_degrees": positive(loose["fov_degrees"], 70),
			"size":        positive(loose["size"], 10),
			"active":      active,
		}), true
	case "Light":
		if mode != "3d" {
			return nil, false
		}
		kind := fmt.Sprint(loose["kind"])
		switch kind {
		case "point", "spot":
		default:
			kind = "directional"
		}
		return mustJSON(map[string]any{
			"type":               "Light",
			"kind":               kind,
			"color":              color(loose["color"], "#FFFFFFFF"),
			"intensity":          positive(loose["intensity"], 1),
			"range":              positive(loose["range"], 20),
			"spot_angle_degrees": positive(loose["spot_angle_degrees"], 45),
		}), true
	default:
		return nil, false
	}
}

func sanitizeValue(property string, raw json.RawMessage) json.RawMessage {
	if strings.HasSuffix(property, "tint") || strings.HasSuffix(property, "color") || strings.HasSuffix(property, "material_color") {
		var s string
		if json.Unmarshal(raw, &s) == nil {
			return mustJSON(color(s, "#FFFFFFFF"))
		}
	}
	return raw
}

func sceneState(manifest json.RawMessage) (string, map[string]bool) {
	var doc struct {
		Manifest struct {
			EntrySceneID string `json:"entry_scene_id"`
		} `json:"manifest"`
		Scenes []struct {
			ID       string `json:"id"`
			Entities []struct {
				ID string `json:"id"`
			} `json:"entities"`
		} `json:"scenes"`
	}
	known := map[string]bool{}
	if json.Unmarshal(manifest, &doc) != nil {
		return "", known
	}
	for _, scene := range doc.Scenes {
		if scene.ID != doc.Manifest.EntrySceneID {
			continue
		}
		for _, entity := range scene.Entities {
			known[entity.ID] = true
		}
		return scene.ID, known
	}
	return "", known
}

func clearUnused(op projects.Command) projects.Command {
	return projects.NormalizeCommand(op)
}

func ensureUUID(raw string) string {
	if projects.IsUUID(raw) {
		return raw
	}
	id, err := projects.NewUUID()
	if err != nil {
		return "00000000-0000-4000-8000-000000000001"
	}
	return id
}

func allowedProperty(property string) bool {
	switch property {
	case "Transform.position", "Transform.rotation", "Transform.scale", "Sprite.tint", "Sprite.layer", "Mesh.material_color", "Camera.active", "Camera.fov_degrees", "Camera.size", "Light.color", "Light.intensity":
		return true
	default:
		return false
	}
}

func vector(raw any, n int, fill float64) []float64 {
	out := make([]float64, n)
	for i := range out {
		out[i] = fill
	}
	switch value := raw.(type) {
	case []any:
		for i := 0; i < n && i < len(value); i++ {
			out[i] = number(value[i], fill)
		}
	case []float64:
		for i := 0; i < n && i < len(value); i++ {
			out[i] = value[i]
		}
	}
	return out
}

func number(raw any, fallback float64) float64 {
	switch value := raw.(type) {
	case float64:
		return value
	case int:
		return float64(value)
	case json.Number:
		n, err := value.Float64()
		if err != nil {
			return fallback
		}
		return n
	default:
		return fallback
	}
}

func positive(raw any, fallback float64) float64 {
	n := number(raw, fallback)
	if n <= 0 {
		return fallback
	}
	return n
}

func color(raw any, fallback string) string {
	s := strings.TrimSpace(fmt.Sprint(raw))
	s = strings.TrimPrefix(s, "#")
	switch len(s) {
	case 6:
		s += "FF"
	case 8:
	default:
		return fallback
	}
	for _, c := range s {
		if !strings.ContainsRune("0123456789abcdefABCDEF", c) {
			return fallback
		}
	}
	return "#" + strings.ToUpper(s)
}

func defaultTransform(mode string) json.RawMessage {
	if mode == "3d" {
		return mustJSON(map[string]any{
			"type": "Transform", "space": "3d", "position": []float64{0, 0, 0}, "rotation": []float64{0, 0, 0}, "scale": []float64{1, 1, 1},
		})
	}
	return mustJSON(map[string]any{
		"type": "Transform", "space": "2d", "position": []float64{0, 0}, "rotation": 0, "scale": []float64{1, 1},
	})
}

func mustJSON(v any) json.RawMessage {
	raw, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return raw
}

func applyError(mode string, manifest json.RawMessage, ops []projects.Command) string {
	_, err := projects.ApplyCommands(manifest, mode, ops)
	if err == nil {
		return ""
	}
	return err.Error()
}
