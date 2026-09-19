-- +goose Up
CREATE TABLE users (
    id uuid PRIMARY KEY,
    display_name text NOT NULL CHECK (char_length(display_name) BETWEEN 1 AND 120),
    status text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'blocked')),
    created_at timestamptz NOT NULL DEFAULT now()
);

-- Only a SHA-256 digest is stored. Identity exchange and token issuance are separate work.
CREATE TABLE access_tokens (
    token_hash bytea PRIMARY KEY CHECK (octet_length(token_hash) = 32),
    user_id uuid NOT NULL REFERENCES users(id),
    expires_at timestamptz NOT NULL,
    revoked_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX access_tokens_user_idx ON access_tokens(user_id);

CREATE TABLE projects (
    id uuid PRIMARY KEY,
    owner_id uuid NOT NULL REFERENCES users(id),
    title text NOT NULL CHECK (char_length(title) BETWEEN 1 AND 120),
    mode text NOT NULL CHECK (mode IN ('2d', '3d')),
    head_revision_id uuid,
    lock_version bigint NOT NULL DEFAULT 0 CHECK (lock_version >= 0),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    deleted_at timestamptz,
    UNIQUE (id, head_revision_id)
);
CREATE INDEX projects_owner_updated_idx ON projects(owner_id, updated_at DESC, id DESC) WHERE deleted_at IS NULL;

CREATE TABLE project_revisions (
    id uuid PRIMARY KEY,
    project_id uuid NOT NULL REFERENCES projects(id),
    parent_id uuid,
    schema_version integer NOT NULL CHECK (schema_version > 0),
    manifest jsonb NOT NULL CHECK (jsonb_typeof(manifest) = 'object'),
    content_hash text NOT NULL CHECK (content_hash ~ '^[a-f0-9]{64}$'),
    author_id uuid NOT NULL REFERENCES users(id),
    source text NOT NULL CHECK (source IN ('create', 'editor', 'restore', 'ai')),
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (project_id, id),
    CONSTRAINT revision_parent_same_project FOREIGN KEY (project_id, parent_id)
        REFERENCES project_revisions(project_id, id)
);
CREATE INDEX project_revisions_project_created_idx ON project_revisions(project_id, created_at DESC);
ALTER TABLE projects ADD CONSTRAINT head_revision_same_project
    FOREIGN KEY (id, head_revision_id) REFERENCES project_revisions(project_id, id);

CREATE TABLE project_commands (
    project_id uuid NOT NULL,
    revision_id uuid NOT NULL,
    operation_id uuid NOT NULL,
    commands jsonb NOT NULL CHECK (jsonb_typeof(commands) = 'array'),
    PRIMARY KEY (project_id, operation_id),
    FOREIGN KEY (project_id, revision_id) REFERENCES project_revisions(project_id, id)
);

CREATE TABLE idempotency_keys (
    actor_id uuid NOT NULL REFERENCES users(id),
    endpoint text NOT NULL,
    key text NOT NULL CHECK (char_length(key) BETWEEN 1 AND 200),
    request_hash bytea NOT NULL CHECK (octet_length(request_hash) = 32),
    response_body jsonb,
    response_status integer,
    created_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL DEFAULT (now() + interval '7 days'),
    PRIMARY KEY (actor_id, endpoint, key)
);
CREATE INDEX idempotency_expiry_idx ON idempotency_keys(expires_at);

-- +goose Down
DROP TABLE idempotency_keys;
DROP TABLE project_commands;
ALTER TABLE projects DROP CONSTRAINT head_revision_same_project;
DROP TABLE project_revisions;
DROP TABLE projects;
DROP TABLE access_tokens;
DROP TABLE users;
