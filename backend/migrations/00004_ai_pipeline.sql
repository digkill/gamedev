-- +goose Up
-- A pipeline job runs the agent conveyor for one prompt. project_id is NULL
-- until the conveyor has produced a project that passed the audit.
CREATE TABLE ai_jobs (
    id uuid PRIMARY KEY,
    owner_id uuid NOT NULL REFERENCES users(id),
    project_id uuid REFERENCES projects(id),
    kind text NOT NULL CHECK (kind IN ('create_game', 'improve_game')),
    status text NOT NULL CHECK (status IN ('queued', 'running', 'succeeded', 'failed', 'cancelled')),
    stage text NOT NULL DEFAULT 'queued' CHECK (stage IN (
        'queued', 'architect', 'game_designer', 'developer', 'compiler',
        'tester', 'reviewer', 'auditor', 'publishing', 'done'
    )),
    progress integer NOT NULL DEFAULT 0 CHECK (progress BETWEEN 0 AND 100),
    prompt text NOT NULL CHECK (char_length(prompt) BETWEEN 1 AND 10000),
    mode text NOT NULL CHECK (mode IN ('2d', '3d')),
    title text CHECK (title IS NULL OR char_length(title) BETWEEN 1 AND 120),
    base_revision_id uuid,
    idempotency_key text CHECK (idempotency_key IS NULL OR char_length(idempotency_key) BETWEEN 1 AND 200),
    cancel_requested boolean NOT NULL DEFAULT false,
    result jsonb,
    error_code text,
    error_message text,
    lease_expires_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    started_at timestamptz,
    updated_at timestamptz NOT NULL DEFAULT now(),
    finished_at timestamptz
);
CREATE INDEX ai_jobs_owner_created_idx ON ai_jobs(owner_id, created_at DESC);
CREATE INDEX ai_jobs_project_created_idx ON ai_jobs(project_id, created_at DESC);
CREATE INDEX ai_jobs_queue_idx ON ai_jobs(created_at) WHERE status IN ('queued', 'running');
CREATE UNIQUE INDEX ai_jobs_idempotency_key ON ai_jobs(owner_id, kind, idempotency_key)
    WHERE idempotency_key IS NOT NULL;

-- One row per agent turn, including repeated turns of a repair loop. This is
-- the audit trail: which agent said what, with which model, and how long it took.
CREATE TABLE ai_job_steps (
    id uuid PRIMARY KEY,
    job_id uuid NOT NULL REFERENCES ai_jobs(id) ON DELETE CASCADE,
    seq integer NOT NULL CHECK (seq >= 0),
    agent text NOT NULL CHECK (agent IN (
        'architect', 'game_designer', 'developer', 'compiler', 'tester', 'reviewer', 'auditor'
    )),
    iteration integer NOT NULL DEFAULT 1 CHECK (iteration >= 1),
    status text NOT NULL CHECK (status IN ('running', 'succeeded', 'failed', 'skipped')),
    provider text,
    model text,
    input_tokens integer,
    output_tokens integer,
    output jsonb,
    notes text,
    duration_ms integer,
    created_at timestamptz NOT NULL DEFAULT now(),
    finished_at timestamptz,
    UNIQUE (job_id, seq)
);
CREATE INDEX ai_job_steps_job_idx ON ai_job_steps(job_id, seq);

-- Durable context the conveyor carries between agents and between jobs of the
-- same project. Prompts are built from these rows, never from raw transcripts.
CREATE TABLE ai_context_entries (
    id uuid PRIMARY KEY,
    owner_id uuid NOT NULL REFERENCES users(id),
    project_id uuid REFERENCES projects(id),
    job_id uuid REFERENCES ai_jobs(id) ON DELETE SET NULL,
    kind text NOT NULL CHECK (kind IN (
        'user_prompt', 'blueprint', 'design', 'spec', 'test_report', 'review', 'audit', 'summary'
    )),
    content jsonb NOT NULL CHECK (jsonb_typeof(content) = 'object'),
    superseded_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX ai_context_project_kind_idx ON ai_context_entries(project_id, kind, created_at DESC)
    WHERE superseded_at IS NULL;
CREATE INDEX ai_context_job_idx ON ai_context_entries(job_id, created_at);

-- +goose Down
DROP TABLE ai_context_entries;
DROP TABLE ai_job_steps;
DROP TABLE ai_jobs;
