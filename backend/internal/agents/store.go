package agents

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrNotFound = errors.New("job not found")
	ErrConflict = errors.New("idempotency key already used for another request")
	ErrBusy     = errors.New("too many active jobs")
)

type Store struct{ DB *pgxpool.Pool }

type Job struct {
	ID              string          `json:"id"`
	OwnerID         string          `json:"-"`
	ProjectID       *string         `json:"project_id,omitempty"`
	Kind            string          `json:"kind"`
	Status          string          `json:"status"`
	Stage           string          `json:"stage"`
	Progress        int             `json:"progress"`
	Prompt          string          `json:"prompt"`
	Mode            string          `json:"mode"`
	Title           *string         `json:"title,omitempty"`
	BaseRevisionID  *string         `json:"base_revision_id,omitempty"`
	CancelRequested bool            `json:"cancel_requested"`
	Result          json.RawMessage `json:"result,omitempty"`
	ErrorCode       string          `json:"error_code,omitempty"`
	ErrorMessage    string          `json:"error_message,omitempty"`
	CreatedAt       time.Time       `json:"created_at"`
	UpdatedAt       time.Time       `json:"updated_at"`
	StartedAt       *time.Time      `json:"started_at,omitempty"`
	FinishedAt      *time.Time      `json:"finished_at,omitempty"`
	Steps           []Step          `json:"steps,omitempty"`
}

type Step struct {
	ID           string          `json:"id"`
	Seq          int             `json:"seq"`
	Agent        string          `json:"agent"`
	Iteration    int             `json:"iteration"`
	Status       string          `json:"status"`
	Provider     string          `json:"provider,omitempty"`
	Model        string          `json:"model,omitempty"`
	InputTokens  int             `json:"input_tokens,omitempty"`
	OutputTokens int             `json:"output_tokens,omitempty"`
	Output       json.RawMessage `json:"output,omitempty"`
	Notes        string          `json:"notes,omitempty"`
	DurationMS   int             `json:"duration_ms,omitempty"`
	CreatedAt    time.Time       `json:"created_at"`
	FinishedAt   *time.Time      `json:"finished_at,omitempty"`
}

// ContextEntry is one durable piece of the conveyor's memory for a project.
type ContextEntry struct {
	ID        string          `json:"id"`
	ProjectID *string         `json:"project_id,omitempty"`
	JobID     *string         `json:"job_id,omitempty"`
	Kind      string          `json:"kind"`
	Content   json.RawMessage `json:"content"`
	CreatedAt time.Time       `json:"created_at"`
}

type NewJob struct {
	OwnerID        string
	Kind           string
	ProjectID      *string
	BaseRevisionID *string
	Prompt         string
	Mode           string
	Title          *string
	IdempotencyKey string
}

const activeJobLimit = 2

func (s Store) CreateJob(ctx context.Context, id string, req NewJob) (Job, error) {
	var active int
	if err := s.DB.QueryRow(ctx, `SELECT count(*) FROM ai_jobs
		WHERE owner_id=$1 AND status IN ('queued','running')`, req.OwnerID).Scan(&active); err != nil {
		return Job{}, err
	}
	if active >= activeJobLimit {
		return Job{}, ErrBusy
	}
	job, err := s.insertJob(ctx, id, req)
	if err == nil {
		return job, nil
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23505" || req.IdempotencyKey == "" {
		return Job{}, err
	}
	// The same key was used before. Returning the original job makes a retried
	// request idempotent instead of starting a second conveyor.
	existing, found, lookupErr := s.JobByKey(ctx, req.OwnerID, req.Kind, req.IdempotencyKey)
	if lookupErr != nil {
		return Job{}, lookupErr
	}
	if !found {
		return Job{}, ErrConflict
	}
	if existing.Prompt != req.Prompt || existing.Mode != req.Mode {
		return Job{}, ErrConflict
	}
	return existing, nil
}

func (s Store) insertJob(ctx context.Context, id string, req NewJob) (Job, error) {
	job := Job{
		ID: id, OwnerID: req.OwnerID, Kind: req.Kind, Status: "queued", Stage: "queued",
		Prompt: req.Prompt, Mode: req.Mode, Title: req.Title,
		ProjectID: req.ProjectID, BaseRevisionID: req.BaseRevisionID,
	}
	err := s.DB.QueryRow(ctx, `INSERT INTO ai_jobs
		(id,owner_id,project_id,kind,status,stage,prompt,mode,title,base_revision_id,idempotency_key)
		VALUES ($1,$2,$3,$4,'queued','queued',$5,$6,$7,$8,$9)
		RETURNING created_at, updated_at`,
		id, req.OwnerID, req.ProjectID, req.Kind, req.Prompt, req.Mode, req.Title,
		req.BaseRevisionID, nullable(req.IdempotencyKey)).Scan(&job.CreatedAt, &job.UpdatedAt)
	return job, err
}

func (s Store) JobByKey(ctx context.Context, owner, kind, key string) (Job, bool, error) {
	job, err := s.scanJob(s.DB.QueryRow(ctx, jobColumns+` FROM ai_jobs
		WHERE owner_id=$1 AND kind=$2 AND idempotency_key=$3`, owner, kind, key))
	if errors.Is(err, pgx.ErrNoRows) {
		return Job{}, false, nil
	}
	return job, err == nil, err
}

const jobColumns = `SELECT id,owner_id,project_id,kind,status,stage,progress,prompt,mode,title,
	base_revision_id,cancel_requested,result,coalesce(error_code,''),coalesce(error_message,''),
	created_at,updated_at,started_at,finished_at`

type scanner interface {
	Scan(dest ...any) error
}

func (s Store) scanJob(row scanner) (Job, error) {
	var job Job
	err := row.Scan(&job.ID, &job.OwnerID, &job.ProjectID, &job.Kind, &job.Status, &job.Stage,
		&job.Progress, &job.Prompt, &job.Mode, &job.Title, &job.BaseRevisionID, &job.CancelRequested,
		&job.Result, &job.ErrorCode, &job.ErrorMessage, &job.CreatedAt, &job.UpdatedAt,
		&job.StartedAt, &job.FinishedAt)
	return job, err
}

func (s Store) Job(ctx context.Context, owner, id string) (Job, error) {
	job, err := s.scanJob(s.DB.QueryRow(ctx, jobColumns+` FROM ai_jobs WHERE id=$1 AND owner_id=$2`, id, owner))
	if errors.Is(err, pgx.ErrNoRows) {
		return Job{}, ErrNotFound
	}
	if err != nil {
		return Job{}, err
	}
	job.Steps, err = s.steps(ctx, id)
	return job, err
}

func (s Store) ListJobs(ctx context.Context, owner string, limit int) ([]Job, error) {
	rows, err := s.DB.Query(ctx, jobColumns+` FROM ai_jobs WHERE owner_id=$1
		ORDER BY created_at DESC LIMIT $2`, owner, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]Job, 0, limit)
	for rows.Next() {
		job, err := s.scanJob(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, job)
	}
	return items, rows.Err()
}

func (s Store) steps(ctx context.Context, jobID string) ([]Step, error) {
	rows, err := s.DB.Query(ctx, `SELECT id,seq,agent,iteration,status,coalesce(provider,''),coalesce(model,''),
		coalesce(input_tokens,0),coalesce(output_tokens,0),output,coalesce(notes,''),coalesce(duration_ms,0),
		created_at,finished_at FROM ai_job_steps WHERE job_id=$1 ORDER BY seq`, jobID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []Step
	for rows.Next() {
		var step Step
		if err := rows.Scan(&step.ID, &step.Seq, &step.Agent, &step.Iteration, &step.Status,
			&step.Provider, &step.Model, &step.InputTokens, &step.OutputTokens, &step.Output,
			&step.Notes, &step.DurationMS, &step.CreatedAt, &step.FinishedAt); err != nil {
			return nil, err
		}
		items = append(items, step)
	}
	return items, rows.Err()
}

// ClaimJob leases the oldest waiting job. SKIP LOCKED lets several workers, in
// this process or another one, pull from the same queue without colliding.
func (s Store) ClaimJob(ctx context.Context, lease time.Duration) (Job, bool, error) {
	job, err := s.scanJob(s.DB.QueryRow(ctx, `UPDATE ai_jobs SET
			status='running',
			started_at=coalesce(started_at, now()),
			updated_at=now(),
			lease_expires_at=now() + $1::interval
		WHERE id = (
			SELECT id FROM ai_jobs
			WHERE status='queued' OR (status='running' AND lease_expires_at < now())
			ORDER BY created_at
			FOR UPDATE SKIP LOCKED
			LIMIT 1
		)
		RETURNING id,owner_id,project_id,kind,status,stage,progress,prompt,mode,title,
			base_revision_id,cancel_requested,result,coalesce(error_code,''),coalesce(error_message,''),
			created_at,updated_at,started_at,finished_at`, lease.String()))
	if errors.Is(err, pgx.ErrNoRows) {
		return Job{}, false, nil
	}
	if err != nil {
		return Job{}, false, err
	}
	return job, true, nil
}

func (s Store) Heartbeat(ctx context.Context, jobID string, lease time.Duration) (bool, error) {
	var cancelRequested bool
	err := s.DB.QueryRow(ctx, `UPDATE ai_jobs SET lease_expires_at=now() + $2::interval, updated_at=now()
		WHERE id=$1 RETURNING cancel_requested`, jobID, lease.String()).Scan(&cancelRequested)
	return cancelRequested, err
}

func (s Store) SetStage(ctx context.Context, jobID, stage string, progress int) error {
	_, err := s.DB.Exec(ctx, `UPDATE ai_jobs SET stage=$2, progress=$3, updated_at=now() WHERE id=$1`,
		jobID, stage, progress)
	return err
}

func (s Store) FinishJob(ctx context.Context, jobID, status string, projectID *string, result json.RawMessage, errorCode, errorMessage string) error {
	stage := "done"
	progress := 100
	if status != "succeeded" {
		stage = "done"
		progress = 100
	}
	_, err := s.DB.Exec(ctx, `UPDATE ai_jobs SET status=$2, stage=$3, progress=$4, project_id=coalesce($5, project_id),
		result=$6, error_code=$7, error_message=$8, finished_at=now(), updated_at=now(), lease_expires_at=NULL
		WHERE id=$1`, jobID, status, stage, progress, projectID, result, nullable(errorCode), nullable(errorMessage))
	return err
}

func (s Store) RequestCancel(ctx context.Context, owner, jobID string) error {
	tag, err := s.DB.Exec(ctx, `UPDATE ai_jobs SET cancel_requested=true, updated_at=now()
		WHERE id=$1 AND owner_id=$2 AND status IN ('queued','running')`, jobID, owner)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s Store) StartStep(ctx context.Context, id, jobID string, seq int, agent string, iteration int) error {
	_, err := s.DB.Exec(ctx, `INSERT INTO ai_job_steps (id,job_id,seq,agent,iteration,status)
		VALUES ($1,$2,$3,$4,$5,'running')`, id, jobID, seq, agent, iteration)
	return err
}

func (s Store) FinishStep(ctx context.Context, id, status string, provider, model string, inputTokens, outputTokens int, output json.RawMessage, notes string, duration time.Duration) error {
	_, err := s.DB.Exec(ctx, `UPDATE ai_job_steps SET status=$2, provider=$3, model=$4,
		input_tokens=$5, output_tokens=$6, output=$7, notes=$8, duration_ms=$9, finished_at=now()
		WHERE id=$1`, id, status, nullable(provider), nullable(model), inputTokens, outputTokens,
		output, nullable(notes), duration.Milliseconds())
	return err
}

// PutContext stores one memory entry and retires the previous entry of the same
// kind for the project, so a later job reads the current design rather than
// every draft that led to it.
func (s Store) PutContext(ctx context.Context, id, owner string, projectID, jobID *string, kind string, content json.RawMessage) error {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if projectID != nil {
		if _, err := tx.Exec(ctx, `UPDATE ai_context_entries SET superseded_at=now()
			WHERE project_id=$1 AND kind=$2 AND superseded_at IS NULL`, *projectID, kind); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `INSERT INTO ai_context_entries (id,owner_id,project_id,job_id,kind,content)
		VALUES ($1,$2,$3,$4,$5,$6)`, id, owner, projectID, jobID, kind, content); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// ProjectContext returns the live memory for a project, newest first.
func (s Store) ProjectContext(ctx context.Context, owner, projectID string) ([]ContextEntry, error) {
	rows, err := s.DB.Query(ctx, `SELECT id,project_id,job_id,kind,content,created_at
		FROM ai_context_entries
		WHERE owner_id=$1 AND project_id=$2 AND superseded_at IS NULL
		ORDER BY created_at DESC LIMIT 40`, owner, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []ContextEntry
	for rows.Next() {
		var entry ContextEntry
		if err := rows.Scan(&entry.ID, &entry.ProjectID, &entry.JobID, &entry.Kind, &entry.Content, &entry.CreatedAt); err != nil {
			return nil, err
		}
		items = append(items, entry)
	}
	return items, rows.Err()
}

// JobContext returns everything written during one job, oldest first. This is
// what the conveyor itself reads while it runs.
func (s Store) JobContext(ctx context.Context, jobID string) ([]ContextEntry, error) {
	rows, err := s.DB.Query(ctx, `SELECT id,project_id,job_id,kind,content,created_at
		FROM ai_context_entries WHERE job_id=$1 ORDER BY created_at`, jobID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []ContextEntry
	for rows.Next() {
		var entry ContextEntry
		if err := rows.Scan(&entry.ID, &entry.ProjectID, &entry.JobID, &entry.Kind, &entry.Content, &entry.CreatedAt); err != nil {
			return nil, err
		}
		items = append(items, entry)
	}
	return items, rows.Err()
}

func nullable(value string) any {
	if value == "" {
		return nil
	}
	return value
}
