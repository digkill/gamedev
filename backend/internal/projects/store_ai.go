package projects

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
)

func (s Store) InsertGeneration(ctx context.Context, actor, projectID, generationID, baseRevisionID, prompt, model string) error {
	_, err := s.DB.Exec(ctx, `INSERT INTO ai_generations
		(id,project_id,owner_id,base_revision_id,status,prompt,model)
		VALUES ($1,$2,$3,$4,'generating',$5,$6)`, generationID, projectID, actor, baseRevisionID, prompt, model)
	return err
}

func (s Store) FinishGeneration(ctx context.Context, generationID, status, summary, errorCode string, operations []Command) error {
	var raw []byte
	if operations != nil {
		var err error
		raw, err = json.Marshal(operations)
		if err != nil {
			return err
		}
	}
	_, err := s.DB.Exec(ctx, `UPDATE ai_generations
		SET status=$2, summary=$3, error_code=$4, operations=$5, updated_at=now()
		WHERE id=$1`, generationID, status, nullableString(summary), nullableString(errorCode), raw)
	return err
}

func (s Store) HasActiveGeneration(ctx context.Context, actor, projectID string) (bool, error) {
	var n int
	err := s.DB.QueryRow(ctx, `SELECT count(*) FROM ai_generations
		WHERE owner_id=$1 AND project_id=$2
		AND status IN ('queued','planning','generating','validating','repairing')
		AND created_at > now() - interval '10 minutes'`, actor, projectID).Scan(&n)
	return n > 0, err
}

func (s Store) ApplyAI(ctx context.Context, actor, projectID, key string, requestHash [32]byte, req ChangeRequest) (Project, error) {
	return s.applyOps(ctx, actor, projectID, key, requestHash, "/api/v1/projects/"+projectID+"/ai/generations", req, "ai")
}

func (s Store) CountOwnerActive(ctx context.Context, actor string) (int, error) {
	var n int
	err := s.DB.QueryRow(ctx, `SELECT count(*) FROM ai_generations
		WHERE owner_id=$1
		AND status IN ('queued','planning','generating','validating','repairing')
		AND created_at > now() - interval '10 minutes'`, actor).Scan(&n)
	if err == pgx.ErrNoRows {
		return 0, nil
	}
	return n, err
}

func nullableString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// ReplaceDocument stores a complete new document as the next revision of an
// existing project. The agent conveyor regenerates a whole game rather than
// editing it command by command, and this keeps that result inside the same
// project history instead of creating a second project.
func (s Store) ReplaceDocument(ctx context.Context, actor, projectID, key string, requestHash [32]byte, document json.RawMessage) (Project, error) {
	manifest, contentHash, err := CanonicalManifest(document)
	if err != nil {
		return Project{}, err
	}
	doc, err := validateProject(manifest)
	if err != nil {
		return Project{}, err
	}
	if doc.Manifest.ProjectID != projectID {
		return Project{}, ErrInvalid
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return Project{}, err
	}
	defer tx.Rollback(ctx)
	endpoint := "/api/v1/projects/" + projectID + "/ai/pipeline"
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
		p.created_at,p.updated_at,r.schema_version
		FROM projects p JOIN project_revisions r ON r.id=p.head_revision_id
		WHERE p.id=$1 AND p.owner_id=$2 AND p.deleted_at IS NULL FOR UPDATE OF p`, projectID, actor).Scan(
		&p.ID, &p.Title, &p.Mode, &p.HeadRevisionID, &p.LockVersion, &p.CreatedAt, &p.UpdatedAt, &p.SchemaVersion)
	if errors.Is(err, pgx.ErrNoRows) {
		return Project{}, ErrNotFound
	}
	if err != nil {
		return Project{}, err
	}
	if p.Mode != doc.Manifest.Mode {
		return Project{}, ErrInvalid
	}
	revisionID, err := NewUUID()
	if err != nil {
		return Project{}, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO project_revisions
		(id,project_id,parent_id,schema_version,manifest,content_hash,author_id,source)
		VALUES ($1,$2,$3,$4,$5,$6,$7,'ai')`,
		revisionID, projectID, p.HeadRevisionID, doc.Manifest.SchemaVersion, manifest, contentHash, actor)
	if err != nil {
		return Project{}, err
	}
	err = tx.QueryRow(ctx, `UPDATE projects SET title=$3, head_revision_id=$2,
		lock_version=lock_version+1, updated_at=now()
		WHERE id=$1 RETURNING lock_version,updated_at`,
		projectID, revisionID, doc.Manifest.Title).Scan(&p.LockVersion, &p.UpdatedAt)
	if err != nil {
		return Project{}, err
	}
	p.Title, p.HeadRevisionID, p.Manifest, p.SchemaVersion = doc.Manifest.Title, revisionID, manifest, doc.Manifest.SchemaVersion
	if err := completeIdempotent(ctx, tx, actor, endpoint, key, p, 200); err != nil {
		return Project{}, err
	}
	return p, tx.Commit(ctx)
}
