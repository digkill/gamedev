package projects

import (
	"bytes"
	"encoding/json"
	"strings"
)

func NormalizeCommand(op Command) Command {
	op.OperationID = strings.TrimSpace(op.OperationID)
	op.Type = strings.TrimSpace(op.Type)
	op.SceneID = strings.TrimSpace(op.SceneID)
	op.EntityID = strings.TrimSpace(op.EntityID)
	op.Property = strings.TrimSpace(op.Property)
	op.Setting = strings.TrimSpace(op.Setting)
	op.Scene = emptyJSON(op.Scene)
	op.Entity = emptyJSON(op.Entity)
	op.Component = emptyJSON(op.Component)
	op.Value = emptyJSON(op.Value)
	op.Rules = emptyJSON(op.Rules)
	return op
}

func emptyJSON(raw json.RawMessage) json.RawMessage {
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "null" || s == "{}" || s == "[]" || s == `""` {
		return nil
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil
	}
	return raw
}
