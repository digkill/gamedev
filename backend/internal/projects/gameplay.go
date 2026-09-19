package projects

import (
	"encoding/json"
	"strings"
)

// Gameplay v1: the typed layer that turns a scene of primitives into a game
// that can be won and lost. Everything here is a closed enumeration with
// bounded numbers. There is no scripting and no free-form expression, so a
// document that validates describes behaviour the trusted runtime can execute
// without sandboxing user code.

// tags are a closed vocabulary. Components reference each other by tag rather
// than by entity id wherever the target is a role instead of one object.
var validTags = map[string]bool{
	"player":      true,
	"enemy":       true,
	"collectible": true,
	"goal":        true,
	"hazard":      true,
	"platform":    true,
	"wall":        true,
	"checkpoint":  true,
	"decor":       true,
	"projectile":  true,
}

type sceneRules struct {
	// Gravity is the downward acceleration in scene units. It is a pointer so
	// that "not specified" keeps the runtime default rather than meaning zero.
	Gravity     *float64 `json:"gravity,omitempty"`
	Win         winRule  `json:"win"`
	Lose        loseRule `json:"lose"`
	OnWin       string   `json:"on_win"`
	OnLose      string   `json:"on_lose"`
	NextSceneID *string  `json:"next_scene_id,omitempty"`
	Bounds      *bounds  `json:"bounds,omitempty"`
}

type winRule struct {
	Condition string  `json:"condition"`
	Seconds   float64 `json:"seconds,omitempty"`
}

type loseRule struct {
	Condition  string   `json:"condition"`
	FallBelowY *float64 `json:"fall_below_y,omitempty"`
	Seconds    float64  `json:"seconds,omitempty"`
}

type bounds struct {
	Min []float64 `json:"min"`
	Max []float64 `json:"max"`
}

func validateSceneRules(rules *sceneRules, mode string) error {
	if rules == nil {
		return nil
	}
	if rules.Gravity != nil && !finiteRange(*rules.Gravity, -200, 200) {
		return ErrInvalid
	}
	switch rules.Win.Condition {
	case "reach_goal", "collect_all", "defeat_all":
		if rules.Win.Seconds != 0 {
			return ErrInvalid
		}
	case "survive_time":
		if !finiteRange(rules.Win.Seconds, 1, 36000) {
			return ErrInvalid
		}
	default:
		return ErrInvalid
	}
	switch rules.Lose.Condition {
	case "health_zero", "fall_out", "none":
		if rules.Lose.Seconds != 0 {
			return ErrInvalid
		}
	case "timeout":
		if !finiteRange(rules.Lose.Seconds, 1, 36000) {
			return ErrInvalid
		}
	default:
		return ErrInvalid
	}
	if rules.Lose.Condition == "fall_out" && rules.Lose.FallBelowY == nil {
		return ErrInvalid
	}
	if rules.Lose.FallBelowY != nil && !finiteRange(*rules.Lose.FallBelowY, -100000, 100000) {
		return ErrInvalid
	}
	switch rules.OnWin {
	case "next_scene", "show_result", "restart":
	default:
		return ErrInvalid
	}
	if rules.OnWin == "next_scene" && rules.NextSceneID == nil {
		return ErrInvalid
	}
	switch rules.OnLose {
	case "restart", "show_result":
	default:
		return ErrInvalid
	}
	if rules.NextSceneID != nil && !IsUUID(*rules.NextSceneID) {
		return ErrInvalid
	}
	if rules.Bounds != nil {
		n := 2
		if mode == "3d" {
			n = 3
		}
		if !validVector(rules.Bounds.Min, n, -100000, 100000) || !validVector(rules.Bounds.Max, n, -100000, 100000) {
			return ErrInvalid
		}
		for i := range rules.Bounds.Min {
			if rules.Bounds.Min[i] >= rules.Bounds.Max[i] {
				return ErrInvalid
			}
		}
	}
	return nil
}

// validateGameplayComponent handles every component type outside the original
// render-only subset. An unknown type still fails closed.
func validateGameplayComponent(raw json.RawMessage, kind, mode string) error {
	switch kind {
	case "Collider":
		var c struct {
			Type      string    `json:"type"`
			Shape     string    `json:"shape"`
			Size      []float64 `json:"size"`
			IsTrigger bool      `json:"is_trigger"`
			Layer     int64     `json:"layer"`
			Mask      int64     `json:"mask"`
		}
		if strictJSON(raw, &c) != nil || c.Layer < 1 || c.Layer > 0xFFFFFFFF || c.Mask < 0 || c.Mask > 0xFFFFFFFF {
			return ErrInvalid
		}
		n := 2
		if mode == "3d" {
			n = 3
		}
		if !validVector(c.Size, n, 0.001, 100000) {
			return ErrInvalid
		}
		if mode == "2d" {
			if c.Shape != "rectangle" && c.Shape != "circle" {
				return ErrInvalid
			}
		} else if c.Shape != "box" && c.Shape != "sphere" && c.Shape != "capsule" {
			return ErrInvalid
		}
	case "Body":
		var c struct {
			Type         string  `json:"type"`
			Kind         string  `json:"kind"`
			Mass         float64 `json:"mass"`
			GravityScale float64 `json:"gravity_scale"`
			Friction     float64 `json:"friction"`
			Restitution  float64 `json:"restitution"`
		}
		if strictJSON(raw, &c) != nil {
			return ErrInvalid
		}
		switch c.Kind {
		case "static", "dynamic", "kinematic":
		default:
			return ErrInvalid
		}
		if !finiteRange(c.Mass, 0.001, 100000) || !finiteRange(c.GravityScale, -10, 10) ||
			!finiteRange(c.Friction, 0, 1) || !finiteRange(c.Restitution, 0, 1) {
			return ErrInvalid
		}
	case "CharacterController":
		var c struct {
			Type            string  `json:"type"`
			Style           string  `json:"style"`
			MoveSpeed       float64 `json:"move_speed"`
			JumpSpeed       float64 `json:"jump_speed"`
			Acceleration    float64 `json:"acceleration"`
			AirControl      float64 `json:"air_control"`
			MaxSlopeDegrees float64 `json:"max_slope_degrees"`
			DoubleJump      bool    `json:"double_jump"`
		}
		if strictJSON(raw, &c) != nil {
			return ErrInvalid
		}
		switch c.Style {
		case "platformer":
			// A 3D obstacle course runs the same side-on controller, so this
			// style is allowed in both modes.
		case "top_down":
			if mode != "2d" {
				return ErrInvalid
			}
		case "first_person", "third_person":
			if mode != "3d" {
				return ErrInvalid
			}
		default:
			return ErrInvalid
		}
		if !finiteRange(c.MoveSpeed, 0.1, 1000) || !finiteRange(c.JumpSpeed, 0, 1000) ||
			!finiteRange(c.Acceleration, 0, 10000) || !finiteRange(c.AirControl, 0, 1) ||
			!finiteRange(c.MaxSlopeDegrees, 0, 89) {
			return ErrInvalid
		}
	case "Health":
		var c struct {
			Type                string  `json:"type"`
			Maximum             int     `json:"maximum"`
			Initial             int     `json:"initial"`
			InvulnerableSeconds float64 `json:"invulnerable_seconds"`
		}
		if strictJSON(raw, &c) != nil || c.Maximum < 1 || c.Maximum > 1000000 ||
			c.Initial < 0 || c.Initial > c.Maximum || !finiteRange(c.InvulnerableSeconds, 0, 60) {
			return ErrInvalid
		}
	case "Damage":
		var c struct {
			Type            string  `json:"type"`
			Amount          int     `json:"amount"`
			Mode            string  `json:"mode"`
			CooldownSeconds float64 `json:"cooldown_seconds"`
			TargetTag       string  `json:"target_tag"`
			DestroySelf     bool    `json:"destroy_self"`
		}
		if strictJSON(raw, &c) != nil || c.Amount < 1 || c.Amount > 1000000 ||
			!finiteRange(c.CooldownSeconds, 0, 60) || !validTags[c.TargetTag] {
			return ErrInvalid
		}
		if c.Mode != "contact" && c.Mode != "trigger" {
			return ErrInvalid
		}
	case "Collectible":
		var c struct {
			Type           string  `json:"type"`
			Kind           string  `json:"kind"`
			Value          int     `json:"value"`
			RespawnSeconds float64 `json:"respawn_seconds"`
			CountsToGoal   bool    `json:"counts_to_goal"`
		}
		if strictJSON(raw, &c) != nil || c.Value < 1 || c.Value > 1000000 || !finiteRange(c.RespawnSeconds, 0, 3600) {
			return ErrInvalid
		}
		switch c.Kind {
		case "score", "health", "key":
		default:
			return ErrInvalid
		}
	case "Goal":
		var c struct {
			Type        string  `json:"type"`
			Requires    string  `json:"requires"`
			Outcome     string  `json:"outcome"`
			NextSceneID *string `json:"next_scene_id,omitempty"`
		}
		if strictJSON(raw, &c) != nil {
			return ErrInvalid
		}
		switch c.Requires {
		case "none", "all_collectibles", "key":
		default:
			return ErrInvalid
		}
		switch c.Outcome {
		case "win":
			if c.NextSceneID != nil {
				return ErrInvalid
			}
		case "next_scene":
			if c.NextSceneID == nil || !IsUUID(*c.NextSceneID) {
				return ErrInvalid
			}
		default:
			return ErrInvalid
		}
	case "Patrol":
		var c struct {
			Type   string      `json:"type"`
			Points [][]float64 `json:"points"`
			Speed  float64     `json:"speed"`
			Loop   string      `json:"loop"`
		}
		if strictJSON(raw, &c) != nil || len(c.Points) < 2 || len(c.Points) > 32 || !finiteRange(c.Speed, 0.01, 1000) {
			return ErrInvalid
		}
		if c.Loop != "ping_pong" && c.Loop != "cycle" {
			return ErrInvalid
		}
		n := 2
		if mode == "3d" {
			n = 3
		}
		for _, point := range c.Points {
			if !validVector(point, n, -100000, 100000) {
				return ErrInvalid
			}
		}
	case "Chase":
		var c struct {
			Type            string  `json:"type"`
			TargetTag       string  `json:"target_tag"`
			Speed           float64 `json:"speed"`
			ActivationRange float64 `json:"activation_range"`
			StopRange       float64 `json:"stop_range"`
		}
		if strictJSON(raw, &c) != nil || !validTags[c.TargetTag] ||
			!finiteRange(c.Speed, 0.01, 1000) || !finiteRange(c.ActivationRange, 0.1, 100000) ||
			!finiteRange(c.StopRange, 0, c.ActivationRange) {
			return ErrInvalid
		}
	case "Spawner":
		var c struct {
			Type             string  `json:"type"`
			TemplateEntityID string  `json:"template_entity_id"`
			IntervalSeconds  float64 `json:"interval_seconds"`
			MaxAlive         int     `json:"max_alive"`
			Total            int     `json:"total"`
			Radius           float64 `json:"radius"`
		}
		if strictJSON(raw, &c) != nil || !IsUUID(c.TemplateEntityID) ||
			!finiteRange(c.IntervalSeconds, 0.1, 600) || c.MaxAlive < 1 || c.MaxAlive > 200 ||
			c.Total < 0 || c.Total > 10000 || !finiteRange(c.Radius, 0, 1000) {
			return ErrInvalid
		}
	case "Timer":
		var c struct {
			Type            string  `json:"type"`
			DurationSeconds float64 `json:"duration_seconds"`
			Repeat          bool    `json:"repeat"`
			Autostart       bool    `json:"autostart"`
		}
		if strictJSON(raw, &c) != nil || !finiteRange(c.DurationSeconds, 0.05, 36000) {
			return ErrInvalid
		}
	case "Tag":
		var c struct {
			Type   string   `json:"type"`
			Values []string `json:"values"`
		}
		if strictJSON(raw, &c) != nil || len(c.Values) < 1 || len(c.Values) > 8 {
			return ErrInvalid
		}
		seen := make(map[string]bool, len(c.Values))
		for _, value := range c.Values {
			if !validTags[value] || seen[value] {
				return ErrInvalid
			}
			seen[value] = true
		}
	case "CameraFollow":
		var c struct {
			Type      string    `json:"type"`
			TargetTag string    `json:"target_tag"`
			Smoothing float64   `json:"smoothing"`
			Offset    []float64 `json:"offset"`
			DeadZone  []float64 `json:"dead_zone,omitempty"`
		}
		if strictJSON(raw, &c) != nil || !validTags[c.TargetTag] || !finiteRange(c.Smoothing, 0, 1) {
			return ErrInvalid
		}
		n := 2
		if mode == "3d" {
			n = 3
		}
		if !validVector(c.Offset, n, -100000, 100000) {
			return ErrInvalid
		}
		if c.DeadZone != nil && !validVector(c.DeadZone, 2, 0, 100000) {
			return ErrInvalid
		}
	case "UIWidget":
		var c struct {
			Type    string    `json:"type"`
			Widget  string    `json:"widget"`
			Anchor  string    `json:"anchor"`
			Offset  []float64 `json:"offset"`
			Size    []float64 `json:"size"`
			Text    string    `json:"text,omitempty"`
			Binding string    `json:"binding"`
			Action  string    `json:"action"`
			Color   string    `json:"color"`
		}
		if strictJSON(raw, &c) != nil || !validVector(c.Offset, 2, -10000, 10000) ||
			!validVector(c.Size, 2, 1, 10000) || !validColor(c.Color) {
			return ErrInvalid
		}
		switch c.Widget {
		case "label", "score", "health_bar", "timer", "button", "panel":
		default:
			return ErrInvalid
		}
		switch c.Anchor {
		case "top_left", "top_center", "top_right", "center", "bottom_left", "bottom_center", "bottom_right":
		default:
			return ErrInvalid
		}
		switch c.Binding {
		case "none", "score", "health", "timer", "collectibles", "keys":
		default:
			return ErrInvalid
		}
		switch c.Action {
		case "none", "restart", "next_scene", "pause", "resume":
		default:
			return ErrInvalid
		}
		if c.Widget == "button" && c.Action == "none" {
			return ErrInvalid
		}
		if len([]rune(c.Text)) > 120 || strings.ContainsAny(c.Text, "\r\n") {
			return ErrInvalid
		}
	default:
		return ErrInvalid
	}
	return nil
}

// validateReferences checks the links a single component cannot check on its
// own: scene transitions and spawner templates.
func validateReferences(doc projectDocument, sceneIDs map[string]bool) error {
	for _, scene := range doc.Scenes {
		if err := validateSceneRules(scene.Rules, scene.Mode); err != nil {
			return err
		}
		if scene.Rules != nil && scene.Rules.NextSceneID != nil && !sceneIDs[*scene.Rules.NextSceneID] {
			return ErrInvalid
		}
		entityIDs := make(map[string]bool, len(scene.Entities))
		for _, entity := range scene.Entities {
			entityIDs[entity.ID] = true
		}
		for _, entity := range scene.Entities {
			for _, raw := range entity.Components {
				var header struct {
					Type             string  `json:"type"`
					NextSceneID      *string `json:"next_scene_id"`
					TemplateEntityID string  `json:"template_entity_id"`
				}
				if json.Unmarshal(raw, &header) != nil {
					return ErrInvalid
				}
				switch header.Type {
				case "Goal":
					if header.NextSceneID != nil && !sceneIDs[*header.NextSceneID] {
						return ErrInvalid
					}
				case "Spawner":
					// The template must live in the same scene: a spawner may
					// not reach into another level's objects.
					if !entityIDs[header.TemplateEntityID] || header.TemplateEntityID == entity.ID {
						return ErrInvalid
					}
				}
			}
		}
	}
	return nil
}
