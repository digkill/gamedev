-- +goose Up
-- Email identity for the existing users table. Rows created by devseed and the
-- bootstrap token keep email NULL and stay usable.
ALTER TABLE users
    ADD COLUMN email text,
    ADD COLUMN password_hash text,
    ADD COLUMN email_verified_at timestamptz,
    ADD COLUMN updated_at timestamptz NOT NULL DEFAULT now();

ALTER TABLE users DROP CONSTRAINT users_status_check;
ALTER TABLE users ADD CONSTRAINT users_status_check
    CHECK (status IN ('pending', 'active', 'blocked'));

-- Email is stored already normalised to lower case; the check keeps that true
-- even if a future code path writes the column directly.
ALTER TABLE users ADD CONSTRAINT users_email_shape CHECK (
    email IS NULL OR (
        email = lower(email)
        AND char_length(email) BETWEEN 3 AND 254
        AND position('@' in email) > 1
    )
);
ALTER TABLE users ADD CONSTRAINT users_password_requires_email CHECK (
    password_hash IS NULL OR email IS NOT NULL
);
CREATE UNIQUE INDEX users_email_key ON users(email) WHERE email IS NOT NULL;

-- Only a SHA-256 digest of the one-time code is stored, like access tokens.
CREATE TABLE email_codes (
    id uuid PRIMARY KEY,
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    purpose text NOT NULL CHECK (purpose IN ('verify_email', 'login', 'password_reset')),
    code_hash bytea NOT NULL CHECK (octet_length(code_hash) = 32),
    expires_at timestamptz NOT NULL,
    consumed_at timestamptz,
    attempts integer NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX email_codes_user_purpose_idx ON email_codes(user_id, purpose, created_at DESC);
CREATE INDEX email_codes_expiry_idx ON email_codes(expires_at);

-- A refresh token points at the access token issued with it, so logout revokes
-- the pair and rotation cannot leave an orphaned access token behind.
CREATE TABLE refresh_tokens (
    token_hash bytea PRIMARY KEY CHECK (octet_length(token_hash) = 32),
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    access_token_hash bytea,
    expires_at timestamptz NOT NULL,
    revoked_at timestamptz,
    rotated_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX refresh_tokens_user_idx ON refresh_tokens(user_id);
CREATE INDEX refresh_tokens_expiry_idx ON refresh_tokens(expires_at);

-- Fixed-window counters for login, code request, and verification attempts.
CREATE TABLE auth_rate_limits (
    scope text NOT NULL CHECK (char_length(scope) BETWEEN 1 AND 40),
    subject text NOT NULL CHECK (char_length(subject) BETWEEN 1 AND 254),
    window_start timestamptz NOT NULL,
    hits integer NOT NULL DEFAULT 0 CHECK (hits >= 0),
    PRIMARY KEY (scope, subject, window_start)
);
CREATE INDEX auth_rate_limits_window_idx ON auth_rate_limits(window_start);

-- +goose Down
DROP TABLE auth_rate_limits;
DROP TABLE refresh_tokens;
DROP TABLE email_codes;
DROP INDEX users_email_key;
ALTER TABLE users DROP CONSTRAINT users_password_requires_email;
ALTER TABLE users DROP CONSTRAINT users_email_shape;
ALTER TABLE users DROP CONSTRAINT users_status_check;
ALTER TABLE users ADD CONSTRAINT users_status_check CHECK (status IN ('active', 'blocked'));
ALTER TABLE users
    DROP COLUMN updated_at,
    DROP COLUMN email_verified_at,
    DROP COLUMN password_hash,
    DROP COLUMN email;
