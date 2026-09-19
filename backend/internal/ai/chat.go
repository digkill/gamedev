package ai

import (
	"context"
	"encoding/json"
	"strings"
)

// ChatRequest is one turn of instruction plus user text. Every agent in the
// pipeline talks to a model through this shape, so the provider can change
// without touching agent code.
type ChatRequest struct {
	System string
	User   string
	// JSON asks the provider for a single JSON object. Providers that cannot
	// enforce it rely on the instruction text and on ExtractJSON below.
	JSON      bool
	MaxTokens int
	Effort    string
}

type ChatResponse struct {
	Text         string
	Provider     string
	Model        string
	InputTokens  int
	OutputTokens int
}

type Chat interface {
	Configured() bool
	Chat(ctx context.Context, req ChatRequest) (ChatResponse, error)
}

type unavailableChat struct{}

func (unavailableChat) Configured() bool { return false }

func (unavailableChat) Chat(context.Context, ChatRequest) (ChatResponse, error) {
	return ChatResponse{}, ErrUnavailable
}

// ExtractJSON pulls the first complete JSON object out of a model reply. Models
// wrap JSON in fences or add a sentence before it often enough that trimming
// alone is not reliable.
func ExtractJSON(text string) (json.RawMessage, bool) {
	trimmed := strings.TrimSpace(text)
	if fenced, ok := stripFence(trimmed); ok {
		trimmed = fenced
	}
	if json.Valid([]byte(trimmed)) && strings.HasPrefix(trimmed, "{") {
		return json.RawMessage(trimmed), true
	}
	start := strings.Index(trimmed, "{")
	if start < 0 {
		return nil, false
	}
	depth, inString, escaped := 0, false, false
	for i := start; i < len(trimmed); i++ {
		c := trimmed[i]
		switch {
		case escaped:
			escaped = false
		case c == '\\' && inString:
			escaped = true
		case c == '"':
			inString = !inString
		case inString:
		case c == '{':
			depth++
		case c == '}':
			depth--
			if depth == 0 {
				candidate := trimmed[start : i+1]
				if json.Valid([]byte(candidate)) {
					return json.RawMessage(candidate), true
				}
				return nil, false
			}
		}
	}
	return nil, false
}

func stripFence(text string) (string, bool) {
	if !strings.HasPrefix(text, "```") {
		return text, false
	}
	rest := text[3:]
	if newline := strings.IndexByte(rest, '\n'); newline >= 0 {
		rest = rest[newline+1:]
	}
	if end := strings.LastIndex(rest, "```"); end >= 0 {
		rest = rest[:end]
	}
	return strings.TrimSpace(rest), true
}
