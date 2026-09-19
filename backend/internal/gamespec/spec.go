// Package gamespec holds the intermediate game description the agent pipeline
// produces, and compiles it into a GameProject document.
//
// The split matters. A language model is good at deciding that a level needs a
// moving platform over a pit and bad at emitting hundreds of correlated UUIDs,
// component fields, and collision masks without a mistake. So the model writes
// this compact spec, and Go turns it into the strict document: every entity
// gets its collider, every level gets a camera, a win condition, a lose
// condition, and a HUD. A game that reaches Compile is complete by
// construction, not by luck.
package gamespec

import (
	"strings"
)

type Spec struct {
	Title       string  `json:"title"`
	Mode        string  `json:"mode"`
	Genre       string  `json:"genre"`
	Orientation string  `json:"orientation"`
	Summary     string  `json:"summary"`
	Palette     Palette `json:"palette"`
	Player      Player  `json:"player"`
	Levels      []Level `json:"levels"`
}

type Palette struct {
	Background  string `json:"background"`
	Player      string `json:"player"`
	Platform    string `json:"platform"`
	Hazard      string `json:"hazard"`
	Enemy       string `json:"enemy"`
	Collectible string `json:"collectible"`
	Goal        string `json:"goal"`
	Decor       string `json:"decor"`
	UI          string `json:"ui"`
}

type Player struct {
	Name       string    `json:"name"`
	Size       []float64 `json:"size"`
	MoveSpeed  float64   `json:"move_speed"`
	JumpSpeed  float64   `json:"jump_speed"`
	Health     int       `json:"health"`
	DoubleJump bool      `json:"double_jump"`
}

type Level struct {
	Name         string    `json:"name"`
	Hint         string    `json:"hint"`
	Gravity      *float64  `json:"gravity,omitempty"`
	Spawn        []float64 `json:"spawn"`
	Platforms    []Box     `json:"platforms"`
	Hazards      []Box     `json:"hazards"`
	Decor        []Box     `json:"decor"`
	Collectibles []Pickup  `json:"collectibles"`
	Enemies      []Enemy   `json:"enemies"`
	Goal         *Box      `json:"goal,omitempty"`
	Win          string    `json:"win"`
	Lose         string    `json:"lose"`
	Seconds      float64   `json:"seconds,omitempty"`
}

type Box struct {
	Name     string    `json:"name"`
	Position []float64 `json:"position"`
	Size     []float64 `json:"size"`
	Color    string    `json:"color,omitempty"`
	Shape    string    `json:"shape,omitempty"`
	Damage   int       `json:"damage,omitempty"`
}

type Pickup struct {
	Name     string    `json:"name"`
	Position []float64 `json:"position"`
	Kind     string    `json:"kind"`
	Value    int       `json:"value"`
}

type Enemy struct {
	Name     string      `json:"name"`
	Position []float64   `json:"position"`
	Size     []float64   `json:"size"`
	Color    string      `json:"color,omitempty"`
	Behavior string      `json:"behavior"`
	Patrol   [][]float64 `json:"patrol,omitempty"`
	Speed    float64     `json:"speed"`
	Damage   int         `json:"damage"`
	Health   int         `json:"health,omitempty"`
	// Count applies to the spawned behaviour: how many arrive in total.
	Count           int     `json:"count,omitempty"`
	IntervalSeconds float64 `json:"interval_seconds,omitempty"`
}

// Defect is one reason a spec is not yet a finished game. Codes are stable so
// the developer agent can be told precisely what to fix.
type Defect struct {
	Code    string `json:"code"`
	Level   int    `json:"level,omitempty"`
	Message string `json:"message"`
	Fatal   bool   `json:"fatal"`
}

const (
	MaxLevels       = 8
	maxPerCategory  = 120
	maxEntitiesHint = 400
)

var (
	genres = map[string]bool{
		"platformer": true, "top_down": true, "puzzle": true,
		"obstacle": true, "room": true, "arena": true,
	}
	winConditions  = map[string]bool{"reach_goal": true, "collect_all": true, "defeat_all": true, "survive_time": true}
	loseConditions = map[string]bool{"health_zero": true, "fall_out": true, "timeout": true, "none": true}
	behaviors      = map[string]bool{"patrol": true, "chase": true, "static": true, "spawned": true}
	pickupKinds    = map[string]bool{"score": true, "health": true, "key": true}
)

var defaultPalette = Palette{
	Background:  "#10131AFF",
	Player:      "#FFD75AFF",
	Platform:    "#4A7C59FF",
	Hazard:      "#D9453BFF",
	Enemy:       "#B5479BFF",
	Collectible: "#4DA3FFFF",
	Goal:        "#7BE495FF",
	Decor:       "#2E3440FF",
	UI:          "#F2F4F8FF",
}

// Normalize fills defaults and clamps every number into the range the project
// validator accepts. It never fails: whatever the model produced becomes a
// well-formed spec, and Validate then reports what is still missing.
func Normalize(spec Spec) Spec {
	spec.Mode = strings.ToLower(strings.TrimSpace(spec.Mode))
	if spec.Mode != "3d" {
		spec.Mode = "2d"
	}
	dims := dimensions(spec.Mode)
	spec.Genre = strings.ToLower(strings.TrimSpace(spec.Genre))
	if !genres[spec.Genre] {
		if spec.Mode == "3d" {
			spec.Genre = "obstacle"
		} else {
			spec.Genre = "platformer"
		}
	}
	spec.Title = clampText(spec.Title, 120, "Новая игра")
	spec.Summary = clampText(spec.Summary, 600, "")
	switch spec.Orientation {
	case "portrait", "landscape", "auto":
	default:
		spec.Orientation = "landscape"
	}
	spec.Palette = normalizePalette(spec.Palette)

	spec.Player.Name = clampText(spec.Player.Name, 80, "Игрок")
	spec.Player.Size = clampVector(spec.Player.Size, dims, 0.2, 20, defaultPlayerSize(spec.Mode))
	spec.Player.MoveSpeed = clampNumber(spec.Player.MoveSpeed, 0.5, 60, 6)
	spec.Player.Health = clampInt(spec.Player.Health, 1, 100, 3)
	jumpDefault := 12.0
	if flatGravity(spec.Genre) {
		jumpDefault = 0
	}
	spec.Player.JumpSpeed = clampNumber(spec.Player.JumpSpeed, 0, 60, jumpDefault)
	if flatGravity(spec.Genre) {
		spec.Player.JumpSpeed, spec.Player.DoubleJump = 0, false
	}

	if len(spec.Levels) == 0 {
		spec.Levels = []Level{{}}
	}
	if len(spec.Levels) > MaxLevels {
		spec.Levels = spec.Levels[:MaxLevels]
	}
	for i := range spec.Levels {
		spec.Levels[i] = normalizeLevel(spec, spec.Levels[i], i, dims)
	}
	return spec
}

func normalizeLevel(spec Spec, level Level, index, dims int) Level {
	level.Name = clampText(level.Name, 80, "Уровень "+itoa(index+1))
	level.Hint = clampText(level.Hint, 160, "")
	level.Spawn = clampVector(level.Spawn, dims, -1000, 1000, defaultSpawn(spec.Mode))
	if level.Gravity != nil {
		value := clampNumber(*level.Gravity, -100, 0, defaultGravity(spec))
		level.Gravity = &value
	}
	level.Platforms = normalizeBoxes(level.Platforms, spec.Palette.Platform, spec.Mode, dims, false)
	level.Hazards = normalizeBoxes(level.Hazards, spec.Palette.Hazard, spec.Mode, dims, true)
	level.Decor = normalizeBoxes(level.Decor, spec.Palette.Decor, spec.Mode, dims, false)
	if level.Goal != nil {
		goals := normalizeBoxes([]Box{*level.Goal}, spec.Palette.Goal, spec.Mode, dims, false)
		goal := goals[0]
		if goal.Name == "" {
			goal.Name = "Финиш"
		}
		level.Goal = &goal
	}
	level.Collectibles = normalizePickups(level.Collectibles, dims)
	level.Enemies = normalizeEnemies(level.Enemies, spec, dims)

	if !winConditions[level.Win] {
		level.Win = defaultWin(spec.Genre)
	}
	if !loseConditions[level.Lose] {
		level.Lose = defaultLose(spec.Genre)
	}
	if level.Win == "survive_time" || level.Lose == "timeout" {
		level.Seconds = clampNumber(level.Seconds, 5, 3600, 60)
	} else {
		level.Seconds = 0
	}
	return level
}

func normalizeBoxes(boxes []Box, fallbackColor, mode string, dims int, hazard bool) []Box {
	if len(boxes) > maxPerCategory {
		boxes = boxes[:maxPerCategory]
	}
	out := make([]Box, 0, len(boxes))
	for i, box := range boxes {
		box.Name = clampText(box.Name, 80, "Объект "+itoa(i+1))
		box.Position = clampVector(box.Position, dims, -1000, 1000, zeroes(dims))
		box.Size = clampVector(box.Size, dims, 0.1, 400, ones(dims))
		box.Color = normalizeColor(box.Color, fallbackColor)
		box.Shape = normalizeShape(box.Shape, mode)
		if hazard {
			box.Damage = clampInt(box.Damage, 1, 100, 1)
		} else {
			box.Damage = 0
		}
		out = append(out, box)
	}
	return out
}

func normalizePickups(pickups []Pickup, dims int) []Pickup {
	if len(pickups) > maxPerCategory {
		pickups = pickups[:maxPerCategory]
	}
	out := make([]Pickup, 0, len(pickups))
	for i, pickup := range pickups {
		pickup.Name = clampText(pickup.Name, 80, "Предмет "+itoa(i+1))
		pickup.Position = clampVector(pickup.Position, dims, -1000, 1000, zeroes(dims))
		if !pickupKinds[pickup.Kind] {
			pickup.Kind = "score"
		}
		pickup.Value = clampInt(pickup.Value, 1, 1000, 1)
		out = append(out, pickup)
	}
	return out
}

func normalizeEnemies(enemies []Enemy, spec Spec, dims int) []Enemy {
	if len(enemies) > maxPerCategory {
		enemies = enemies[:maxPerCategory]
	}
	out := make([]Enemy, 0, len(enemies))
	for i, enemy := range enemies {
		enemy.Name = clampText(enemy.Name, 80, "Противник "+itoa(i+1))
		enemy.Position = clampVector(enemy.Position, dims, -1000, 1000, zeroes(dims))
		enemy.Size = clampVector(enemy.Size, dims, 0.2, 20, defaultPlayerSize(spec.Mode))
		enemy.Color = normalizeColor(enemy.Color, spec.Palette.Enemy)
		if !behaviors[enemy.Behavior] {
			enemy.Behavior = "patrol"
		}
		enemy.Speed = clampNumber(enemy.Speed, 0.1, 40, 2)
		enemy.Damage = clampInt(enemy.Damage, 1, 100, 1)
		if enemy.Health != 0 {
			enemy.Health = clampInt(enemy.Health, 1, 1000, 1)
		}
		switch enemy.Behavior {
		case "patrol":
			enemy.Patrol = normalizePatrol(enemy.Patrol, enemy.Position, dims)
		case "spawned":
			enemy.Count = clampInt(enemy.Count, 1, 200, 8)
			enemy.IntervalSeconds = clampNumber(enemy.IntervalSeconds, 0.2, 120, 3)
			enemy.Patrol = nil
		default:
			enemy.Patrol = nil
		}
		out = append(out, enemy)
	}
	return out
}

// normalizePatrol guarantees at least two distinct waypoints; a patrol route
// that collapses to one point would leave the enemy standing still.
func normalizePatrol(points [][]float64, origin []float64, dims int) [][]float64 {
	cleaned := make([][]float64, 0, len(points))
	for _, point := range points {
		if len(cleaned) >= 32 {
			break
		}
		cleaned = append(cleaned, clampVector(point, dims, -1000, 1000, origin))
	}
	if len(cleaned) < 2 {
		second := append([]float64(nil), origin...)
		second[0] += 4
		cleaned = [][]float64{append([]float64(nil), origin...), second}
	}
	if sameVector(cleaned[0], cleaned[len(cleaned)-1]) && len(cleaned) == 2 {
		cleaned[1][0] += 4
	}
	return cleaned
}

func normalizePalette(palette Palette) Palette {
	palette.Background = normalizeColor(palette.Background, defaultPalette.Background)
	palette.Player = normalizeColor(palette.Player, defaultPalette.Player)
	palette.Platform = normalizeColor(palette.Platform, defaultPalette.Platform)
	palette.Hazard = normalizeColor(palette.Hazard, defaultPalette.Hazard)
	palette.Enemy = normalizeColor(palette.Enemy, defaultPalette.Enemy)
	palette.Collectible = normalizeColor(palette.Collectible, defaultPalette.Collectible)
	palette.Goal = normalizeColor(palette.Goal, defaultPalette.Goal)
	palette.Decor = normalizeColor(palette.Decor, defaultPalette.Decor)
	palette.UI = normalizeColor(palette.UI, defaultPalette.UI)
	return palette
}

func defaultWin(genre string) string {
	switch genre {
	case "arena":
		return "survive_time"
	case "puzzle", "room":
		return "reach_goal"
	default:
		return "reach_goal"
	}
}

func defaultLose(genre string) string {
	switch genre {
	case "platformer", "obstacle":
		return "health_zero"
	default:
		return "health_zero"
	}
}

func defaultGravity(spec Spec) float64 {
	if flatGravity(spec.Genre) {
		return 0
	}
	if spec.Mode == "3d" {
		return -18
	}
	return -22
}

// flatGravity marks the genres played on a plane, where a falling character
// would make no sense.
func flatGravity(genre string) bool {
	return genre == "top_down" || genre == "puzzle" || genre == "arena"
}

func defaultPlayerSize(mode string) []float64 {
	if mode == "3d" {
		return []float64{0.8, 1.6, 0.8}
	}
	return []float64{0.9, 1.3}
}

func defaultSpawn(mode string) []float64 {
	if mode == "3d" {
		return []float64{0, 1.5, 0}
	}
	return []float64{0, 1.5}
}

func dimensions(mode string) int {
	if mode == "3d" {
		return 3
	}
	return 2
}
