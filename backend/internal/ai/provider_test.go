package ai

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/digkill/gamedev/backend/internal/projects"
)

func TestUnavailableWithoutKey(t *testing.T) {
	p := New("", "gpt-4.1-mini")
	if p.Configured() {
		t.Fatal("empty key must disable AI")
	}
	_, err := p.Generate(context.Background(), Request{Prompt: "платформер"})
	if err != ErrUnavailable {
		t.Fatalf("got %v", err)
	}
}

func TestParseProposalNormalizesEmptyFields(t *testing.T) {
	id, err := projects.NewUUID()
	if err != nil {
		t.Fatal(err)
	}
	scene, err := projects.NewUUID()
	if err != nil {
		t.Fatal(err)
	}
	entity, err := projects.NewUUID()
	if err != nil {
		t.Fatal(err)
	}
	raw := `{"summary":"Куб","operations":[{"operation_id":"` + id + `","type":"DeleteEntity","scene_id":"` + scene + `","entity_id":"` + entity + `","scene":null,"entity":null}]}`
	ops, summary, err := parseProposal(raw)
	if err != nil || summary != "Куб" || len(ops) != 1 {
		t.Fatalf("%v %q %d", err, summary, len(ops))
	}
	if len(ops[0].Scene) != 0 || len(ops[0].Entity) != 0 {
		t.Fatalf("empty fields: %+v", ops[0])
	}
}

func TestCompactContextKeepsEntryScene(t *testing.T) {
	raw := json.RawMessage(`{"manifest":{"title":"A","mode":"2d","entry_scene_id":"11111111-1111-4111-8111-111111111111"},"scenes":[{"id":"11111111-1111-4111-8111-111111111111","name":"Stage","entities":[{"id":"22222222-2222-4222-8222-222222222222","name":"Hero","parent_id":null,"components":[{"type":"Transform"}]}]}]}`)
	got := CompactContext(raw)
	var payload struct {
		Scene struct {
			Entities []struct{ Name string } `json:"entities"`
		} `json:"scene"`
	}
	if json.Unmarshal(got, &payload) != nil || len(payload.Scene.Entities) != 1 || payload.Scene.Entities[0].Name != "Hero" {
		t.Fatalf("%s", got)
	}
}

func TestUserInputContainsJSON(t *testing.T) {
	got := userInput(Request{Prompt: "платформер", Mode: "2d", Context: json.RawMessage(`{"scene":{"id":"1"}}`)}, "")
	if !strings.Contains(strings.ToLower(got), "json") {
		t.Fatalf("input must mention json: %s", got)
	}
}

func TestSanitizeCreateEntityDropsExtraFields(t *testing.T) {
	manifest := json.RawMessage(`{"manifest":{"schema_version":1,"project_id":"36b59224-8072-4d12-87ac-255593d3cb1b","title":"2D sample","mode":"2d","entry_scene_id":"403f3d5b-80db-4ee5-af2f-f00e9231414a","runtime_api_version":"1.0","minimum_app_version":"1.0.0","orientation":"landscape","input_profile":"touch_platformer","capabilities":["scene","input"],"logic":{"graph":false,"python_sdk_version":"1.0"},"limits_profile":"mobile_standard_v1"},"scenes":[{"id":"403f3d5b-80db-4ee5-af2f-f00e9231414a","name":"Stage","mode":"2d","entities":[{"id":"6a2db48a-8245-4bba-bf62-2f44e685e668","name":"Blue square","parent_id":null,"components":[{"type":"Transform","space":"2d","position":[0,0],"rotation":0,"scale":[2,2]},{"type":"Sprite","shape":"rectangle","tint":"#4DA3FFFF","layer":0}]}]}],"assets":[],"scripts":[],"graphs":[],"prefabs":[]}`)
	ops := sanitizeOps("2d", manifest, []projects.Command{{
		OperationID: "not-a-uuid",
		Type:        "CreateEntity",
		SceneID:     "wrong",
		EntityID:    "also-wrong",
		Entity:      json.RawMessage(`{"id":"nope","name":"Hero","visible":true,"parent_id":null,"components":[{"type":"Transform","space":"2d","position":[1,2,3],"rotation":0,"scale":[1,1],"extra":true},{"type":"Sprite","shape":"rectangle","tint":"#FF0","layer":0,"width":10}]}`),
	}})
	if applyError("2d", manifest, ops) != "" || len(ops) != 1 {
		t.Fatalf("sanitized ops invalid: %s %d", applyError("2d", manifest, ops), len(ops))
	}
}

func TestFallbackOpsAreValid(t *testing.T) {
	manifest := json.RawMessage(`{"manifest":{"schema_version":1,"project_id":"36b59224-8072-4d12-87ac-255593d3cb1b","title":"2D sample","mode":"2d","entry_scene_id":"403f3d5b-80db-4ee5-af2f-f00e9231414a","runtime_api_version":"1.0","minimum_app_version":"1.0.0","orientation":"landscape","input_profile":"touch_platformer","capabilities":["scene","input"],"logic":{"graph":false,"python_sdk_version":"1.0"},"limits_profile":"mobile_standard_v1"},"scenes":[{"id":"403f3d5b-80db-4ee5-af2f-f00e9231414a","name":"Stage","mode":"2d","entities":[{"id":"6a2db48a-8245-4bba-bf62-2f44e685e668","name":"Blue square","parent_id":null,"components":[{"type":"Transform","space":"2d","position":[0,0],"rotation":0,"scale":[2,2]},{"type":"Sprite","shape":"rectangle","tint":"#4DA3FFFF","layer":0}]}]}],"assets":[],"scripts":[],"graphs":[],"prefabs":[]}`)
	ops := fallbackOps("2d", "403f3d5b-80db-4ee5-af2f-f00e9231414a", "платформер")
	if applyError("2d", manifest, ops) != "" || len(ops) != 3 {
		t.Fatalf("fallback invalid: %s %d", applyError("2d", manifest, ops), len(ops))
	}
}
