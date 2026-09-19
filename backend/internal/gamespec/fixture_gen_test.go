package gamespec

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/digkill/gamedev/backend/internal/projects"
)

// TestWriteFixtures regenerates the contract fixtures produced by the
// compiler. It runs only when GAMESPEC_FIXTURE_DIR is set.
func TestWriteFixtures(t *testing.T) {
	dir := os.Getenv("GAMESPEC_FIXTURE_DIR")
	if dir == "" {
		t.Skip("set GAMESPEC_FIXTURE_DIR to regenerate contract fixtures")
	}
	for name, spec := range map[string]Spec{
		"platformer_2d": Template("Прыжки по крышам", "2d", "platformer", 2),
		"obstacle_3d":   Template("Полоса препятствий", "3d", "obstacle", 1),
		"arena_3d":      Template("Арена", "3d", "arena", 1),
	} {
		compiled, err := Compile(spec, projects.NewUUID)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if _, _, err := projects.CanonicalManifest(compiled.Document); err != nil {
			t.Fatalf("%s rejected by the Go validator: %v", name, err)
		}
		var pretty any
		if err := json.Unmarshal(compiled.Document, &pretty); err != nil {
			t.Fatal(err)
		}
		raw, err := json.MarshalIndent(pretty, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(dir+"/"+name+".project.json", append(raw, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}
