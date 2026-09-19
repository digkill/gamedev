package ai

import (
	"encoding/json"
)

func CompactContext(manifest json.RawMessage) json.RawMessage {
	var doc struct {
		Manifest struct {
			Title        string `json:"title"`
			Mode         string `json:"mode"`
			EntrySceneID string `json:"entry_scene_id"`
		} `json:"manifest"`
		Scenes []struct {
			ID       string `json:"id"`
			Name     string `json:"name"`
			Mode     string `json:"mode"`
			Entities []struct {
				ID         string           `json:"id"`
				Name       string           `json:"name"`
				ParentID   *string          `json:"parent_id"`
				Components []map[string]any `json:"components"`
			} `json:"entities"`
		} `json:"scenes"`
	}
	if json.Unmarshal(manifest, &doc) != nil {
		return json.RawMessage(`{}`)
	}
	type compactEntity struct {
		ID       string   `json:"id"`
		Name     string   `json:"name"`
		ParentID *string  `json:"parent_id"`
		Kinds    []string `json:"kinds"`
	}
	type compactScene struct {
		ID       string          `json:"id"`
		Name     string          `json:"name"`
		Entities []compactEntity `json:"entities"`
	}
	out := struct {
		Title string       `json:"title"`
		Mode  string       `json:"mode"`
		Scene compactScene `json:"scene"`
	}{Title: doc.Manifest.Title, Mode: doc.Manifest.Mode}
	for _, scene := range doc.Scenes {
		if scene.ID != doc.Manifest.EntrySceneID {
			continue
		}
		out.Scene.ID = scene.ID
		out.Scene.Name = scene.Name
		for _, entity := range scene.Entities {
			kinds := make([]string, 0, len(entity.Components))
			for _, component := range entity.Components {
				if t, ok := component["type"].(string); ok {
					kinds = append(kinds, t)
				}
			}
			out.Scene.Entities = append(out.Scene.Entities, compactEntity{
				ID: entity.ID, Name: entity.Name, ParentID: entity.ParentID, Kinds: kinds,
			})
		}
	}
	raw, err := json.Marshal(out)
	if err != nil || len(raw) > 32*1024 {
		fallback, _ := json.Marshal(map[string]string{"mode": doc.Manifest.Mode, "title": doc.Manifest.Title})
		return fallback
	}
	return raw
}
