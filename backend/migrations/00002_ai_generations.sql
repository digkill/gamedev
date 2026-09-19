-- +goose Up
CREATE TABLE ai_generations (
    id uuid PRIMARY KEY,
    project_id uuid NOT NULL REFERENCES projects(id),
    owner_id uuid NOT NULL REFERENCES users(id),
    base_revision_id uuid NOT NULL,
    status text NOT NULL CHECK (status IN (
        'queued', 'planning', 'generating', 'validating', 'repairing',
        'ready', 'applied', 'failed', 'cancelled', 'conflict', 'expired'
    )),
    prompt text NOT NULL CHECK (char_length(prompt) BETWEEN 1 AND 10000),
    summary text,
    operations jsonb,
    error_code text,
    model text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX ai_generations_project_created_idx ON ai_generations(project_id, created_at DESC);
CREATE INDEX ai_generations_owner_active_idx ON ai_generations(owner_id, created_at DESC)
    WHERE status IN ('queued', 'planning', 'generating', 'validating', 'repairing');

-- +goose Down
DROP TABLE ai_generations;
