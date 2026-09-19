package httpapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/digkill/gamedev/backend/internal/projects"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Run against a disposable, Goose-migrated PostgreSQL with TEST_DATABASE_URL.
func TestProjectLifecycleWithPostgres(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set TEST_DATABASE_URL for PostgreSQL integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	userID, err := projects.NewUUID()
	if err != nil {
		t.Fatal(err)
	}
	token := "local_integration_test_token_" + userID
	hash := sha256.Sum256([]byte(token))
	if _, err := pool.Exec(ctx, "INSERT INTO users(id, display_name) VALUES ($1, $2)", userID, "HTTP integration test"); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "INSERT INTO access_tokens(token_hash, user_id, expires_at) VALUES ($1, $2, now()+interval '1 hour')", hash[:], userID); err != nil {
		t.Fatal(err)
	}

	fixture := filepath.Join("..", "..", "..", "..", "engine", "godot-host", "demo", "empty_2d.project.json")
	raw, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatal(err)
	}
	projectID, err := projects.NewUUID()
	if err != nil {
		t.Fatal(err)
	}
	var editable map[string]any
	if err := json.Unmarshal(raw, &editable); err != nil {
		t.Fatal(err)
	}
	editable["manifest"].(map[string]any)["project_id"] = projectID
	raw, err = json.Marshal(editable)
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		Manifest struct {
			ProjectID string `json:"project_id"`
		} `json:"manifest"`
	}
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	create, err := json.Marshal(map[string]any{"title": "2D sample", "mode": "2d", "schema_version": 1, "manifest": json.RawMessage(raw)})
	if err != nil {
		t.Fatal(err)
	}
	api := API{DB: pool, Projects: projects.Store{DB: pool}}.Handler()
	call := func(method, path, key string, body []byte, bearer string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, bytes.NewReader(body))
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		if key != "" {
			req.Header.Set("Idempotency-Key", key)
		}
		rec := httptest.NewRecorder()
		api.ServeHTTP(rec, req)
		return rec
	}
	if got := call("POST", "/api/v1/projects", "create-one", create, ""); got.Code != 401 {
		t.Fatalf("unauthenticated create: %d", got.Code)
	}
	first := call("POST", "/api/v1/projects", "create-one", create, token)
	if first.Code != 201 {
		t.Fatalf("create: %d %s", first.Code, first.Body.String())
	}
	var created projects.Project
	if err := json.Unmarshal(first.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.ID != document.Manifest.ProjectID || created.HeadRevisionID == "" {
		t.Fatal("project identity/revision mismatch")
	}
	second := call("POST", "/api/v1/projects", "create-one", create, token)
	var replayed projects.Project
	if err := json.Unmarshal(second.Body.Bytes(), &replayed); err != nil {
		t.Fatal(err)
	}
	if second.Code != 201 || replayed.ID != created.ID || replayed.HeadRevisionID != created.HeadRevisionID {
		t.Fatalf("idempotent replay: %d", second.Code)
	}
	var alternate map[string]any
	if err := json.Unmarshal(create, &alternate); err != nil {
		t.Fatal(err)
	}
	alternate["title"] = "Altered"
	alternate["manifest"].(map[string]any)["manifest"].(map[string]any)["title"] = "Altered"
	changedBody, err := json.Marshal(alternate)
	if err != nil {
		t.Fatal(err)
	}
	if got := call("POST", "/api/v1/projects", "create-one", changedBody, token); got.Code != 409 {
		t.Fatalf("idempotency conflict: %d", got.Code)
	}

	path := "/api/v1/projects/" + created.ID
	if got := call("GET", path, "", nil, token); got.Code != 200 {
		t.Fatalf("owner read: %d", got.Code)
	}
	if got := call("GET", path, "", nil, "wrong_bearer_token_that_is_long_enough"); got.Code != 401 {
		t.Fatalf("foreign read: %d", got.Code)
	}
	operationID, err := projects.NewUUID()
	if err != nil {
		t.Fatal(err)
	}
	sceneID, err := projects.NewUUID()
	if err != nil {
		t.Fatal(err)
	}
	change, err := json.Marshal(map[string]any{
		"base_revision_id": created.HeadRevisionID,
		"operations":       []any{map[string]any{"operation_id": operationID, "type": "CreateScene", "scene": map[string]any{"id": sceneID, "name": "Second", "mode": "2d", "entities": []any{}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	changed := call("POST", path+"/changes", "change-one", change, token)
	if changed.Code != 200 {
		t.Fatalf("change: %d %s", changed.Code, changed.Body.String())
	}
	if got := call("POST", path+"/changes", "change-two", change, token); got.Code != 409 || !strings.Contains(got.Body.String(), "REVISION_CONFLICT") {
		t.Fatalf("stale revision: %d %s", got.Code, got.Body.String())
	}
}
