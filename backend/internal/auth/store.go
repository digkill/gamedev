package auth

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Store struct{ DB *pgxpool.Pool }

type account struct {
	ID            string
	Email         string
	DisplayName   string
	Status        string
	PasswordHash  string
	EmailVerified bool
	CreatedAt     time.Time
}

func (a account) public() User {
	return User{
		ID:            a.ID,
		Email:         a.Email,
		DisplayName:   a.DisplayName,
		Status:        a.Status,
		EmailVerified: a.EmailVerified,
		CreatedAt:     a.CreatedAt,
	}
}

func (s Store) CreateUser(ctx context.Context, id, email, displayName, passwordHash, status string) (account, error) {
	var a account
	err := s.DB.QueryRow(ctx, `INSERT INTO users (id, display_name, status, email, password_hash)
		VALUES ($1,$2,$3,$4,$5)
		RETURNING id, coalesce(email,''), display_name, status, coalesce(password_hash,''),
		          email_verified_at IS NOT NULL, created_at`,
		id, displayName, status, email, passwordHash).Scan(
		&a.ID, &a.Email, &a.DisplayName, &a.Status, &a.PasswordHash, &a.EmailVerified, &a.CreatedAt)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return account{}, ErrEmailTaken
	}
	return a, err
}

func (s Store) accountByEmail(ctx context.Context, email string) (account, error) {
	var a account
	err := s.DB.QueryRow(ctx, `SELECT id, coalesce(email,''), display_name, status, coalesce(password_hash,''),
		email_verified_at IS NOT NULL, created_at FROM users WHERE email=$1`, email).Scan(
		&a.ID, &a.Email, &a.DisplayName, &a.Status, &a.PasswordHash, &a.EmailVerified, &a.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return account{}, ErrNotFound
	}
	return a, err
}

func (s Store) User(ctx context.Context, id string) (User, error) {
	var a account
	err := s.DB.QueryRow(ctx, `SELECT id, coalesce(email,''), display_name, status, '',
		email_verified_at IS NOT NULL, created_at FROM users WHERE id=$1`, id).Scan(
		&a.ID, &a.Email, &a.DisplayName, &a.Status, &a.PasswordHash, &a.EmailVerified, &a.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, ErrNotFound
	}
	return a.public(), err
}

func (s Store) activate(ctx context.Context, userID string) error {
	_, err := s.DB.Exec(ctx, `UPDATE users
		SET email_verified_at=coalesce(email_verified_at, now()),
		    status=CASE WHEN status='pending' THEN 'active' ELSE status END,
		    updated_at=now()
		WHERE id=$1`, userID)
	return err
}

func (s Store) setPassword(ctx context.Context, userID, passwordHash string) error {
	_, err := s.DB.Exec(ctx, `UPDATE users SET password_hash=$2, updated_at=now() WHERE id=$1`, userID, passwordHash)
	return err
}

func (s Store) updateDisplayName(ctx context.Context, userID, displayName string) error {
	_, err := s.DB.Exec(ctx, `UPDATE users SET display_name=$2, updated_at=now() WHERE id=$1`, userID, displayName)
	return err
}

// putCode supersedes any earlier unused code for the same purpose, so only the
// most recent message works.
func (s Store) putCode(ctx context.Context, id, userID, purpose string, codeHash []byte, expiresAt time.Time) error {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `UPDATE email_codes SET consumed_at=now()
		WHERE user_id=$1 AND purpose=$2 AND consumed_at IS NULL`, userID, purpose); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO email_codes (id,user_id,purpose,code_hash,expires_at)
		VALUES ($1,$2,$3,$4,$5)`, id, userID, purpose, codeHash, expiresAt); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s Store) codeIssuedWithin(ctx context.Context, userID, purpose string, window time.Duration) (bool, error) {
	var exists bool
	err := s.DB.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM email_codes
		WHERE user_id=$1 AND purpose=$2 AND created_at > now() - $3::interval)`,
		userID, purpose, window.String()).Scan(&exists)
	return exists, err
}

// consumeCode checks and burns the newest live code in one transaction. A wrong
// code costs an attempt; exhausting the attempts burns the code entirely.
func (s Store) consumeCode(ctx context.Context, userID, purpose string, codeHash []byte, maxAttempts int) error {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var id string
	var stored []byte
	var attempts int
	err = tx.QueryRow(ctx, `SELECT id, code_hash, attempts FROM email_codes
		WHERE user_id=$1 AND purpose=$2 AND consumed_at IS NULL AND expires_at > now()
		ORDER BY created_at DESC LIMIT 1 FOR UPDATE`, userID, purpose).Scan(&id, &stored, &attempts)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrCodeInvalid
	}
	if err != nil {
		return err
	}
	if !equalDigest(stored, codeHash) {
		attempts++
		consume := "NULL"
		if attempts >= maxAttempts {
			consume = "now()"
		}
		if _, err := tx.Exec(ctx, `UPDATE email_codes SET attempts=$2, consumed_at=`+consume+` WHERE id=$1`, id, attempts); err != nil {
			return err
		}
		if err := tx.Commit(ctx); err != nil {
			return err
		}
		return ErrCodeInvalid
	}
	if _, err := tx.Exec(ctx, `UPDATE email_codes SET consumed_at=now() WHERE id=$1`, id); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s Store) issueSession(ctx context.Context, userID string, accessHash []byte, accessExpires time.Time, refreshHash []byte, refreshExpires time.Time) error {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `INSERT INTO access_tokens (token_hash,user_id,expires_at)
		VALUES ($1,$2,$3)`, accessHash, userID, accessExpires); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO refresh_tokens (token_hash,user_id,access_token_hash,expires_at)
		VALUES ($1,$2,$3,$4)`, refreshHash, userID, accessHash, refreshExpires); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// rotateRefresh consumes a refresh token exactly once. Reuse of an already
// rotated token revokes the whole family, which is the standard response to a
// stolen token being replayed.
func (s Store) rotateRefresh(ctx context.Context, refreshHash []byte) (string, error) {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)
	var userID string
	var revokedAt, rotatedAt *time.Time
	var expiresAt time.Time
	var accessHash []byte
	err = tx.QueryRow(ctx, `SELECT user_id, access_token_hash, expires_at, revoked_at, rotated_at
		FROM refresh_tokens WHERE token_hash=$1 FOR UPDATE`, refreshHash).Scan(
		&userID, &accessHash, &expiresAt, &revokedAt, &rotatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrUnauthorized
	}
	if err != nil {
		return "", err
	}
	if rotatedAt != nil {
		if _, err := tx.Exec(ctx, `UPDATE refresh_tokens SET revoked_at=coalesce(revoked_at, now())
			WHERE user_id=$1 AND revoked_at IS NULL`, userID); err != nil {
			return "", err
		}
		if _, err := tx.Exec(ctx, `UPDATE access_tokens SET revoked_at=coalesce(revoked_at, now())
			WHERE user_id=$1 AND revoked_at IS NULL`, userID); err != nil {
			return "", err
		}
		if err := tx.Commit(ctx); err != nil {
			return "", err
		}
		return "", ErrUnauthorized
	}
	if revokedAt != nil || expiresAt.Before(time.Now()) {
		return "", ErrUnauthorized
	}
	var status string
	if err := tx.QueryRow(ctx, `SELECT status FROM users WHERE id=$1`, userID).Scan(&status); err != nil {
		return "", err
	}
	if status != "active" {
		return "", ErrAccountBlocked
	}
	if _, err := tx.Exec(ctx, `UPDATE refresh_tokens SET rotated_at=now(), revoked_at=now() WHERE token_hash=$1`, refreshHash); err != nil {
		return "", err
	}
	if accessHash != nil {
		if _, err := tx.Exec(ctx, `UPDATE access_tokens SET revoked_at=coalesce(revoked_at, now()) WHERE token_hash=$1`, accessHash); err != nil {
			return "", err
		}
	}
	return userID, tx.Commit(ctx)
}

func (s Store) revokeRefresh(ctx context.Context, userID string, refreshHash []byte) error {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var accessHash []byte
	err = tx.QueryRow(ctx, `UPDATE refresh_tokens SET revoked_at=coalesce(revoked_at, now())
		WHERE token_hash=$1 AND user_id=$2 RETURNING access_token_hash`, refreshHash, userID).Scan(&accessHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if accessHash != nil {
		if _, err := tx.Exec(ctx, `UPDATE access_tokens SET revoked_at=coalesce(revoked_at, now())
			WHERE token_hash=$1`, accessHash); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (s Store) revokeAll(ctx context.Context, userID string) error {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `UPDATE refresh_tokens SET revoked_at=now() WHERE user_id=$1 AND revoked_at IS NULL`, userID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE access_tokens SET revoked_at=now() WHERE user_id=$1 AND revoked_at IS NULL`, userID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s Store) revokeAccess(ctx context.Context, accessHash []byte) error {
	_, err := s.DB.Exec(ctx, `UPDATE access_tokens SET revoked_at=coalesce(revoked_at, now()) WHERE token_hash=$1`, accessHash)
	return err
}

// rateLimit counts hits in a fixed window. It is deliberately simple: the
// counter lives in the same database as the accounts it protects, so a restart
// of the API does not reset it.
func (s Store) rateLimit(ctx context.Context, scope, subject string, window time.Duration, limit int) error {
	if subject == "" || limit <= 0 {
		return nil
	}
	start := time.Now().UTC().Truncate(window)
	var hits int
	err := s.DB.QueryRow(ctx, `INSERT INTO auth_rate_limits (scope,subject,window_start,hits)
		VALUES ($1,$2,$3,1)
		ON CONFLICT (scope,subject,window_start) DO UPDATE SET hits=auth_rate_limits.hits+1
		RETURNING hits`, scope, subject, start).Scan(&hits)
	if err != nil {
		return err
	}
	if hits > limit {
		return ErrRateLimited
	}
	return nil
}

func (s Store) purgeExpired(ctx context.Context) error {
	_, err := s.DB.Exec(ctx, `DELETE FROM email_codes WHERE expires_at < now() - interval '1 day'`)
	if err != nil {
		return err
	}
	if _, err := s.DB.Exec(ctx, `DELETE FROM refresh_tokens WHERE expires_at < now() - interval '7 days'`); err != nil {
		return err
	}
	_, err = s.DB.Exec(ctx, `DELETE FROM auth_rate_limits WHERE window_start < now() - interval '1 day'`)
	return err
}

func equalDigest(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	var diff byte
	for i := range a {
		diff |= a[i] ^ b[i]
	}
	return diff == 0
}
