package projects

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	ErrNotFound            = errors.New("project not found")
	ErrConflict            = errors.New("revision conflict")
	ErrIdempotencyConflict = errors.New("idempotency conflict")
	ErrInvalid             = errors.New("invalid project request")
	ErrAIUnavailable       = errors.New("ai unavailable")
	ErrAIFailed            = errors.New("ai failed")
	ErrAIProvider          = errors.New("ai provider failed")
)

type Project struct {
	ID             string          `json:"id"`
	Title          string          `json:"title"`
	Mode           string          `json:"mode"`
	HeadRevisionID string          `json:"head_revision_id"`
	LockVersion    int64           `json:"lock_version"`
	SchemaVersion  int             `json:"schema_version,omitempty"`
	Manifest       json.RawMessage `json:"manifest,omitempty"`
	CreatedAt      time.Time       `json:"created_at"`
	UpdatedAt      time.Time       `json:"updated_at"`
}

type CreateRequest struct {
	Title         string          `json:"title"`
	Mode          string          `json:"mode"`
	SchemaVersion int             `json:"schema_version"`
	Manifest      json.RawMessage `json:"manifest"`
}

type Command struct {
	OperationID string          `json:"operation_id"`
	Type        string          `json:"type"`
	Scene       json.RawMessage `json:"scene"`
	SceneID     string          `json:"scene_id"`
	Entity      json.RawMessage `json:"entity"`
	EntityID    string          `json:"entity_id"`
	Component   json.RawMessage `json:"component"`
	Property    string          `json:"property"`
	Value       json.RawMessage `json:"value"`
	Setting     string          `json:"setting"`
	Rules       json.RawMessage `json:"rules"`
}

type ChangeRequest struct {
	BaseRevisionID string    `json:"base_revision_id"`
	Operations     []Command `json:"operations"`
}

type GenerateRequest struct {
	Prompt         string `json:"prompt"`
	BaseRevisionID string `json:"base_revision_id"`
}

type GenerateResponse struct {
	GenerationID string  `json:"generation_id"`
	Status       string  `json:"status"`
	Summary      string  `json:"summary"`
	Project      Project `json:"project"`
}

func (r GenerateRequest) Validate() error {
	n := len([]rune(strings.TrimSpace(r.Prompt)))
	if n < 1 || n > 10000 || !IsUUID(r.BaseRevisionID) {
		return ErrInvalid
	}
	return nil
}

type MetadataUpdate struct {
	Title       string `json:"title"`
	LockVersion int64  `json:"lock_version"`
}

type Revision struct {
	ID            string          `json:"id"`
	ParentID      *string         `json:"parent_id,omitempty"`
	SchemaVersion int             `json:"schema_version"`
	ContentHash   string          `json:"content_hash"`
	Source        string          `json:"source"`
	CreatedAt     time.Time       `json:"created_at"`
	Manifest      json.RawMessage `json:"manifest,omitempty"`
}

func (r CreateRequest) Validate() error {
	if strings.TrimSpace(r.Title) == "" || len([]rune(r.Title)) > 120 || (r.Mode != "2d" && r.Mode != "3d") || r.SchemaVersion != 1 {
		return ErrInvalid
	}
	doc, err := validateProject(r.Manifest)
	if err != nil || doc.Manifest.Title != r.Title || doc.Manifest.Mode != r.Mode || doc.Manifest.SchemaVersion != r.SchemaVersion {
		return ErrInvalid
	}
	return nil
}

func (r ChangeRequest) Validate() error {
	if !IsUUID(r.BaseRevisionID) || len(r.Operations) < 1 || len(r.Operations) > 100 {
		return ErrInvalid
	}
	seen := make(map[string]bool, len(r.Operations))
	for _, op := range r.Operations {
		if seen[op.OperationID] || validateCommand(op) != nil {
			return ErrInvalid
		}
		seen[op.OperationID] = true
	}
	return nil
}

func (r MetadataUpdate) Validate() error {
	if !shortName(r.Title, 120) || r.LockVersion < 0 {
		return ErrInvalid
	}
	return nil
}

func validateManifest(raw json.RawMessage) error {
	_, err := validateProject(raw)
	return err
}

func CanonicalManifest(raw json.RawMessage) ([]byte, string, error) {
	if err := validateManifest(raw); err != nil {
		return nil, "", err
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, "", ErrInvalid
	}
	canonical, err := json.Marshal(value)
	if err != nil {
		return nil, "", fmt.Errorf("canonical manifest: %w", err)
	}
	hash := sha256.Sum256(canonical)
	return canonical, hex.EncodeToString(hash[:]), nil
}

func NewUUID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	s := hex.EncodeToString(b[:])
	return fmt.Sprintf("%s-%s-%s-%s-%s", s[:8], s[8:12], s[12:16], s[16:20], s[20:]), nil
}

func IsUUID(s string) bool {
	if len(s) != 36 || s[8] != '-' || s[13] != '-' || s[18] != '-' || s[23] != '-' {
		return false
	}
	for i, c := range s {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			continue
		}
		if !strings.ContainsRune("0123456789abcdefABCDEF", c) {
			return false
		}
	}
	return true
}
