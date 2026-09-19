package projects

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Store struct{ DB *pgxpool.Pool }

type ConflictError struct{ CurrentRevisionID string }

func (e *ConflictError) Error() string        { return ErrConflict.Error() }
func (e *ConflictError) Is(target error) bool { return target == ErrConflict }

func (s Store) EnsureBootstrapToken(ctx context.Context, hexHash string) error {
	raw, err := hex.DecodeString(strings.TrimSpace(hexHash))
	if err != nil || len(raw) != 32 {
		return errors.New("invalid bootstrap token hash")
	}
	var exists bool
	if err := s.DB.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM access_tokens WHERE token_hash=$1)`, raw).Scan(&exists); err != nil {
		return err
	}
	if exists {
		return nil
	}
	id, err := NewUUID()
	if err != nil {
		return err
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `INSERT INTO users(id, display_name) VALUES ($1, $2)`, id, "Bootstrap operator"); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO access_tokens(token_hash, user_id, expires_at)
		VALUES ($1, $2, now() + interval '90 days')`, raw, id); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s Store) Actor(ctx context.Context, bearer string) (string, error) {
	if len(bearer) < 32 || len(bearer) > 512 {
		return "", ErrNotFound
	}
	hash := sha256.Sum256([]byte(bearer))
	var id string
	err := s.DB.QueryRow(ctx, `SELECT u.id FROM access_tokens t JOIN users u ON u.id=t.user_id
		WHERE t.token_hash=$1 AND t.revoked_at IS NULL AND t.expires_at>now() AND u.status='active'`, hash[:]).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	return id, err
}

func (s Store) Create(ctx context.Context, actor, key string, requestHash [32]byte, req CreateRequest) (Project, error) {
	if err := req.Validate(); err != nil {
		return Project{}, err
	}
	manifest, contentHash, err := CanonicalManifest(req.Manifest)
	if err != nil {
		return Project{}, err
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return Project{}, err
	}
	defer tx.Rollback(ctx)
	const endpoint = "/api/v1/projects"
	if cached, err := beginIdempotent(ctx, tx, actor, endpoint, key, requestHash); err != nil {
		return Project{}, err
	} else if cached != nil {
		var p Project
		if err := json.Unmarshal(cached, &p); err != nil {
			return Project{}, err
		}
		return p, nil
	}
	doc, err := validateProject(req.Manifest)
	if err != nil {
		return Project{}, err
	}
	// Preserve the UUID of an offline-created project. The stored document and
	// database row must refer to the same project.
	projectID := doc.Manifest.ProjectID
	revisionID, err := NewUUID()
	if err != nil {
		return Project{}, err
	}
	var p Project
	err = tx.QueryRow(ctx, `INSERT INTO projects (id,owner_id,title,mode) VALUES ($1,$2,$3,$4)
		RETURNING created_at,updated_at`, projectID, actor, req.Title, req.Mode).Scan(&p.CreatedAt, &p.UpdatedAt)
	if err != nil {
		return Project{}, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO project_revisions
		(id,project_id,schema_version,manifest,content_hash,author_id,source)
		VALUES ($1,$2,$3,$4,$5,$6,'create')`, revisionID, projectID, req.SchemaVersion, manifest, contentHash, actor)
	if err != nil {
		return Project{}, err
	}
	_, err = tx.Exec(ctx, `UPDATE projects SET head_revision_id=$2 WHERE id=$1`, projectID, revisionID)
	if err != nil {
		return Project{}, err
	}
	p.ID, p.Title, p.Mode, p.HeadRevisionID, p.SchemaVersion, p.Manifest = projectID, req.Title, req.Mode, revisionID, req.SchemaVersion, manifest
	if err := completeIdempotent(ctx, tx, actor, endpoint, key, p, 201); err != nil {
		return Project{}, err
	}
	return p, tx.Commit(ctx)
}

func (s Store) Get(ctx context.Context, actor, projectID string) (Project, error) {
	var p Project
	err := s.DB.QueryRow(ctx, `SELECT p.id,p.title,p.mode,p.head_revision_id,p.lock_version,
		r.schema_version,r.manifest,p.created_at,p.updated_at
		FROM projects p JOIN project_revisions r ON r.id=p.head_revision_id
		WHERE p.id=$1 AND p.owner_id=$2 AND p.deleted_at IS NULL`, projectID, actor).Scan(
		&p.ID, &p.Title, &p.Mode, &p.HeadRevisionID, &p.LockVersion, &p.SchemaVersion, &p.Manifest, &p.CreatedAt, &p.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Project{}, ErrNotFound
	}
	return p, err
}

func (s Store) List(ctx context.Context, actor, cursor string, limit int) ([]Project, string, error) {
	rows, err := s.DB.Query(ctx, `SELECT id,title,mode,head_revision_id,lock_version,created_at,updated_at
		FROM projects WHERE owner_id=$1 AND deleted_at IS NULL AND ($2::uuid IS NULL OR id>$2::uuid)
		ORDER BY id LIMIT $3`, actor, nullableUUID(cursor), limit+1)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	items := make([]Project, 0, limit)
	for rows.Next() {
		var p Project
		if err := rows.Scan(&p.ID, &p.Title, &p.Mode, &p.HeadRevisionID, &p.LockVersion, &p.CreatedAt, &p.UpdatedAt); err != nil {
			return nil, "", err
		}
		items = append(items, p)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	next := ""
	if len(items) > limit {
		items = items[:limit]
		next = items[len(items)-1].ID
	}
	return items, next, nil
}

func (s Store) Change(ctx context.Context, actor, projectID, key string, requestHash [32]byte, req ChangeRequest) (Project, error) {
	return s.applyOps(ctx, actor, projectID, key, requestHash, "/api/v1/projects/"+projectID+"/changes", req, "editor")
}

func (s Store) applyOps(ctx context.Context, actor, projectID, key string, requestHash [32]byte, endpoint string, req ChangeRequest, source string) (Project, error) {
	if err := req.Validate(); err != nil {
		return Project{}, err
	}
	if source != "editor" && source != "ai" {
		return Project{}, ErrInvalid
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return Project{}, err
	}
	defer tx.Rollback(ctx)
	if cached, err := beginIdempotent(ctx, tx, actor, endpoint, key, requestHash); err != nil {
		return Project{}, err
	} else if cached != nil {
		var p Project
		if err := json.Unmarshal(cached, &p); err != nil {
			return Project{}, err
		}
		return p, nil
	}
	var p Project
	err = tx.QueryRow(ctx, `SELECT p.id,p.title,p.mode,p.head_revision_id,p.lock_version,
		p.created_at,p.updated_at,r.manifest,r.schema_version
		FROM projects p JOIN project_revisions r ON r.id=p.head_revision_id
		WHERE p.id=$1 AND p.owner_id=$2 AND p.deleted_at IS NULL FOR UPDATE OF p`, projectID, actor).Scan(
		&p.ID, &p.Title, &p.Mode, &p.HeadRevisionID, &p.LockVersion, &p.CreatedAt, &p.UpdatedAt, &p.Manifest, &p.SchemaVersion)
	if errors.Is(err, pgx.ErrNoRows) {
		return Project{}, ErrNotFound
	}
	if err != nil {
		return Project{}, err
	}
	if p.HeadRevisionID != req.BaseRevisionID {
		return Project{}, &ConflictError{CurrentRevisionID: p.HeadRevisionID}
	}
	for _, op := range req.Operations {
		var exists bool
		err = tx.QueryRow(ctx, `SELECT true FROM project_commands WHERE project_id=$1 AND operation_id=$2`, projectID, op.OperationID).Scan(&exists)
		if err == nil {
			return Project{}, ErrInvalid
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return Project{}, err
		}
	}
	manifest, err := ApplyCommands(p.Manifest, p.Mode, req.Operations)
	if err != nil {
		return Project{}, err
	}
	manifest, contentHash, err := CanonicalManifest(manifest)
	if err != nil {
		return Project{}, err
	}
	doc, err := validateProject(manifest)
	if err != nil {
		return Project{}, err
	}
	revisionID, err := NewUUID()
	if err != nil {
		return Project{}, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO project_revisions
		(id,project_id,parent_id,schema_version,manifest,content_hash,author_id,source)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`, revisionID, projectID, p.HeadRevisionID, p.SchemaVersion, manifest, contentHash, actor, source)
	if err != nil {
		return Project{}, err
	}
	for _, op := range req.Operations {
		commands, err := json.Marshal([]Command{op})
		if err != nil {
			return Project{}, err
		}
		_, err = tx.Exec(ctx, `INSERT INTO project_commands (project_id,revision_id,operation_id,commands)
			VALUES ($1,$2,$3,$4)`, projectID, revisionID, op.OperationID, commands)
		if err != nil {
			return Project{}, err
		}
	}
	err = tx.QueryRow(ctx, `UPDATE projects SET title=$3, head_revision_id=$2, lock_version=lock_version+1, updated_at=now()
		WHERE id=$1 RETURNING lock_version,updated_at`, projectID, revisionID, doc.Manifest.Title).Scan(&p.LockVersion, &p.UpdatedAt)
	if err != nil {
		return Project{}, err
	}
	p.Title, p.HeadRevisionID, p.Manifest = doc.Manifest.Title, revisionID, manifest
	if err := completeIdempotent(ctx, tx, actor, endpoint, key, p, 200); err != nil {
		return Project{}, err
	}
	return p, tx.Commit(ctx)
}

func nullableUUID(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func beginIdempotent(ctx context.Context, tx pgx.Tx, actor, endpoint, key string, requestHash [32]byte) ([]byte, error) {
	_, err := tx.Exec(ctx, `INSERT INTO idempotency_keys (actor_id,endpoint,key,request_hash)
		VALUES ($1,$2,$3,$4) ON CONFLICT DO NOTHING`, actor, endpoint, key, requestHash[:])
	if err != nil {
		return nil, err
	}
	var storedHash []byte
	var response []byte
	err = tx.QueryRow(ctx, `SELECT request_hash,response_body FROM idempotency_keys
		WHERE actor_id=$1 AND endpoint=$2 AND key=$3 FOR UPDATE`, actor, endpoint, key).Scan(&storedHash, &response)
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(storedHash, requestHash[:]) {
		return nil, ErrIdempotencyConflict
	}
	return response, nil
}

func (s Store) UpdateTitle(ctx context.Context, actor, projectID, key string, requestHash [32]byte, req MetadataUpdate) (Project, error) {
	if err := req.Validate(); err != nil {
		return Project{}, err
	}
	current, err := s.Get(ctx, actor, projectID)
	if err != nil {
		return Project{}, err
	}
	if current.LockVersion != req.LockVersion {
		return Project{}, &ConflictError{CurrentRevisionID: current.HeadRevisionID}
	}
	operationID, err := NewUUID()
	if err != nil {
		return Project{}, err
	}
	value, err := json.Marshal(req.Title)
	if err != nil {
		return Project{}, err
	}
	return s.Change(ctx, actor, projectID, "metadata:"+key, requestHash, ChangeRequest{
		BaseRevisionID: current.HeadRevisionID,
		Operations:     []Command{{OperationID: operationID, Type: "SetProjectSetting", Setting: "title", Value: value}},
	})
}

func (s Store) SoftDelete(ctx context.Context, actor, projectID, key string, requestHash [32]byte, lockVersion int64) error {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	endpoint := "/api/v1/projects/" + projectID
	if cached, err := beginIdempotent(ctx, tx, actor, endpoint+":delete", key, requestHash); err != nil {
		return err
	} else if cached != nil {
		return nil
	}
	var currentLock int64
	err = tx.QueryRow(ctx, `SELECT lock_version FROM projects WHERE id=$1 AND owner_id=$2 AND deleted_at IS NULL FOR UPDATE`, projectID, actor).Scan(&currentLock)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if currentLock != lockVersion {
		return ErrConflict
	}
	_, err = tx.Exec(ctx, `UPDATE projects SET deleted_at=now(), updated_at=now() WHERE id=$1`, projectID)
	if err != nil {
		return err
	}
	if err := completeIdempotent(ctx, tx, actor, endpoint+":delete", key, Project{ID: projectID}, 200); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s Store) Restore(ctx context.Context, actor, projectID, key string, requestHash [32]byte) (Project, error) {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return Project{}, err
	}
	defer tx.Rollback(ctx)
	endpoint := "/api/v1/projects/" + projectID + "/restore"
	if cached, err := beginIdempotent(ctx, tx, actor, endpoint, key, requestHash); err != nil {
		return Project{}, err
	} else if cached != nil {
		var p Project
		if err := json.Unmarshal(cached, &p); err != nil {
			return Project{}, err
		}
		return p, nil
	}
	tag, err := tx.Exec(ctx, `UPDATE projects SET deleted_at=NULL, updated_at=now() WHERE id=$1 AND owner_id=$2 AND deleted_at IS NOT NULL`, projectID, actor)
	if err != nil {
		return Project{}, err
	}
	if tag.RowsAffected() == 0 {
		return Project{}, ErrNotFound
	}
	var p Project
	err = tx.QueryRow(ctx, `SELECT p.id,p.title,p.mode,p.head_revision_id,p.lock_version,
		r.schema_version,r.manifest,p.created_at,p.updated_at
		FROM projects p JOIN project_revisions r ON r.id=p.head_revision_id
		WHERE p.id=$1 AND p.owner_id=$2 AND p.deleted_at IS NULL`, projectID, actor).Scan(
		&p.ID, &p.Title, &p.Mode, &p.HeadRevisionID, &p.LockVersion, &p.SchemaVersion, &p.Manifest, &p.CreatedAt, &p.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Project{}, ErrNotFound
	}
	if err != nil {
		return Project{}, err
	}
	if err := completeIdempotent(ctx, tx, actor, endpoint, key, p, 200); err != nil {
		return Project{}, err
	}
	return p, tx.Commit(ctx)
}

func (s Store) ListRevisions(ctx context.Context, actor, projectID, cursor string, limit int) ([]Revision, string, error) {
	if _, err := s.Get(ctx, actor, projectID); err != nil {
		return nil, "", err
	}
	rows, err := s.DB.Query(ctx, `SELECT id,parent_id,schema_version,content_hash,source,created_at
		FROM project_revisions WHERE project_id=$1 AND ($2::uuid IS NULL OR id>$2::uuid)
		ORDER BY id LIMIT $3`, projectID, nullableUUID(cursor), limit+1)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	items := make([]Revision, 0, limit)
	for rows.Next() {
		var item Revision
		if err := rows.Scan(&item.ID, &item.ParentID, &item.SchemaVersion, &item.ContentHash, &item.Source, &item.CreatedAt); err != nil {
			return nil, "", err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	next := ""
	if len(items) > limit {
		items = items[:limit]
		next = items[len(items)-1].ID
	}
	return items, next, nil
}

func (s Store) GetRevision(ctx context.Context, actor, projectID, revisionID string) (Revision, error) {
	if _, err := s.Get(ctx, actor, projectID); err != nil {
		return Revision{}, err
	}
	var item Revision
	err := s.DB.QueryRow(ctx, `SELECT id,parent_id,schema_version,content_hash,source,created_at,manifest
		FROM project_revisions WHERE project_id=$1 AND id=$2`, projectID, revisionID).Scan(
		&item.ID, &item.ParentID, &item.SchemaVersion, &item.ContentHash, &item.Source, &item.CreatedAt, &item.Manifest)
	if errors.Is(err, pgx.ErrNoRows) {
		return Revision{}, ErrNotFound
	}
	return item, err
}

func completeIdempotent(ctx context.Context, tx pgx.Tx, actor, endpoint, key string, response Project, status int) error {
	data, err := json.Marshal(response)
	if err != nil {
		return fmt.Errorf("marshal idempotency response: %w", err)
	}
	_, err = tx.Exec(ctx, `UPDATE idempotency_keys SET response_body=$4,response_status=$5
		WHERE actor_id=$1 AND endpoint=$2 AND key=$3`, actor, endpoint, key, data, status)
	return err
}
