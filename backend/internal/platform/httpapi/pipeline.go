package httpapi

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/digkill/gamedev/backend/internal/agents"
	"github.com/digkill/gamedev/backend/internal/projects"
)

func (a API) pipelineRoutes(mux *http.ServeMux) {
	if a.Worker == nil {
		return
	}
	mux.HandleFunc("POST /api/v1/games", a.authorize(a.createGame))
	mux.HandleFunc("POST /api/v1/projects/{id}/ai/pipeline", a.authorize(a.improveGame))
	mux.HandleFunc("GET /api/v1/ai/jobs", a.authorize(a.listJobs))
	mux.HandleFunc("GET /api/v1/ai/jobs/{job_id}", a.authorize(a.getJob))
	mux.HandleFunc("POST /api/v1/ai/jobs/{job_id}/cancel", a.authorize(a.cancelJob))
	mux.HandleFunc("GET /api/v1/projects/{id}/ai/context", a.authorize(a.projectContext))
}

type createGameRequest struct {
	Prompt string `json:"prompt"`
	Mode   string `json:"mode"`
	Title  string `json:"title"`
}

type improveGameRequest struct {
	Prompt         string `json:"prompt"`
	BaseRevisionID string `json:"base_revision_id"`
}

// createGame starts the full conveyor: architect, designer, developer,
// compiler, tester, reviewer, auditor. It answers immediately with a job the
// client polls, because the run takes minutes.
func (a API) createGame(w http.ResponseWriter, r *http.Request) {
	key, ok := idempotencyKey(w, r)
	if !ok {
		return
	}
	var req createGameRequest
	if !readJSON(w, r, &req) {
		return
	}
	prompt := strings.TrimSpace(req.Prompt)
	if n := utf8.RuneCountInString(prompt); n < 4 || n > 10000 {
		fail(w, r, 422, "VALIDATION_FAILED", "Опишите игру: от 4 до 10000 символов", false, nil)
		return
	}
	mode := strings.TrimSpace(req.Mode)
	if mode == "" {
		mode = "2d"
	}
	if mode != "2d" && mode != "3d" {
		fail(w, r, 422, "VALIDATION_FAILED", "mode должен быть 2d или 3d", false, nil)
		return
	}
	var title *string
	if trimmed := strings.TrimSpace(req.Title); trimmed != "" {
		if utf8.RuneCountInString(trimmed) > 120 {
			fail(w, r, 422, "VALIDATION_FAILED", "Название не длиннее 120 символов", false, nil)
			return
		}
		title = &trimmed
	}
	a.queue(w, r, agents.NewJob{
		OwnerID: actor(r), Kind: "create_game", Prompt: prompt, Mode: mode,
		Title: title, IdempotencyKey: key,
	})
}

// improveGame reruns the conveyor over an existing project. The stored context
// of the earlier runs goes into the prompts, so the game keeps its identity.
func (a API) improveGame(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !projects.IsUUID(id) {
		fail(w, r, 404, "NOT_FOUND", "Проект не найден", false, nil)
		return
	}
	key, ok := idempotencyKey(w, r)
	if !ok {
		return
	}
	var req improveGameRequest
	if !readJSON(w, r, &req) {
		return
	}
	prompt := strings.TrimSpace(req.Prompt)
	if n := utf8.RuneCountInString(prompt); n < 4 || n > 10000 {
		fail(w, r, 422, "VALIDATION_FAILED", "Опишите изменения: от 4 до 10000 символов", false, nil)
		return
	}
	project, err := a.Projects.Get(r.Context(), actor(r), id)
	if err != nil {
		projectError(w, r, err)
		return
	}
	if req.BaseRevisionID != "" && req.BaseRevisionID != project.HeadRevisionID {
		projectError(w, r, &projects.ConflictError{CurrentRevisionID: project.HeadRevisionID})
		return
	}
	head := project.HeadRevisionID
	a.queue(w, r, agents.NewJob{
		OwnerID: actor(r), Kind: "improve_game", ProjectID: &project.ID,
		BaseRevisionID: &head, Prompt: prompt, Mode: project.Mode, IdempotencyKey: key,
	})
}

func (a API) queue(w http.ResponseWriter, r *http.Request, req agents.NewJob) {
	if a.AI == nil || !a.AI.Configured() {
		fail(w, r, 503, "AI_UNAVAILABLE", "Модель не настроена на сервере", true, nil)
		return
	}
	id, err := projects.NewUUID()
	if err != nil {
		fail(w, r, 503, "DEPENDENCY_UNAVAILABLE", "Временная ошибка сервера", true, nil)
		return
	}
	job, err := a.Agents.CreateJob(r.Context(), id, req)
	if err != nil {
		jobError(w, r, err)
		return
	}
	a.Worker.Wake()
	w.Header().Set("Location", "/api/v1/ai/jobs/"+job.ID)
	writeJSON(w, 202, jobView(job))
}

func (a API) listJobs(w http.ResponseWriter, r *http.Request) {
	limit := 20
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 100 {
			fail(w, r, 422, "VALIDATION_FAILED", "limit должен быть от 1 до 100", false, nil)
			return
		}
		limit = n
	}
	items, err := a.Agents.ListJobs(r.Context(), actor(r), limit)
	if err != nil {
		jobError(w, r, err)
		return
	}
	views := make([]map[string]any, 0, len(items))
	for _, job := range items {
		views = append(views, jobView(job))
	}
	writeJSON(w, 200, map[string]any{"items": views})
}

func (a API) getJob(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("job_id")
	if !projects.IsUUID(id) {
		fail(w, r, 404, "NOT_FOUND", "Задание не найдено", false, nil)
		return
	}
	job, err := a.Agents.Job(r.Context(), actor(r), id)
	if err != nil {
		jobError(w, r, err)
		return
	}
	view := jobView(job)
	view["steps"] = job.Steps
	writeJSON(w, 200, view)
}

func (a API) cancelJob(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("job_id")
	if !projects.IsUUID(id) {
		fail(w, r, 404, "NOT_FOUND", "Задание не найдено", false, nil)
		return
	}
	if err := a.Agents.RequestCancel(r.Context(), actor(r), id); err != nil {
		jobError(w, r, err)
		return
	}
	w.WriteHeader(202)
}

// projectContext exposes what the conveyor remembers about a project. It is
// the same material the agents read, so a user can see why the game looks the
// way it does.
func (a API) projectContext(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !projects.IsUUID(id) {
		fail(w, r, 404, "NOT_FOUND", "Проект не найден", false, nil)
		return
	}
	if _, err := a.Projects.Get(r.Context(), actor(r), id); err != nil {
		projectError(w, r, err)
		return
	}
	entries, err := a.Agents.ProjectContext(r.Context(), actor(r), id)
	if err != nil {
		jobError(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"items": entries})
}

// jobView adds the human-readable stage name to the stored job.
func jobView(job agents.Job) map[string]any {
	return map[string]any{
		"id":               job.ID,
		"kind":             job.Kind,
		"status":           job.Status,
		"stage":            job.Stage,
		"stage_title":      stageTitle(job.Stage),
		"progress":         job.Progress,
		"mode":             job.Mode,
		"prompt":           job.Prompt,
		"project_id":       job.ProjectID,
		"result":           job.Result,
		"error_code":       job.ErrorCode,
		"error_message":    job.ErrorMessage,
		"cancel_requested": job.CancelRequested,
		"created_at":       job.CreatedAt,
		"updated_at":       job.UpdatedAt,
		"started_at":       job.StartedAt,
		"finished_at":      job.FinishedAt,
	}
}

func stageTitle(stage string) string {
	switch stage {
	case "queued":
		return "В очереди"
	case "compiler":
		return "Сборка проекта"
	case "publishing":
		return "Сохранение"
	case "done":
		return "Готово"
	}
	for _, agent := range agents.Order {
		if agent.Name == stage {
			return agent.Title
		}
	}
	return stage
}

func jobError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, agents.ErrNotFound):
		fail(w, r, 404, "NOT_FOUND", "Задание не найдено", false, nil)
	case errors.Is(err, agents.ErrBusy):
		fail(w, r, 429, "AI_QUOTA", "Уже выполняются две генерации, дождитесь их завершения", true, nil)
	case errors.Is(err, agents.ErrConflict):
		fail(w, r, 409, "IDEMPOTENCY_CONFLICT", "Ключ уже использован для другого запроса", false, nil)
	default:
		fail(w, r, 503, "DEPENDENCY_UNAVAILABLE", "Временная ошибка сервера", true, nil)
	}
}
