package projects

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"math"
	"strings"
)

// Project data is untrusted even if it arrives from a signed-in user. This
// first implementation accepts the strict, executable subset of GameProject v1
// supported by engine/godot-host. Other contract features fail closed until the
// corresponding trusted runtime and admission checks exist.
type projectDocument struct {
	Manifest projectManifest   `json:"manifest"`
	Scenes   []projectScene    `json:"scenes"`
	Prefabs  []json.RawMessage `json:"prefabs"`
	Graphs   []json.RawMessage `json:"graphs"`
	Scripts  []json.RawMessage `json:"scripts"`
	Assets   []json.RawMessage `json:"assets"`
}

type projectManifest struct {
	SchemaVersion     int      `json:"schema_version"`
	ProjectID         string   `json:"project_id"`
	Title             string   `json:"title"`
	Mode              string   `json:"mode"`
	EntrySceneID      string   `json:"entry_scene_id"`
	RuntimeAPIVersion string   `json:"runtime_api_version"`
	MinimumAppVersion string   `json:"minimum_app_version"`
	Orientation       string   `json:"orientation"`
	InputProfile      string   `json:"input_profile"`
	Capabilities      []string `json:"capabilities"`
	Logic             struct {
		Graph            bool   `json:"graph"`
		PythonSDKVersion string `json:"python_sdk_version"`
	} `json:"logic"`
	LimitsProfile string `json:"limits_profile"`
}

type projectScene struct {
	ID    string      `json:"id"`
	Name  string      `json:"name"`
	Mode  string      `json:"mode"`
	Rules *sceneRules `json:"rules,omitempty"`
	// Entities keeps its position last so a scene serialises with its metadata
	// first, which keeps stored documents readable.
	Entities []projectEntity `json:"entities"`
}

type projectEntity struct {
	ID       string  `json:"id"`
	Name     string  `json:"name"`
	ParentID *string `json:"parent_id"`
	// Enabled defaults to true when absent. A disabled entity is loaded but not
	// activated, which is how a Spawner template lives in the scene.
	Enabled    *bool             `json:"enabled,omitempty"`
	Components []json.RawMessage `json:"components"`
}

func validateProject(raw json.RawMessage) (projectDocument, error) {
	var doc projectDocument
	if len(raw) == 0 || len(raw) > 2<<20 || strictJSON(raw, &doc) != nil {
		return doc, ErrInvalid
	}
	m := doc.Manifest
	if m.SchemaVersion != 1 || !IsUUID(m.ProjectID) || !IsUUID(m.EntrySceneID) || !shortName(m.Title, 120) || (m.Mode != "2d" && m.Mode != "3d") || m.RuntimeAPIVersion != "1.0" || m.MinimumAppVersion != "1.0.0" || m.LimitsProfile != "mobile_standard_v1" || m.Logic.PythonSDKVersion != "1.0" || m.Logic.Graph {
		return doc, ErrInvalid
	}
	switch m.Orientation {
	case "portrait", "landscape", "auto":
	default:
		return doc, ErrInvalid
	}
	switch m.InputProfile {
	case "touch_platformer", "touch_top_down", "touch_first_person", "touch_third_person", "touch_generic":
	default:
		return doc, ErrInvalid
	}
	if len(m.Capabilities) == 0 || len(m.Capabilities) > 6 {
		return doc, ErrInvalid
	}
	caps := make(map[string]bool)
	for _, c := range m.Capabilities {
		switch c {
		case "scene", "input", "audio", "ui", "local_save", "physics":
		default:
			return doc, ErrInvalid
		}
		if caps[c] {
			return doc, ErrInvalid
		}
		caps[c] = true
	}
	if doc.Scenes == nil || len(doc.Scenes) < 1 || len(doc.Scenes) > 20 || doc.Prefabs == nil || doc.Graphs == nil || doc.Scripts == nil || doc.Assets == nil || len(doc.Prefabs) != 0 || len(doc.Graphs) != 0 || len(doc.Scripts) != 0 || len(doc.Assets) != 0 {
		return doc, ErrInvalid
	}
	sceneIDs := make(map[string]bool)
	for _, scene := range doc.Scenes {
		if !IsUUID(scene.ID) || sceneIDs[scene.ID] || !shortName(scene.Name, 80) || scene.Mode != m.Mode || scene.Entities == nil || len(scene.Entities) > 2000 {
			return doc, ErrInvalid
		}
		sceneIDs[scene.ID] = true
		if err := validateEntities(scene); err != nil {
			return doc, err
		}
	}
	if !sceneIDs[m.EntrySceneID] {
		return doc, ErrInvalid
	}
	// Scene rules and component references point at other scenes and entities,
	// so they can only be checked once every scene is known.
	if err := validateReferences(doc, sceneIDs); err != nil {
		return doc, err
	}
	return doc, nil
}

func validateEntities(scene projectScene) error {
	parents := make(map[string]*string)
	for _, entity := range scene.Entities {
		if !IsUUID(entity.ID) || !shortName(entity.Name, 80) || entity.Components == nil || len(entity.Components) < 1 || len(entity.Components) > 32 {
			return ErrInvalid
		}
		if _, exists := parents[entity.ID]; exists {
			return ErrInvalid
		}
		parents[entity.ID] = entity.ParentID
		seenTypes := make(map[string]bool)
		for _, raw := range entity.Components {
			var header struct {
				Type string `json:"type"`
			}
			if json.Unmarshal(raw, &header) != nil || seenTypes[header.Type] || validateComponent(raw, header.Type, scene.Mode) != nil {
				return ErrInvalid
			}
			seenTypes[header.Type] = true
		}
		if !seenTypes["Transform"] {
			return ErrInvalid
		}
	}
	for id := range parents {
		seen := map[string]bool{id: true}
		for cursor := parents[id]; cursor != nil; cursor = parents[*cursor] {
			if !IsUUID(*cursor) || seen[*cursor] {
				return ErrInvalid
			}
			if _, exists := parents[*cursor]; !exists {
				return ErrInvalid
			}
			seen[*cursor] = true
		}
	}
	return nil
}

func validateComponent(raw json.RawMessage, kind, mode string) error {
	switch kind {
	case "Transform":
		var c struct {
			Type     string          `json:"type"`
			Space    string          `json:"space"`
			Position []float64       `json:"position"`
			Rotation json.RawMessage `json:"rotation"`
			Scale    []float64       `json:"scale"`
		}
		if strictJSON(raw, &c) != nil || c.Space != mode {
			return ErrInvalid
		}
		n := 2
		if mode == "3d" {
			n = 3
		}
		if !validVector(c.Position, n, -100000, 100000) || !validVector(c.Scale, n, 0.001, 1000) {
			return ErrInvalid
		}
		if mode == "2d" {
			var r float64
			if json.Unmarshal(c.Rotation, &r) != nil || !finiteRange(r, -36000, 36000) {
				return ErrInvalid
			}
		} else {
			var r []float64
			if json.Unmarshal(c.Rotation, &r) != nil || !validVector(r, 3, -36000, 36000) {
				return ErrInvalid
			}
		}
	case "Sprite":
		if mode != "2d" {
			return ErrInvalid
		}
		var c struct {
			Type  string `json:"type"`
			Shape string `json:"shape"`
			Tint  string `json:"tint"`
			Layer int    `json:"layer"`
		}
		if strictJSON(raw, &c) != nil || (c.Shape != "rectangle" && c.Shape != "circle") || !validColor(c.Tint) || c.Layer < -4096 || c.Layer > 4096 {
			return ErrInvalid
		}
	case "Mesh":
		if mode != "3d" {
			return ErrInvalid
		}
		var c struct {
			Type          string `json:"type"`
			Primitive     string `json:"primitive"`
			MaterialColor string `json:"material_color"`
		}
		if strictJSON(raw, &c) != nil || (c.Primitive != "box" && c.Primitive != "sphere" && c.Primitive != "capsule" && c.Primitive != "plane") || (c.MaterialColor != "" && !validColor(c.MaterialColor)) {
			return ErrInvalid
		}
	case "Camera":
		var c struct {
			Type       string  `json:"type"`
			Projection string  `json:"projection"`
			Near       float64 `json:"near"`
			Far        float64 `json:"far"`
			FOV        float64 `json:"fov_degrees"`
			Size       float64 `json:"size"`
			Active     *bool   `json:"active"`
		}
		if strictJSON(raw, &c) != nil || !finiteRange(c.Near, 0.000001, 10000) || !finiteRange(c.Far, c.Near+0.000001, 100000) || (c.Projection != "orthographic" && c.Projection != "perspective") || (mode == "2d" && c.Projection != "orthographic") {
			return ErrInvalid
		}
		if (c.FOV != 0 && !finiteRange(c.FOV, 1, 179)) || (c.Size != 0 && !finiteRange(c.Size, 0.001, 100000)) {
			return ErrInvalid
		}
	case "Light":
		if mode != "3d" {
			return ErrInvalid
		}
		var c struct {
			Type      string  `json:"type"`
			Kind      string  `json:"kind"`
			Color     string  `json:"color"`
			Intensity float64 `json:"intensity"`
			Range     float64 `json:"range"`
			SpotAngle float64 `json:"spot_angle_degrees"`
		}
		if strictJSON(raw, &c) != nil || (c.Kind != "directional" && c.Kind != "point" && c.Kind != "spot") || !validColor(c.Color) || !finiteRange(c.Intensity, 0, 100000) || (c.Range != 0 && !finiteRange(c.Range, 0.001, 100000)) || (c.SpotAngle != 0 && !finiteRange(c.SpotAngle, 1, 179)) {
			return ErrInvalid
		}
	default:
		return validateGameplayComponent(raw, kind, mode)
	}
	return nil
}

func strictJSON(raw []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return ErrInvalid
	}
	return nil
}

func shortName(name string, max int) bool {
	return strings.TrimSpace(name) != "" && len([]rune(name)) <= max
}

func validVector(values []float64, n int, low, high float64) bool {
	if len(values) != n {
		return false
	}
	for _, v := range values {
		if !finiteRange(v, low, high) {
			return false
		}
	}
	return true
}

func finiteRange(v, low, high float64) bool {
	return !math.IsNaN(v) && !math.IsInf(v, 0) && v >= low && v <= high
}

func validColor(s string) bool {
	if len(s) != 9 || s[0] != '#' {
		return false
	}
	for _, c := range s[1:] {
		if !strings.ContainsRune("0123456789abcdefABCDEF", c) {
			return false
		}
	}
	return true
}
