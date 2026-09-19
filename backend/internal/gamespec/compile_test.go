package gamespec

import (
	"encoding/json"
	"testing"

	"github.com/digkill/gamedev/backend/internal/projects"
)

// A spec with almost nothing in it still has to compile into a document the
// project validator accepts: that is the whole point of the compiler.
func TestCompileEmptySpecProducesValidProject(t *testing.T) {
	compiled, err := Compile(Repair(Normalize(Spec{})), projects.NewUUID)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if _, _, err := projects.CanonicalManifest(compiled.Document); err != nil {
		t.Fatalf("compiled document rejected: %v\n%s", err, compiled.Document)
	}
	if compiled.Stats.Scenes != 1 || compiled.Stats.Platforms == 0 {
		t.Fatalf("unexpected stats: %+v", compiled.Stats)
	}
}

func TestCompileFullTwoLevelPlatformer(t *testing.T) {
	spec := Spec{
		Title: "Прыжки по крышам", Mode: "2d", Genre: "platformer",
		Player: Player{Name: "Кот", MoveSpeed: 7, JumpSpeed: 14, Health: 3},
		Levels: []Level{
			{
				Name: "Двор", Spawn: []float64{0, 1.5}, Win: "reach_goal", Lose: "health_zero",
				Platforms: []Box{
					{Name: "Земля", Position: []float64{6, -1}, Size: []float64{24, 1}},
					{Name: "Ящик", Position: []float64{6, 1}, Size: []float64{2, 1}},
				},
				Hazards:      []Box{{Name: "Шипы", Position: []float64{10, -0.2}, Size: []float64{2, 0.6}, Damage: 1}},
				Collectibles: []Pickup{{Name: "Рыбка", Position: []float64{6, 2.5}, Kind: "score", Value: 1}},
				Enemies: []Enemy{{
					Name: "Пёс", Position: []float64{14, 0}, Behavior: "patrol",
					Patrol: [][]float64{{12, 0}, {17, 0}}, Speed: 2.5, Damage: 1, Health: 1,
				}},
				Goal: &Box{Name: "Крыша", Position: []float64{20, 0.5}, Size: []float64{1.2, 2.4}},
			},
			{
				Name: "Крыши", Spawn: []float64{0, 1.5}, Win: "collect_all", Lose: "health_zero",
				Platforms:    []Box{{Name: "Скат", Position: []float64{4, -1}, Size: []float64{20, 1}}},
				Collectibles: []Pickup{{Name: "Ключ", Position: []float64{4, 1}, Kind: "key", Value: 1}},
				Enemies: []Enemy{{
					Name: "Ворона", Position: []float64{9, 2}, Behavior: "chase", Speed: 3, Damage: 1,
				}},
				Goal: &Box{Name: "Люк", Position: []float64{12, 0.5}, Size: []float64{1.2, 2}},
			},
		},
	}
	normalized := Normalize(spec)
	if defects := Fatal(Validate(normalized)); len(defects) != 0 {
		t.Fatalf("unexpected defects: %+v", defects)
	}
	compiled, err := Compile(normalized, projects.NewUUID)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if _, _, err := projects.CanonicalManifest(compiled.Document); err != nil {
		t.Fatalf("compiled document rejected: %v\n%s", err, compiled.Document)
	}
	var doc struct {
		Manifest struct {
			EntrySceneID string `json:"entry_scene_id"`
		} `json:"manifest"`
		Scenes []struct {
			ID    string `json:"id"`
			Rules struct {
				OnWin       string  `json:"on_win"`
				NextSceneID *string `json:"next_scene_id"`
			} `json:"rules"`
			Entities []struct {
				Components []map[string]any `json:"components"`
			} `json:"entities"`
		} `json:"scenes"`
	}
	if err := json.Unmarshal(compiled.Document, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Scenes) != 2 {
		t.Fatalf("want 2 scenes, got %d", len(doc.Scenes))
	}
	if doc.Scenes[0].Rules.OnWin != "next_scene" || doc.Scenes[0].Rules.NextSceneID == nil ||
		*doc.Scenes[0].Rules.NextSceneID != doc.Scenes[1].ID {
		t.Fatalf("first level must lead into the second: %+v", doc.Scenes[0].Rules)
	}
	if doc.Scenes[1].Rules.OnWin != "show_result" {
		t.Fatalf("last level must end the game, got %q", doc.Scenes[1].Rules.OnWin)
	}
	for i, scene := range doc.Scenes {
		kinds := map[string]int{}
		for _, entity := range scene.Entities {
			for _, component := range entity.Components {
				if name, ok := component["type"].(string); ok {
					kinds[name]++
				}
			}
		}
		for _, required := range []string{"Camera", "CameraFollow", "CharacterController", "Health", "UIWidget", "Collider"} {
			if kinds[required] == 0 {
				t.Fatalf("scene %d is missing %s: %v", i, required, kinds)
			}
		}
	}
}

func TestCompile3DObstacleCourse(t *testing.T) {
	spec := Normalize(Spec{
		Title: "Полоса препятствий", Mode: "3d", Genre: "obstacle",
		Levels: []Level{{
			Name: "Старт", Spawn: []float64{0, 2, 0}, Win: "reach_goal", Lose: "fall_out",
			Platforms: []Box{
				{Name: "Площадка", Position: []float64{0, 0, 0}, Size: []float64{8, 1, 8}},
				{Name: "Мост", Position: []float64{0, 0, 10}, Size: []float64{2, 0.5, 12}},
			},
			Hazards: []Box{{Name: "Лава", Position: []float64{0, -2, 10}, Size: []float64{20, 0.5, 20}, Damage: 5}},
			Goal:    &Box{Name: "Портал", Position: []float64{0, 1, 18}, Size: []float64{2, 3, 2}},
		}},
	})
	compiled, err := Compile(spec, projects.NewUUID)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if _, _, err := projects.CanonicalManifest(compiled.Document); err != nil {
		t.Fatalf("compiled 3d document rejected: %v\n%s", err, compiled.Document)
	}
}

func TestValidateReportsMissingGoal(t *testing.T) {
	spec := Normalize(Spec{
		Mode: "2d", Genre: "platformer",
		Levels: []Level{{
			Spawn: []float64{0, 1}, Win: "reach_goal",
			Platforms: []Box{{Name: "Земля", Position: []float64{0, -1}, Size: []float64{20, 1}}},
			Enemies:   []Enemy{{Name: "Враг", Position: []float64{5, 0}, Behavior: "patrol", Health: 1}},
			Hazards:   []Box{{Name: "Шипы", Position: []float64{8, -0.2}, Size: []float64{1, 0.5}}},
			Decor:     []Box{{Name: "Куст", Position: []float64{3, 0}, Size: []float64{1, 1}}},
		}},
	})
	defects := Validate(spec)
	found := false
	for _, defect := range defects {
		if defect.Code == "GOAL_MISSING" && defect.Fatal {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing goal must be reported: %+v", defects)
	}
	repaired := Repair(spec)
	if remaining := Fatal(Validate(repaired)); len(remaining) != 0 {
		t.Fatalf("repair must clear fatal defects: %+v", remaining)
	}
	if repaired.Levels[0].Goal == nil {
		t.Fatal("repair must add the missing goal")
	}
}
