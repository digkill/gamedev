package agents

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/digkill/gamedev/backend/internal/ai"
	"github.com/digkill/gamedev/backend/internal/gamespec"
	"github.com/digkill/gamedev/backend/internal/projects"
)

var ErrCancelled = errors.New("job cancelled")

// finish reports a closing write that lost the job as success: the worker that
// took over is responsible for the outcome.
func finish(err error) error {
	if errors.Is(err, ErrSuperseded) {
		return nil
	}
	return err
}

// Pipeline runs the conveyor for one job. Every stage writes a step row and a
// context entry, so a finished job carries its own explanation of how the game
// came to look the way it does.
type Pipeline struct {
	Store       Store
	Projects    projects.Store
	Chat        ai.Chat
	MaxRepairs  int
	StepTimeout time.Duration
	NewID       func() (string, error)
}

// Result is the job payload the client polls for.
type Result struct {
	ProjectID    string            `json:"project_id"`
	RevisionID   string            `json:"revision_id,omitempty"`
	Title        string            `json:"title"`
	Mode         string            `json:"mode"`
	Genre        string            `json:"genre"`
	Summary      string            `json:"summary"`
	Stats        gamespec.Stats    `json:"stats"`
	Iterations   int               `json:"iterations"`
	Degraded     bool              `json:"degraded"`
	Notes        []string          `json:"notes,omitempty"`
	OpenDefects  []gamespec.Defect `json:"open_defects,omitempty"`
	ReviewScore  int               `json:"review_score,omitempty"`
	AuditVerdict string            `json:"audit_verdict,omitempty"`
}

type run struct {
	pipeline *Pipeline
	job      Job
	notes    []string
	degraded bool
}

func (p Pipeline) Run(ctx context.Context, job Job) error {
	r := &run{pipeline: &p, job: job}
	result, err := r.execute(ctx)
	// The closing write must land even when the run ended because the context
	// was cancelled, so it gets a short context of its own.
	finishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if err != nil {
		if errors.Is(err, ErrSuperseded) {
			// Another worker owns this job; it will report the outcome.
			slog.Warn("pipeline stopped, job taken over by another worker", "job", job.ID)
			return nil
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			// Leave the job claimable: the lease expires and another worker
			// resumes the queue rather than reporting a failure the user did
			// not cause.
			slog.Warn("pipeline interrupted", "job", job.ID, "error", err)
			return nil
		}
		code, message := classify(err)
		if errors.Is(err, ErrCancelled) {
			return finish(p.Store.FinishJob(finishCtx, job.ID, job.RunnerID, "cancelled", nil, nil, code, message))
		}
		slog.Error("pipeline failed", "job", job.ID, "error", err)
		return finish(p.Store.FinishJob(finishCtx, job.ID, job.RunnerID, "failed", nil, nil, code, message))
	}
	payload, marshalErr := json.Marshal(result)
	if marshalErr != nil {
		return finish(p.Store.FinishJob(finishCtx, job.ID, job.RunnerID, "failed", nil, nil, "INTERNAL", "не удалось сохранить результат"))
	}
	projectID := result.ProjectID
	return finish(p.Store.FinishJob(finishCtx, job.ID, job.RunnerID, "succeeded", &projectID, payload, "", ""))
}

func (r *run) execute(ctx context.Context) (Result, error) {
	if r.pipeline.Chat == nil || !r.pipeline.Chat.Configured() {
		return Result{}, ai.ErrUnavailable
	}
	if r.job.RunnerID == "" {
		return Result{}, errors.New("job was handed to the pipeline without a claim")
	}
	memory, err := r.memory(ctx)
	if err != nil {
		return Result{}, err
	}
	if err := r.saveContext(ctx, "user_prompt", map[string]any{
		"prompt": r.job.Prompt, "mode": r.job.Mode, "kind": r.job.Kind,
	}); err != nil {
		return Result{}, err
	}

	blueprint, err := r.architect(ctx, memory)
	if err != nil {
		return Result{}, err
	}
	design, err := r.designer(ctx, memory, blueprint)
	if err != nil {
		return Result{}, err
	}

	var (
		spec       gamespec.Spec
		compiled   gamespec.Compiled
		testReport testReport
		review     reviewReport
		defects    []gamespec.Defect
		iterations int
	)
	attempts := r.pipeline.MaxRepairs + 1
	if attempts < 1 {
		attempts = 1
	}
	for iteration := 1; iteration <= attempts; iteration++ {
		iterations = iteration
		if err := r.checkCancel(ctx); err != nil {
			return Result{}, err
		}
		last := iteration == attempts

		spec, err = r.developer(ctx, memory, blueprint, design, defects, iteration)
		if err != nil {
			return Result{}, err
		}
		spec = gamespec.Normalize(spec)
		automatic := gamespec.Validate(spec)
		if fatal := gamespec.Fatal(automatic); len(fatal) > 0 && !last {
			defects = automatic
			r.note(fmt.Sprintf("итерация %d: автопроверка нашла %d критических дефектов, вернул разработчику", iteration, len(fatal)))
			continue
		}
		if len(gamespec.Fatal(automatic)) > 0 {
			// Out of attempts: build the missing pieces rather than ship a
			// level the player cannot finish.
			spec = gamespec.Repair(spec)
			automatic = gamespec.Validate(spec)
			r.degraded = true
			r.note("после последней итерации сработал детерминированный ремонт уровней")
		}
		compiled, err = r.compile(ctx, spec, automatic, iteration)
		if err != nil {
			return Result{}, err
		}

		testReport, err = r.tester(ctx, spec, compiled, automatic, iteration)
		if err != nil {
			return Result{}, err
		}
		if testReport.failed() && !last {
			defects = append(automatic, testReport.Defects...)
			r.note(fmt.Sprintf("итерация %d: тестировщик вернул игру на доработку", iteration))
			continue
		}

		review, err = r.reviewer(ctx, memory, blueprint, design, spec, compiled, testReport, iteration)
		if err != nil {
			return Result{}, err
		}
		if review.Verdict == "revise" && !last {
			defects = append(automatic, review.defects()...)
			r.note(fmt.Sprintf("итерация %d: ревьюер потребовал правок", iteration))
			continue
		}
		defects = automatic
		break
	}

	audit, err := r.auditor(ctx, spec, compiled, testReport, review)
	if err != nil {
		return Result{}, err
	}
	if audit.Verdict == "rejected" && len(audit.PolicyViolations) > 0 {
		return Result{}, &policyError{reasons: audit.PolicyViolations}
	}

	if err := r.pipeline.Store.SetStage(ctx, r.job.ID, r.job.RunnerID, "publishing", 95); err != nil {
		return Result{}, err
	}
	projectID, revisionID, err := r.publish(ctx, compiled)
	if err != nil {
		return Result{}, err
	}
	if err := r.saveProjectContext(ctx, projectID, spec, compiled); err != nil {
		return Result{}, err
	}
	return Result{
		ProjectID: projectID, RevisionID: revisionID,
		Title: compiled.Title, Mode: compiled.Mode, Genre: spec.Genre,
		Summary:     pickSummary(audit.Summary, spec.Summary),
		Stats:       compiled.Stats,
		Iterations:  iterations,
		Degraded:    r.degraded,
		Notes:       r.notes,
		OpenDefects: nonFatal(defects),
		ReviewScore: review.Score, AuditVerdict: audit.Verdict,
	}, nil
}

// ---- stages -------------------------------------------------------------

func (r *run) architect(ctx context.Context, memory string) (json.RawMessage, error) {
	prompt := section("Запрос пользователя", r.job.Prompt) +
		section("Требуемый режим", r.job.Mode) +
		memory
	raw, err := r.step(ctx, Architect, prompt, 1, "architect", 10)
	if err != nil {
		return nil, err
	}
	if err := r.saveContextRaw(ctx, "blueprint", raw); err != nil {
		return nil, err
	}
	return raw, nil
}

func (r *run) designer(ctx context.Context, memory string, blueprint json.RawMessage) (json.RawMessage, error) {
	prompt := section("Запрос пользователя", r.job.Prompt) +
		section("Архитектура", string(blueprint)) +
		memory
	raw, err := r.step(ctx, GameDesigner, prompt, 1, "game_designer", 25)
	if err != nil {
		return nil, err
	}
	if err := r.saveContextRaw(ctx, "design", raw); err != nil {
		return nil, err
	}
	return raw, nil
}

func (r *run) developer(ctx context.Context, memory string, blueprint, design json.RawMessage, defects []gamespec.Defect, iteration int) (gamespec.Spec, error) {
	prompt := section("Запрос пользователя", r.job.Prompt) +
		section("Архитектура", string(blueprint)) +
		section("Дизайн", string(design)) +
		memory
	if len(defects) > 0 {
		prompt += section("Дефекты предыдущей версии — исправь их все", formatDefects(defects))
	}
	raw, err := r.step(ctx, Developer, prompt, iteration, "developer", 45)
	if err != nil && (errors.Is(err, ai.ErrProvider) || errors.Is(err, ai.ErrFailed)) {
		// The level spec is the longest generation in the conveyor, and a
		// gateway will drop a long reasoning request before it finishes. A
		// second, smaller attempt costs far less than losing the design.
		lighter := Developer
		lighter.Effort, lighter.MaxTokens = "low", 8000
		r.note("разработчик не ответил, повторяю запрос в облегчённом режиме")
		raw, err = r.step(ctx, lighter, prompt+"\n\nОтвечай короче: не больше двух уровней и по 8 объектов на уровень.",
			iteration, "developer", 48)
	}
	if err != nil {
		// A dead provider must not cost the user the whole request: fall back
		// to the deterministic template, clearly marked as degraded.
		if errors.Is(err, ai.ErrProvider) || errors.Is(err, ai.ErrFailed) {
			r.degraded = true
			r.note("разработчик недоступен, собран шаблонный уровень")
			return r.templateSpec(blueprint), nil
		}
		return gamespec.Spec{}, err
	}
	var spec gamespec.Spec
	if err := json.Unmarshal(raw, &spec); err != nil {
		r.degraded = true
		r.note("разработчик вернул неразбираемую спецификацию, собран шаблонный уровень")
		return r.templateSpec(blueprint), nil
	}
	if spec.Mode == "" {
		spec.Mode = r.job.Mode
	}
	if err := r.saveContextRaw(ctx, "spec", raw); err != nil {
		return gamespec.Spec{}, err
	}
	return spec, nil
}

func (r *run) templateSpec(blueprint json.RawMessage) gamespec.Spec {
	var plan struct {
		Title  string `json:"title"`
		Genre  string `json:"genre"`
		Levels int    `json:"levels_planned"`
	}
	_ = json.Unmarshal(blueprint, &plan)
	title := plan.Title
	if title == "" {
		title = firstWords(r.job.Prompt, 6)
	}
	return gamespec.Template(title, r.job.Mode, plan.Genre, plan.Levels)
}

// compile is the deterministic stage: no model, no judgement, just the
// document and the proof that it validates.
func (r *run) compile(ctx context.Context, spec gamespec.Spec, defects []gamespec.Defect, iteration int) (gamespec.Compiled, error) {
	started := time.Now()
	stepID, err := r.beginStep(ctx, "compiler", iteration, 55)
	if err != nil {
		return gamespec.Compiled{}, err
	}
	projectID := ""
	if r.job.ProjectID != nil {
		projectID = *r.job.ProjectID
	}
	compiled, err := gamespec.CompileWithID(spec, projectID, r.pipeline.NewID)
	if err == nil {
		_, _, err = projects.CanonicalManifest(compiled.Document)
	}
	if err != nil {
		// The compiler only fails on a spec that survived normalisation and
		// still breaks the schema, which means the fallback is the right answer.
		repaired := gamespec.Repair(spec)
		compiled, err = gamespec.CompileWithID(repaired, projectID, r.pipeline.NewID)
		if err == nil {
			_, _, err = projects.CanonicalManifest(compiled.Document)
		}
		if err != nil {
			_ = r.pipeline.Store.FinishStep(ctx, stepID, "failed", "", "", 0, 0, nil, err.Error(), time.Since(started))
			return gamespec.Compiled{}, fmt.Errorf("compile: %w", err)
		}
		r.degraded = true
		r.note("документ проекта пересобран после ремонта спецификации")
	}
	output, _ := json.Marshal(map[string]any{
		"stats": compiled.Stats, "defects": defects,
		"entry_scene_id": compiled.EntrySceneID,
	})
	return compiled, r.pipeline.Store.FinishStep(ctx, stepID, "succeeded", "internal", "compiler", 0, 0, output, "", time.Since(started))
}

type testReport struct {
	Verdict     string            `json:"verdict"`
	Defects     []gamespec.Defect `json:"defects"`
	Playthrough string            `json:"playthrough"`
	Notes       string            `json:"notes"`
}

func (t testReport) failed() bool {
	if t.Verdict == "fail" {
		return true
	}
	return len(gamespec.Fatal(t.Defects)) > 0
}

func (r *run) tester(ctx context.Context, spec gamespec.Spec, compiled gamespec.Compiled, automatic []gamespec.Defect, iteration int) (testReport, error) {
	prompt := section("Запрос пользователя", r.job.Prompt) +
		section("Спецификация уровней", compactSpec(spec)) +
		section("Отчёт автоматических проверок", formatDefects(automatic)) +
		section("Собранная сцена", statsLine(compiled))
	raw, err := r.step(ctx, Tester, prompt, iteration, "tester", 70)
	if err != nil {
		// Losing the judgement of the tester is not fatal: the deterministic
		// checks already ran and they are the authoritative ones.
		r.note("тестировщик недоступен, использую только автоматические проверки")
		return testReport{Verdict: verdictFor(automatic), Defects: automatic}, nil
	}
	var report testReport
	if err := json.Unmarshal(raw, &report); err != nil {
		return testReport{Verdict: verdictFor(automatic), Defects: automatic}, nil
	}
	report.Defects = append(report.Defects, automatic...)
	return report, r.saveContextRaw(ctx, "test_report", raw)
}

type reviewReport struct {
	Verdict  string `json:"verdict"`
	Score    int    `json:"score"`
	Matches  bool   `json:"matches_request"`
	Findings []struct {
		Severity string `json:"severity"`
		Message  string `json:"message"`
		Level    int    `json:"level"`
	} `json:"findings"`
	Improvements []string `json:"improvements"`
}

func (v reviewReport) defects() []gamespec.Defect {
	out := make([]gamespec.Defect, 0, len(v.Findings)+len(v.Improvements))
	for _, finding := range v.Findings {
		out = append(out, gamespec.Defect{
			Code: "REVIEW_" + strings.ToUpper(finding.Severity), Level: finding.Level,
			Message: finding.Message, Fatal: finding.Severity == "high",
		})
	}
	for _, improvement := range v.Improvements {
		out = append(out, gamespec.Defect{Code: "REVIEW_IMPROVEMENT", Message: improvement})
	}
	return out
}

func (r *run) reviewer(ctx context.Context, memory string, blueprint, design json.RawMessage, spec gamespec.Spec, compiled gamespec.Compiled, test testReport, iteration int) (reviewReport, error) {
	testJSON, _ := json.Marshal(test)
	prompt := section("Запрос пользователя", r.job.Prompt) +
		section("Архитектура", string(blueprint)) +
		section("Дизайн", string(design)) +
		section("Что собрано", compactSpec(spec)) +
		section("Отчёт тестировщика", string(testJSON)) +
		section("Бюджеты сцены", statsLine(compiled)) +
		memory
	raw, err := r.step(ctx, Reviewer, prompt, iteration, "reviewer", 82)
	if err != nil {
		r.note("ревьюер недоступен, пропускаю этап")
		return reviewReport{Verdict: "approve", Score: 0}, nil
	}
	var report reviewReport
	if err := json.Unmarshal(raw, &report); err != nil {
		return reviewReport{Verdict: "approve"}, nil
	}
	return report, r.saveContextRaw(ctx, "review", raw)
}

type auditReport struct {
	Verdict          string   `json:"verdict"`
	PolicyViolations []string `json:"policy_violations"`
	Unresolved       []string `json:"unresolved"`
	Summary          string   `json:"summary"`
}

func (r *run) auditor(ctx context.Context, spec gamespec.Spec, compiled gamespec.Compiled, test testReport, review reviewReport) (auditReport, error) {
	// The deterministic policy screen runs first and cannot be talked out of
	// its verdict by the model.
	if violations := policyViolations(spec); len(violations) > 0 {
		return auditReport{Verdict: "rejected", PolicyViolations: violations}, nil
	}
	testJSON, _ := json.Marshal(test)
	reviewJSON, _ := json.Marshal(review)
	prompt := section("Запрос пользователя", r.job.Prompt) +
		section("Что собрано", compactSpec(spec)) +
		section("Бюджеты сцены", statsLine(compiled)) +
		section("Отчёт тестировщика", string(testJSON)) +
		section("Отчёт ревьюера", string(reviewJSON))
	raw, err := r.step(ctx, Auditor, prompt, 1, "auditor", 90)
	if err != nil {
		r.note("ревизор недоступен, публикую по результатам автоматических проверок")
		return auditReport{Verdict: "approved", Summary: spec.Summary}, nil
	}
	var report auditReport
	if err := json.Unmarshal(raw, &report); err != nil {
		return auditReport{Verdict: "approved", Summary: spec.Summary}, nil
	}
	return report, r.saveContextRaw(ctx, "audit", raw)
}

// publish stores the finished document, either as a new project or as a new
// revision of the project the job was started from.
func (r *run) publish(ctx context.Context, compiled gamespec.Compiled) (string, string, error) {
	key := "job:" + r.job.ID
	hash := requestHash(key + compiled.ProjectID)
	if r.job.Kind == "improve_game" && r.job.ProjectID != nil {
		project, err := r.pipeline.Projects.ReplaceDocument(ctx, r.job.OwnerID, *r.job.ProjectID, key, hash, compiled.Document)
		if err != nil {
			return "", "", err
		}
		return project.ID, project.HeadRevisionID, nil
	}
	project, err := r.pipeline.Projects.Create(ctx, r.job.OwnerID, key, hash, projects.CreateRequest{
		Title:         compiled.Title,
		Mode:          compiled.Mode,
		SchemaVersion: 1,
		Manifest:      compiled.Document,
	})
	if err != nil {
		return "", "", err
	}
	return project.ID, project.HeadRevisionID, nil
}

// ---- plumbing -----------------------------------------------------------

func (r *run) step(ctx context.Context, agent Agent, prompt string, iteration int, stage string, progress int) (json.RawMessage, error) {
	if err := r.checkCancel(ctx); err != nil {
		return nil, err
	}
	stepID, err := r.beginStep(ctx, agent.Name, iteration, progress)
	if err != nil {
		return nil, err
	}
	started := time.Now()
	timeout := r.pipeline.StepTimeout
	if timeout <= 0 {
		timeout = 330 * time.Second
	}
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	response, err := r.pipeline.Chat.Chat(callCtx, ai.ChatRequest{
		System: agent.System, User: prompt, JSON: true,
		MaxTokens: agent.MaxTokens, Effort: agent.Effort,
	})
	if err != nil {
		_ = r.pipeline.Store.FinishStep(ctx, stepID, "failed", "", "", 0, 0, nil, err.Error(), time.Since(started))
		return nil, err
	}
	raw, ok := ai.ExtractJSON(response.Text)
	if !ok {
		_ = r.pipeline.Store.FinishStep(ctx, stepID, "failed", response.Provider, response.Model,
			response.InputTokens, response.OutputTokens, nil, "ответ не содержит JSON", time.Since(started))
		return nil, ai.ErrFailed
	}
	return raw, r.pipeline.Store.FinishStep(ctx, stepID, "succeeded", response.Provider, response.Model,
		response.InputTokens, response.OutputTokens, raw, "", time.Since(started))
}

func (r *run) beginStep(ctx context.Context, agent string, iteration, progress int) (string, error) {
	if err := r.pipeline.Store.SetStage(ctx, r.job.ID, r.job.RunnerID, agent, progress); err != nil {
		return "", err
	}
	id, err := r.pipeline.NewID()
	if err != nil {
		return "", err
	}
	return id, r.pipeline.Store.StartStep(ctx, id, r.job.ID, agent, iteration)
}

// checkCancel renews the lease and, in doing so, confirms this worker still
// owns the job. It runs before every stage, so a worker that lost the job
// stops after at most one wasted stage instead of running the whole conveyor
// beside the new owner.
func (r *run) checkCancel(ctx context.Context) error {
	cancelRequested, err := r.pipeline.Store.Heartbeat(ctx, r.job.ID, r.job.RunnerID, 5*time.Minute)
	if err != nil {
		return err
	}
	if cancelRequested {
		return ErrCancelled
	}
	return ctx.Err()
}

func (r *run) note(text string) { r.notes = append(r.notes, text) }

func (r *run) saveContext(ctx context.Context, kind string, content any) error {
	raw, err := json.Marshal(content)
	if err != nil {
		return err
	}
	return r.saveContextRaw(ctx, kind, raw)
}

func (r *run) saveContextRaw(ctx context.Context, kind string, content json.RawMessage) error {
	id, err := r.pipeline.NewID()
	if err != nil {
		return err
	}
	jobID := r.job.ID
	return r.pipeline.Store.PutContext(ctx, id, r.job.OwnerID, r.job.ProjectID, &jobID, kind, content)
}

// saveProjectContext re-files the job's memory under the finished project, so
// the next request about this game starts from what was decided here.
func (r *run) saveProjectContext(ctx context.Context, projectID string, spec gamespec.Spec, compiled gamespec.Compiled) error {
	entries, err := r.pipeline.Store.JobContext(ctx, r.job.ID)
	if err != nil {
		return err
	}
	jobID := r.job.ID
	for _, entry := range entries {
		if entry.ProjectID != nil {
			continue
		}
		id, err := r.pipeline.NewID()
		if err != nil {
			return err
		}
		if err := r.pipeline.Store.PutContext(ctx, id, r.job.OwnerID, &projectID, &jobID, entry.Kind, entry.Content); err != nil {
			return err
		}
	}
	summary, err := json.Marshal(map[string]any{
		"title": compiled.Title, "mode": compiled.Mode, "genre": spec.Genre,
		"summary": spec.Summary, "levels": len(spec.Levels), "stats": compiled.Stats,
		"player": spec.Player, "palette": spec.Palette,
	})
	if err != nil {
		return err
	}
	id, err := r.pipeline.NewID()
	if err != nil {
		return err
	}
	return r.pipeline.Store.PutContext(ctx, id, r.job.OwnerID, &projectID, &jobID, "summary", summary)
}

// memory renders what the conveyor already knows about this project into the
// prompt. For a brand new game it is empty; for a change request it carries the
// design decisions of every earlier run.
func (r *run) memory(ctx context.Context) (string, error) {
	if r.job.ProjectID == nil {
		return "", nil
	}
	entries, err := r.pipeline.Store.ProjectContext(ctx, r.job.OwnerID, *r.job.ProjectID)
	if err != nil {
		return "", err
	}
	if len(entries) == 0 {
		return "", nil
	}
	var b strings.Builder
	b.WriteString("\n=== Память проекта (решения прошлых запусков) ===\n")
	budget := 12000
	for _, entry := range entries {
		block := entry.Kind + ": " + string(entry.Content) + "\n"
		if len(block) > budget {
			break
		}
		budget -= len(block)
		b.WriteString(block)
	}
	return b.String(), nil
}
