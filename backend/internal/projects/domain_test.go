package projects

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func fixture(t *testing.T, mode string) json.RawMessage {
	t.Helper()
	path := filepath.Join("..", "..", "..", "engine", "godot-host", "demo", "empty_"+mode+".project.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func document(t *testing.T, raw json.RawMessage) map[string]any {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

func encoded(t *testing.T, doc map[string]any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestBundledProjectsPassBackendValidation(t *testing.T) {
	for _, mode := range []string{"2d", "3d"} {
		t.Run(mode, func(t *testing.T) {
			raw := fixture(t, mode)
			doc, err := validateProject(raw)
			if err != nil {
				t.Fatal(err)
			}
			request := CreateRequest{Title: doc.Manifest.Title, Mode: mode, SchemaVersion: 1, Manifest: raw}
			if err := request.Validate(); err != nil {
				t.Fatal(err)
			}
			if _, _, err := CanonicalManifest(raw); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestRejectsExecutableAndBrokenProjectData(t *testing.T) {
	base := fixture(t, "2d")
	tests := map[string]func(map[string]any){
		"arbitrary top-level field": func(d map[string]any) { d["godot_script"] = "extends Node" },
		"unapproved asset":          func(d map[string]any) { d["assets"] = []any{map[string]any{"id": "bad"}} },
		"unknown component": func(d map[string]any) {
			entity := d["scenes"].([]any)[0].(map[string]any)["entities"].([]any)[0].(map[string]any)
			entity["components"] = append(entity["components"].([]any), map[string]any{"type": "Script", "source": "import os"})
		},
		"wrong entry scene": func(d map[string]any) {
			d["manifest"].(map[string]any)["entry_scene_id"] = "36b59224-8072-4d12-87ac-255593d3cb1b"
		},
		"parent cycle": func(d map[string]any) {
			entity := d["scenes"].([]any)[0].(map[string]any)["entities"].([]any)[0].(map[string]any)
			entity["parent_id"] = entity["id"]
		},
		"unknown component property": func(d map[string]any) {
			entity := d["scenes"].([]any)[0].(map[string]any)["entities"].([]any)[0].(map[string]any)
			entity["components"].([]any)[1].(map[string]any)["script_url"] = "https://bad.example/run"
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			d := document(t, base)
			mutate(d)
			if _, err := validateProject(encoded(t, d)); !errors.Is(err, ErrInvalid) {
				t.Fatalf("expected invalid, got %v", err)
			}
		})
	}
}

func TestCreateScenePreservesValidatedDocument(t *testing.T) {
	base := fixture(t, "2d")
	operation := Command{
		OperationID: "f5664c8b-d0ce-44bb-9bde-f9724b1f423a",
		Type:        "CreateScene",
		Scene:       json.RawMessage(`{"id":"70e63213-51e1-457a-ac0b-d4e61eb5a7fe","name":"Second","mode":"2d","entities":[]}`),
	}
	result, err := ApplyCommands(base, "2d", []Command{operation})
	if err != nil {
		t.Fatal(err)
	}
	doc, err := validateProject(result)
	if err != nil || len(doc.Scenes) != 2 {
		t.Fatalf("result invalid: %v", err)
	}
	if doc.Manifest.ProjectID != "36b59224-8072-4d12-87ac-255593d3cb1b" {
		t.Fatal("project ID changed")
	}
	if _, err := ApplyCommands(result, "2d", []Command{operation}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("duplicate scene accepted: %v", err)
	}
}

func TestStage0CommandsEditEntities(t *testing.T) {
	base := fixture(t, "2d")
	entity := json.RawMessage(`{"id":"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa","name":"Red circle","parent_id":null,"components":[{"type":"Transform","space":"2d","position":[40,0],"rotation":0,"scale":[1,1]},{"type":"Sprite","shape":"circle","tint":"#FF5555FF","layer":1}]}`)
	created, err := ApplyCommands(base, "2d", []Command{{
		OperationID: "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb",
		Type:        "CreateEntity",
		SceneID:     "403f3d5b-80db-4ee5-af2f-f00e9231414a",
		Entity:      entity,
	}})
	if err != nil {
		t.Fatal(err)
	}
	doc, err := validateProject(created)
	if err != nil || len(doc.Scenes[0].Entities) != 2 {
		t.Fatalf("create entity: %v %d", err, len(doc.Scenes[0].Entities))
	}
	moved, err := ApplyCommands(created, "2d", []Command{{
		OperationID: "cccccccc-cccc-4ccc-8ccc-cccccccccccc",
		Type:        "SetProperty",
		SceneID:     "403f3d5b-80db-4ee5-af2f-f00e9231414a",
		EntityID:    "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
		Property:    "Transform.position",
		Value:       json.RawMessage(`[80, 10]`),
	}})
	if err != nil {
		t.Fatal(err)
	}
	renamed, err := ApplyCommands(moved, "2d", []Command{{
		OperationID: "dddddddd-dddd-4ddd-8ddd-dddddddddddd",
		Type:        "SetProjectSetting",
		Setting:     "title",
		Value:       json.RawMessage(`"Arena"`),
	}})
	if err != nil {
		t.Fatal(err)
	}
	doc, err = validateProject(renamed)
	if err != nil || doc.Manifest.Title != "Arena" {
		t.Fatalf("rename: %v %q", err, doc.Manifest.Title)
	}
	deleted, err := ApplyCommands(renamed, "2d", []Command{{
		OperationID: "eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee",
		Type:        "DeleteEntity",
		SceneID:     "403f3d5b-80db-4ee5-af2f-f00e9231414a",
		EntityID:    "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
	}})
	if err != nil {
		t.Fatal(err)
	}
	doc, err = validateProject(deleted)
	if err != nil || len(doc.Scenes[0].Entities) != 1 {
		t.Fatalf("delete: %v", err)
	}
}

func TestRejectsUnsupportedAndUnsafeCommands(t *testing.T) {
	base := fixture(t, "2d")
	tests := []Command{
		{OperationID: "11111111-1111-4111-8111-111111111111", Type: "CreatePrefab"},
		{OperationID: "22222222-2222-4222-8222-222222222222", Type: "SetScript", Entity: json.RawMessage(`{"id":"x"}`)},
		{OperationID: "33333333-3333-4333-8333-333333333333", Type: "SetProperty", SceneID: "403f3d5b-80db-4ee5-af2f-f00e9231414a", EntityID: "6a2db48a-8245-4bba-bf62-2f44e685e668", Property: "Health.maximum", Value: json.RawMessage(`10`)},
		{OperationID: "44444444-4444-4444-8444-444444444444", Type: "CreateEntity", SceneID: "403f3d5b-80db-4ee5-af2f-f00e9231414a", Entity: json.RawMessage(`{"id":"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa","name":"Bad","parent_id":null,"components":[{"type":"Transform","space":"2d","position":[0,0],"rotation":0,"scale":[1,1]},{"type":"Script","source":"import os"}]}`)},
	}
	for _, op := range tests {
		if _, err := ApplyCommands(base, "2d", []Command{op}); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%s accepted: %v", op.Type+" "+op.Property, err)
		}
	}
}
