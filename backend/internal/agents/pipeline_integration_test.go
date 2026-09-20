package agents

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/digkill/gamedev/backend/internal/ai"
	"github.com/digkill/gamedev/backend/internal/projects"
	"github.com/jackc/pgx/v5/pgxpool"
)

// scriptedChat answers as whichever agent is asking, so the whole conveyor can
// run without a model provider.
type scriptedChat struct {
	mu      sync.Mutex
	calls   []string
	replies map[string]string
	fail    map[string]bool
}

func (c *scriptedChat) Configured() bool { return true }

func (c *scriptedChat) Chat(_ context.Context, req ai.ChatRequest) (ai.ChatResponse, error) {
	role := roleOf(req.System)
	c.mu.Lock()
	c.calls = append(c.calls, role)
	reply, ok := c.replies[role]
	shouldFail := c.fail[role]
	c.mu.Unlock()
	if shouldFail {
		return ai.ChatResponse{}, ai.ErrProvider
	}
	if !ok {
		return ai.ChatResponse{}, ai.ErrFailed
	}
	return ai.ChatResponse{Text: reply, Provider: "test", Model: "scripted"}, nil
}

func (c *scriptedChat) seen() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.calls...)
}

func roleOf(system string) string {
	switch {
	case strings.Contains(system, "Ты архитектор"):
		return "architect"
	case strings.Contains(system, "Ты гейм-дизайнер"):
		return "game_designer"
	case strings.Contains(system, "Ты разработчик"):
		return "developer"
	case strings.Contains(system, "Ты тестировщик"):
		return "tester"
	case strings.Contains(system, "Ты ревьюер"):
		return "reviewer"
	case strings.Contains(system, "Ты ревизор"):
		return "auditor"
	default:
		return "unknown"
	}
}

func defaultReplies() map[string]string {
	return map[string]string{
		"architect": `{"title":"Прыжки по крышам","mode":"2d","genre":"platformer","levels_planned":2,
			"win_condition":"reach_goal","lose_condition":"health_zero"}`,
		"game_designer": `{"title":"Прыжки по крышам","summary":"Кот убегает от собак по крышам.",
			"player":{"name":"Кот","move_speed":7,"jump_speed":13,"health":3}}`,
		"developer": `{"title":"Прыжки по крышам","mode":"2d","genre":"platformer","summary":"Кот убегает по крышам.",
			"player":{"name":"Кот","size":[0.9,1.3],"move_speed":7,"jump_speed":13,"health":3},
			"levels":[
			  {"name":"Двор","hint":"Беги вправо","gravity":-22,"spawn":[0,1.5],
			   "platforms":[{"name":"Земля","position":[6,-1],"size":[24,1]},{"name":"Ящик","position":[8,1],"size":[3,1]}],
			   "hazards":[{"name":"Шипы","position":[12,-0.2],"size":[2,0.6],"damage":1}],
			   "collectibles":[{"name":"Рыбка","position":[8,2.5],"kind":"score","value":1}],
			   "enemies":[{"name":"Пёс","position":[15,0],"behavior":"patrol","patrol":[[13,0],[17,0]],"speed":2.5,"damage":1,"health":1}],
			   "goal":{"name":"Лестница","position":[17,0.5],"size":[1.2,2.4]},
			   "win":"reach_goal","lose":"health_zero"},
			  {"name":"Крыши","hint":"Собери всё","gravity":-22,"spawn":[0,1.5],
			   "platforms":[{"name":"Скат","position":[5,-1],"size":[20,1]},{"name":"Труба","position":[9,1],"size":[2,1]}],
			   "hazards":[{"name":"Провал","position":[13,-1.6],"size":[2,0.5],"damage":1}],
			   "collectibles":[{"name":"Ключ","position":[9,2.5],"kind":"key","value":1},
			                   {"name":"Монета","position":[4,0.5],"kind":"score","value":1}],
			   "enemies":[{"name":"Ворона","position":[12,0.5],"behavior":"chase","speed":3,"damage":1,"health":1}],
			   "goal":{"name":"Люк","position":[14,0.5],"size":[1.2,2]},
			   "win":"collect_all","lose":"health_zero"}
			]}`,
		"tester":   `{"verdict":"pass","defects":[],"playthrough":"Бежим вправо, прыгаем через шипы, доходим до лестницы.","notes":"Играбельно"}`,
		"reviewer": `{"verdict":"approve","score":8,"matches_request":true,"findings":[],"improvements":[]}`,
		"auditor":  `{"verdict":"approved","policy_violations":[],"unresolved":[],"summary":"Небольшой платформер из двух уровней."}`,
	}
}

func openTestDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set TEST_DATABASE_URL for the pipeline integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func newTestUser(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	id, err := projects.NewUUID()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO users(id, display_name) VALUES ($1,$2)`, id, "pipeline test"); err != nil {
		t.Fatal(err)
	}
	return id
}

func runPipeline(t *testing.T, pool *pgxpool.Pool, chat ai.Chat, req NewJob) Job {
	t.Helper()
	store := Store{DB: pool}
	pipeline := Pipeline{
		Store: store, Projects: projects.Store{DB: pool}, Chat: chat,
		MaxRepairs: 1, StepTimeout: 30 * time.Second, NewID: projects.NewUUID,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	id, err := projects.NewUUID()
	if err != nil {
		t.Fatal(err)
	}
	job, err := store.CreateJob(ctx, id, req)
	if err != nil {
		t.Fatal(err)
	}
	claimed, found, err := store.ClaimJob(ctx, time.Minute)
	if err != nil || !found {
		t.Fatalf("claim: %v found=%v", err, found)
	}
	if claimed.ID != job.ID {
		t.Fatalf("claimed a different job: %s", claimed.ID)
	}
	if err := pipeline.Run(ctx, claimed); err != nil {
		t.Fatalf("run: %v", err)
	}
	finished, err := store.Job(ctx, req.OwnerID, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	return finished
}

func TestConveyorProducesPlayableProject(t *testing.T) {
	pool := openTestDB(t)
	owner := newTestUser(t, pool)
	chat := &scriptedChat{replies: defaultReplies(), fail: map[string]bool{}}

	job := runPipeline(t, pool, chat, NewJob{
		OwnerID: owner, Kind: "create_game", Mode: "2d",
		Prompt: "Сделай платформер про кота на крышах", IdempotencyKey: "test-" + owner,
	})
	if job.Status != "succeeded" {
		t.Fatalf("job failed: %s %s %s", job.Status, job.ErrorCode, job.ErrorMessage)
	}

	// Every agent must have been consulted, in order.
	want := []string{"architect", "game_designer", "developer", "tester", "reviewer", "auditor"}
	got := chat.seen()
	if len(got) != len(want) {
		t.Fatalf("unexpected agent calls: %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("agent order %v, want %v", got, want)
		}
	}

	var result Result
	if err := json.Unmarshal(job.Result, &result); err != nil {
		t.Fatal(err)
	}
	if result.ProjectID == "" || result.Stats.Scenes != 2 {
		t.Fatalf("unexpected result: %+v", result)
	}
	if result.Stats.Platforms < 2 || result.Stats.Goals != 2 || result.Stats.UIWidgets < 4 {
		t.Fatalf("the built game is missing parts: %+v", result.Stats)
	}
	if result.AuditVerdict != "approved" {
		t.Fatalf("audit verdict %q", result.AuditVerdict)
	}

	// The stored project must be a document the validator accepts.
	project, err := (projects.Store{DB: pool}).Get(context.Background(), owner, result.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := projects.CanonicalManifest(project.Manifest); err != nil {
		t.Fatalf("stored project is invalid: %v", err)
	}

	// Each stage must have left an audit row.
	if len(job.Steps) < 7 {
		t.Fatalf("expected a step per stage, got %d", len(job.Steps))
	}
	for _, step := range job.Steps {
		if step.Status != "succeeded" {
			t.Fatalf("step %s failed: %s", step.Agent, step.Notes)
		}
	}

	// The conveyor's memory must be filed under the project so a later request
	// can build on it.
	entries, err := (Store{DB: pool}).ProjectContext(context.Background(), owner, result.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]bool{}
	for _, entry := range entries {
		kinds[entry.Kind] = true
	}
	for _, kind := range []string{"blueprint", "design", "spec", "summary"} {
		if !kinds[kind] {
			t.Fatalf("project memory is missing %q: %v", kind, kinds)
		}
	}
}

// With the developer unreachable the user must still receive a playable game,
// clearly marked as degraded.
func TestConveyorFallsBackToTemplate(t *testing.T) {
	pool := openTestDB(t)
	owner := newTestUser(t, pool)
	chat := &scriptedChat{replies: defaultReplies(), fail: map[string]bool{"developer": true}}

	job := runPipeline(t, pool, chat, NewJob{
		OwnerID: owner, Kind: "create_game", Mode: "2d",
		Prompt: "Платформер про кота", IdempotencyKey: "fallback-" + owner,
	})
	if job.Status != "succeeded" {
		t.Fatalf("job failed: %s %s", job.ErrorCode, job.ErrorMessage)
	}
	var result Result
	if err := json.Unmarshal(job.Result, &result); err != nil {
		t.Fatal(err)
	}
	if !result.Degraded {
		t.Fatal("a template fallback must be reported as degraded")
	}
	if result.Stats.Platforms == 0 || result.Stats.Goals == 0 {
		t.Fatalf("the fallback game is not playable: %+v", result.Stats)
	}
	project, err := (projects.Store{DB: pool}).Get(context.Background(), owner, result.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := projects.CanonicalManifest(project.Manifest); err != nil {
		t.Fatalf("fallback project is invalid: %v", err)
	}
}

// A second run over the same project must produce a new revision rather than a
// second project, and must read the memory of the first run.
func TestConveyorImprovesExistingProject(t *testing.T) {
	pool := openTestDB(t)
	owner := newTestUser(t, pool)
	chat := &scriptedChat{replies: defaultReplies(), fail: map[string]bool{}}

	first := runPipeline(t, pool, chat, NewJob{
		OwnerID: owner, Kind: "create_game", Mode: "2d",
		Prompt: "Платформер про кота", IdempotencyKey: "improve-first-" + owner,
	})
	var created Result
	if err := json.Unmarshal(first.Result, &created); err != nil {
		t.Fatal(err)
	}

	projectID := created.ProjectID
	second := runPipeline(t, pool, chat, NewJob{
		OwnerID: owner, Kind: "improve_game", ProjectID: &projectID, Mode: "2d",
		Prompt: "Добавь второй уровень сложнее", IdempotencyKey: "improve-second-" + owner,
	})
	if second.Status != "succeeded" {
		t.Fatalf("improve job failed: %s %s", second.ErrorCode, second.ErrorMessage)
	}
	var improved Result
	if err := json.Unmarshal(second.Result, &improved); err != nil {
		t.Fatal(err)
	}
	if improved.ProjectID != projectID {
		t.Fatalf("improve must stay in the same project: %s != %s", improved.ProjectID, projectID)
	}
	if improved.RevisionID == "" || improved.RevisionID == created.RevisionID {
		t.Fatal("improve must create a new revision")
	}
	revisions, _, err := (projects.Store{DB: pool}).ListRevisions(context.Background(), owner, projectID, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(revisions) < 2 {
		t.Fatalf("history must keep both versions, got %d", len(revisions))
	}
}

func TestConveyorRejectsBannedContent(t *testing.T) {
	pool := openTestDB(t)
	owner := newTestUser(t, pool)
	replies := defaultReplies()
	replies["developer"] = strings.Replace(replies["developer"], `"title":"Прыжки по крышам"`, `"title":"Porn Runner"`, 1)
	chat := &scriptedChat{replies: replies, fail: map[string]bool{}}

	job := runPipeline(t, pool, chat, NewJob{
		OwnerID: owner, Kind: "create_game", Mode: "2d",
		Prompt: "Платформер", IdempotencyKey: "policy-" + owner,
	})
	if job.Status != "failed" || job.ErrorCode != "POLICY_REJECTED" {
		t.Fatalf("banned content must be rejected, got %s/%s", job.Status, job.ErrorCode)
	}
	if job.ProjectID != nil {
		t.Fatal("a rejected job must not publish a project")
	}
}

// A job whose lease expired is re-claimed and restarted from the first agent.
// The step numbering has to continue past the earlier attempt instead of
// reusing seq 1, which used to break the run on a unique-constraint violation.
func TestReclaimedJobKeepsNumberingSteps(t *testing.T) {
	pool := openTestDB(t)
	owner := newTestUser(t, pool)
	store := Store{DB: pool}
	ctx := context.Background()

	id, err := projects.NewUUID()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateJob(ctx, id, NewJob{
		OwnerID: owner, Kind: "create_game", Mode: "2d",
		Prompt: "Платформер", IdempotencyKey: "reclaim-" + owner,
	}); err != nil {
		t.Fatal(err)
	}
	claimed, found, err := store.ClaimJob(ctx, time.Minute)
	if err != nil || !found || claimed.ID != id {
		t.Fatalf("first claim: %v found=%v", err, found)
	}
	for _, agent := range []string{"architect", "game_designer"} {
		stepID, err := projects.NewUUID()
		if err != nil {
			t.Fatal(err)
		}
		if err := store.StartStep(ctx, stepID, id, agent, 1); err != nil {
			t.Fatalf("first attempt step %s: %v", agent, err)
		}
	}

	// The worker died without finishing: expire the lease by hand.
	if _, err := pool.Exec(ctx, `UPDATE ai_jobs SET lease_expires_at = now() - interval '1 minute' WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	retaken, found, err := store.ClaimJob(ctx, time.Minute)
	if err != nil || !found || retaken.ID != id {
		t.Fatalf("a job with an expired lease must be re-claimable: %v found=%v", err, found)
	}
	stepID, err := projects.NewUUID()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.StartStep(ctx, stepID, id, "architect", 1); err != nil {
		t.Fatalf("the restarted run must be able to record its first step: %v", err)
	}

	job, err := store.Job(ctx, owner, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(job.Steps) != 3 {
		t.Fatalf("want 3 steps across both attempts, got %d", len(job.Steps))
	}
	for i, step := range job.Steps {
		if step.Seq != i+1 {
			t.Fatalf("step %d has seq %d; numbering must stay dense and unique", i, step.Seq)
		}
	}
}
