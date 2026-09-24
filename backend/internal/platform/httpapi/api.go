package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/digkill/gamedev/backend/internal/agents"
	"github.com/digkill/gamedev/backend/internal/ai"
	"github.com/digkill/gamedev/backend/internal/auth"
	"github.com/digkill/gamedev/backend/internal/projects"
	"github.com/jackc/pgx/v5/pgxpool"
)

type API struct {
	DB       *pgxpool.Pool
	Projects projects.Store
	AI       aiProvider
	// Auth is nil when no identity backend is wired; the auth routes are then
	// not registered and the bootstrap token remains the only way in.
	Auth   *auth.Service
	Agents agents.Store
	// Worker is nil when the agent conveyor is disabled.
	Worker *agents.Worker
	// TrustProxyHeaders enables X-Forwarded-For for rate limiting. Set it only
	// when the API is reachable exclusively through a proxy that rewrites it.
	TrustProxyHeaders bool
}

type aiProvider interface {
	Configured() bool
	Generate(ctx context.Context, req ai.Request) (ai.Result, error)
}

type errorBody struct {
	Error struct {
		Code      string `json:"code"`
		Message   string `json:"message"`
		RequestID string `json:"request_id"`
		Retryable bool   `json:"retryable"`
		Details   any    `json:"details,omitempty"`
	} `json:"error"`
}

func (a API) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", root)
	mux.HandleFunc("GET /health/live", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, map[string]string{"status": "ok"}) })
	mux.HandleFunc("GET /health/ready", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := a.DB.Ping(ctx); err != nil {
			fail(w, r, 503, "DEPENDENCY_UNAVAILABLE", "База данных недоступна", true, nil)
			return
		}
		writeJSON(w, 200, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("POST /api/v1/projects", a.authorize(a.create))
	mux.HandleFunc("GET /api/v1/projects", a.authorize(a.list))
	mux.HandleFunc("GET /api/v1/projects/{id}", a.authorize(a.get))
	mux.HandleFunc("PATCH /api/v1/projects/{id}", a.authorize(a.patch))
	mux.HandleFunc("DELETE /api/v1/projects/{id}", a.authorize(a.remove))
	mux.HandleFunc("POST /api/v1/projects/{id}/restore", a.authorize(a.restore))
	mux.HandleFunc("GET /api/v1/projects/{id}/revisions", a.authorize(a.listRevisions))
	mux.HandleFunc("GET /api/v1/projects/{id}/revisions/{revision_id}", a.authorize(a.getRevision))
	mux.HandleFunc("POST /api/v1/projects/{id}/changes", a.authorize(a.change))
	mux.HandleFunc("POST /api/v1/projects/{id}/ai/generations", a.authorize(a.generate))
	a.authRoutes(mux)
	a.pipelineRoutes(mux)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, err := projects.NewUUID()
		if err != nil {
			http.Error(w, "internal error", 500)
			return
		}
		ctx := context.WithValue(r.Context(), requestIDKey{}, "req_"+id)
		ctx = context.WithValue(ctx, trustProxyKey{}, a.TrustProxyHeaders)
		r = r.WithContext(ctx)
		w.Header().Set("X-Request-ID", "req_"+id)
		mux.ServeHTTP(w, r)
	})
}

type requestIDKey struct{}
type actorKey struct{}
type bearerKey struct{}
type trustProxyKey struct{}

func (a API) authorize(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		parts := strings.SplitN(r.Header.Get("Authorization"), " ", 2)
		if len(parts) != 2 || parts[0] != "Bearer" || strings.TrimSpace(parts[1]) == "" {
			fail(w, r, 401, "UNAUTHORIZED", "Требуется вход", false, nil)
			return
		}
		actor, err := a.Projects.Actor(r.Context(), parts[1])
		if errors.Is(err, projects.ErrNotFound) {
			fail(w, r, 401, "UNAUTHORIZED", "Сессия недействительна", false, nil)
			return
		}
		if err != nil {
			fail(w, r, 503, "DEPENDENCY_UNAVAILABLE", "Временная ошибка базы данных", true, nil)
			return
		}
		ctx := context.WithValue(r.Context(), actorKey{}, actor)
		ctx = context.WithValue(ctx, bearerKey{}, parts[1])
		next(w, r.WithContext(ctx))
	}
}

func actor(r *http.Request) string { return r.Context().Value(actorKey{}).(string) }

// bearer returns the presented access token so logout can revoke it.
func bearer(r *http.Request) string {
	token, _ := r.Context().Value(bearerKey{}).(string)
	return token
}

func (a API) create(w http.ResponseWriter, r *http.Request) {
	key, ok := idempotencyKey(w, r)
	if !ok {
		return
	}
	body, ok := bodyBytes(w, r)
	if !ok {
		return
	}
	var req projects.CreateRequest
	if !decodeStrict(body, &req) {
		fail(w, r, 422, "VALIDATION_FAILED", "Некорректное тело запроса", false, nil)
		return
	}
	p, err := a.Projects.Create(r.Context(), actor(r), key, sha256.Sum256(body), req)
	if err != nil {
		projectError(w, r, err)
		return
	}
	w.Header().Set("Location", "/api/v1/projects/"+p.ID)
	writeJSON(w, 201, p)
}

func (a API) list(w http.ResponseWriter, r *http.Request) {
	limit, cursor, ok := listParams(w, r)
	if !ok {
		return
	}
	items, next, err := a.Projects.List(r.Context(), actor(r), cursor, limit)
	if err != nil {
		projectError(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"items": items, "next_cursor": next})
}

func (a API) get(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !projects.IsUUID(id) {
		fail(w, r, 404, "NOT_FOUND", "Проект не найден", false, nil)
		return
	}
	p, err := a.Projects.Get(r.Context(), actor(r), id)
	if err != nil {
		projectError(w, r, err)
		return
	}
	writeJSON(w, 200, p)
}

func (a API) patch(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !projects.IsUUID(id) {
		fail(w, r, 404, "NOT_FOUND", "Проект не найден", false, nil)
		return
	}
	key, ok := idempotencyKey(w, r)
	if !ok {
		return
	}
	body, ok := bodyBytes(w, r)
	if !ok {
		return
	}
	var req projects.MetadataUpdate
	if !decodeStrict(body, &req) {
		fail(w, r, 422, "VALIDATION_FAILED", "Некорректное тело запроса", false, nil)
		return
	}
	p, err := a.Projects.UpdateTitle(r.Context(), actor(r), id, key, sha256.Sum256(body), req)
	if err != nil {
		projectError(w, r, err)
		return
	}
	writeJSON(w, 200, p)
}

func (a API) remove(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !projects.IsUUID(id) {
		fail(w, r, 404, "NOT_FOUND", "Проект не найден", false, nil)
		return
	}
	key, ok := idempotencyKey(w, r)
	if !ok {
		return
	}
	lock, ok := ifMatch(r)
	if !ok {
		fail(w, r, 422, "VALIDATION_FAILED", "Требуется If-Match с lock_version", false, nil)
		return
	}
	err := a.Projects.SoftDelete(r.Context(), actor(r), id, key, sha256.Sum256([]byte(key+"|"+r.Header.Get("If-Match"))), lock)
	if err != nil {
		projectError(w, r, err)
		return
	}
	w.WriteHeader(204)
}

func (a API) restore(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !projects.IsUUID(id) {
		fail(w, r, 404, "NOT_FOUND", "Проект не найден", false, nil)
		return
	}
	key, ok := idempotencyKey(w, r)
	if !ok {
		return
	}
	p, err := a.Projects.Restore(r.Context(), actor(r), id, key, sha256.Sum256([]byte(key)))
	if err != nil {
		projectError(w, r, err)
		return
	}
	writeJSON(w, 200, p)
}

func (a API) listRevisions(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !projects.IsUUID(id) {
		fail(w, r, 404, "NOT_FOUND", "Проект не найден", false, nil)
		return
	}
	limit, cursor, ok := listParams(w, r)
	if !ok {
		return
	}
	items, next, err := a.Projects.ListRevisions(r.Context(), actor(r), id, cursor, limit)
	if err != nil {
		projectError(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"items": items, "next_cursor": next})
}

func (a API) getRevision(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	revisionID := r.PathValue("revision_id")
	if !projects.IsUUID(id) || !projects.IsUUID(revisionID) {
		fail(w, r, 404, "NOT_FOUND", "Ревизия не найдена", false, nil)
		return
	}
	item, err := a.Projects.GetRevision(r.Context(), actor(r), id, revisionID)
	if err != nil {
		projectError(w, r, err)
		return
	}
	writeJSON(w, 200, item)
}

func (a API) change(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !projects.IsUUID(id) {
		fail(w, r, 404, "NOT_FOUND", "Проект не найден", false, nil)
		return
	}
	key, ok := idempotencyKey(w, r)
	if !ok {
		return
	}
	body, ok := bodyBytes(w, r)
	if !ok {
		return
	}
	var req projects.ChangeRequest
	if !decodeStrict(body, &req) {
		fail(w, r, 422, "VALIDATION_FAILED", "Некорректное тело запроса", false, nil)
		return
	}
	p, err := a.Projects.Change(r.Context(), actor(r), id, key, sha256.Sum256(body), req)
	if err != nil {
		projectError(w, r, err)
		return
	}
	writeJSON(w, 200, p)
}

func (a API) generate(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !projects.IsUUID(id) {
		fail(w, r, 404, "NOT_FOUND", "Проект не найден", false, nil)
		return
	}
	key, ok := idempotencyKey(w, r)
	if !ok {
		return
	}
	body, ok := bodyBytes(w, r)
	if !ok {
		return
	}
	var req projects.GenerateRequest
	if !decodeStrict(body, &req) || req.Validate() != nil {
		fail(w, r, 422, "VALIDATION_FAILED", "Некорректное тело запроса", false, nil)
		return
	}
	if a.AI == nil || !a.AI.Configured() {
		fail(w, r, 503, "AI_UNAVAILABLE", "AI временно недоступен", true, nil)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 150*time.Second)
	defer cancel()
	p, err := a.Projects.Get(ctx, actor(r), id)
	if err != nil {
		projectError(w, r, err)
		return
	}
	if p.HeadRevisionID != req.BaseRevisionID {
		projectError(w, r, &projects.ConflictError{CurrentRevisionID: p.HeadRevisionID})
		return
	}
	busy, err := a.Projects.HasActiveGeneration(ctx, actor(r), id)
	if err != nil {
		projectError(w, r, err)
		return
	}
	if busy {
		fail(w, r, 409, "AI_BUSY", "Уже выполняется генерация этого проекта", true, nil)
		return
	}
	ownerBusy, err := a.Projects.CountOwnerActive(ctx, actor(r))
	if err != nil {
		projectError(w, r, err)
		return
	}
	if ownerBusy >= 2 {
		fail(w, r, 429, "AI_QUOTA", "Слишком много активных генераций", true, nil)
		return
	}
	generationID, err := projects.NewUUID()
	if err != nil {
		projectError(w, r, err)
		return
	}
	if err := a.Projects.InsertGeneration(ctx, actor(r), id, generationID, req.BaseRevisionID, req.Prompt, "openai"); err != nil {
		projectError(w, r, err)
		return
	}
	result, err := a.AI.Generate(ctx, ai.Request{Prompt: req.Prompt, Mode: p.Mode, Context: ai.CompactContext(p.Manifest), Manifest: p.Manifest})
	if err != nil {
		_ = a.Projects.FinishGeneration(ctx, generationID, "failed", "", "AI_FAILED", nil)
		projectError(w, r, err)
		return
	}
	ops := make([]projects.Command, 0, len(result.Operations))
	for _, op := range result.Operations {
		ops = append(ops, projects.NormalizeCommand(op))
	}
	applied, err := a.Projects.ApplyAI(ctx, actor(r), id, key, sha256.Sum256(body), projects.ChangeRequest{
		BaseRevisionID: req.BaseRevisionID,
		Operations:     ops,
	})
	if err != nil {
		status := "failed"
		if errors.Is(err, projects.ErrConflict) {
			status = "conflict"
		}
		_ = a.Projects.FinishGeneration(ctx, generationID, status, result.Summary, "APPLY_FAILED", ops)
		projectError(w, r, err)
		return
	}
	_ = a.Projects.FinishGeneration(ctx, generationID, "applied", result.Summary, "", ops)
	writeJSON(w, 200, projects.GenerateResponse{
		GenerationID: generationID,
		Status:       "applied",
		Summary:      result.Summary,
		Project:      applied,
	})
}

func listParams(w http.ResponseWriter, r *http.Request) (int, string, bool) {
	if err := r.ParseForm(); err != nil {
		fail(w, r, 422, "VALIDATION_FAILED", "Некорректный запрос", false, nil)
		return 0, "", false
	}
	limit := 20
	if s := r.URL.Query().Get("limit"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 || n > 100 {
			fail(w, r, 422, "VALIDATION_FAILED", "limit должен быть от 1 до 100", false, nil)
			return 0, "", false
		}
		limit = n
	}
	cursor := r.URL.Query().Get("cursor")
	if cursor != "" && !projects.IsUUID(cursor) {
		fail(w, r, 422, "VALIDATION_FAILED", "Некорректный cursor", false, nil)
		return 0, "", false
	}
	return limit, cursor, true
}

func ifMatch(r *http.Request) (int64, bool) {
	raw := strings.Trim(strings.TrimSpace(r.Header.Get("If-Match")), "\"")
	n, err := strconv.ParseInt(raw, 10, 64)
	return n, err == nil && n >= 0
}

func idempotencyKey(w http.ResponseWriter, r *http.Request) (string, bool) {
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if len(key) < 1 || len(key) > 200 {
		fail(w, r, 422, "VALIDATION_FAILED", "Требуется Idempotency-Key длиной до 200 символов", false, nil)
		return "", false
	}
	return key, true
}

func bodyBytes(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		fail(w, r, 415, "UNSUPPORTED_MEDIA_TYPE", "Требуется application/json", false, nil)
		return nil, false
	}
	reader := http.MaxBytesReader(w, r.Body, 2<<20)
	defer reader.Close()
	data, err := io.ReadAll(reader)
	if err != nil {
		fail(w, r, 413, "PAYLOAD_TOO_LARGE", "Запрос слишком большой", false, nil)
		return nil, false
	}
	return data, true
}

func decodeStrict(data []byte, v any) bool {
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if decoder.Decode(v) != nil {
		return false
	}
	var extra any
	return errors.Is(decoder.Decode(&extra), io.EOF)
}

func projectError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, projects.ErrInvalid):
		fail(w, r, 422, "VALIDATION_FAILED", "Проект не прошёл проверку", false, nil)
	case errors.Is(err, projects.ErrNotFound):
		fail(w, r, 404, "NOT_FOUND", "Проект не найден", false, nil)
	case errors.Is(err, projects.ErrIdempotencyConflict):
		fail(w, r, 409, "IDEMPOTENCY_CONFLICT", "Ключ уже использован для другого запроса", false, nil)
	case errors.Is(err, projects.ErrAIUnavailable):
		fail(w, r, 503, "AI_UNAVAILABLE", "AI временно недоступен", true, nil)
	case errors.Is(err, projects.ErrAIProvider):
		fail(w, r, 503, "AI_UNAVAILABLE", "Сервер генерации временно недоступен", true, nil)
	case errors.Is(err, projects.ErrAIFailed):
		fail(w, r, 422, "AI_FAILED", "Модель вернула неприменимые изменения", false, nil)
	case errors.Is(err, projects.ErrConflict):
		var conflict *projects.ConflictError
		if errors.As(err, &conflict) {
			fail(w, r, 409, "REVISION_CONFLICT", "Проект был изменён на другом устройстве", false, map[string]string{"current_revision_id": conflict.CurrentRevisionID})
			return
		}
		fail(w, r, 409, "REVISION_CONFLICT", "Конфликт ревизий", false, nil)
	default:
		fail(w, r, 503, "DEPENDENCY_UNAVAILABLE", "Временная ошибка сервера", true, nil)
	}
}

func fail(w http.ResponseWriter, r *http.Request, status int, code, message string, retry bool, details any) {
	var body errorBody
	body.Error.Code, body.Error.Message, body.Error.Retryable, body.Error.Details = code, message, retry, details
	if v, ok := r.Context().Value(requestIDKey{}).(string); ok {
		body.Error.RequestID = v
	}
	writeJSON(w, status, body)
}

func root(w http.ResponseWriter, r *http.Request) {
	if strings.Contains(r.Header.Get("Accept"), "text/html") {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, rootPage)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"service": "gamedev",
		"status":  "ok",
		"health":  "/health/live",
	})
}

const rootPage = `<!DOCTYPE html>
<html lang="ru">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>GameDev</title>
<style>
  body { margin: 0; min-height: 100vh; display: grid; place-items: center; background: #111; color: #eee; font: 16px/1.5 system-ui, sans-serif; }
  main { width: min(32rem, calc(100% - 2rem)); }
  h1 { font-size: 1.5rem; font-weight: 600; margin: 0 0 .5rem; }
  p { margin: 0; color: #aaa; }
  a { color: #9cf; }
</style>
</head>
<body>
<main>
  <h1>GameDev API работает</h1>
  <p>Песочница для Misa. Проверка: <a href="/health/live">/health/live</a>.</p>
</main>
</body>
</html>
`

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
