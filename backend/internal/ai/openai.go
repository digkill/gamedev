package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// openAIChat talks to the OpenAI Responses API directly. It stays for
// deployments that hold an OpenAI key and do not route through kie.ai.
type openAIChat struct {
	key    string
	model  string
	url    string
	client *http.Client
}

func newOpenAIChat(apiKey, model string) *openAIChat {
	return &openAIChat{
		key:    strings.TrimSpace(apiKey),
		model:  strings.TrimSpace(model),
		url:    "https://api.openai.com/v1/responses",
		client: &http.Client{Timeout: 120 * time.Second},
	}
}

func (o *openAIChat) Configured() bool { return o != nil && o.key != "" && o.model != "" }

func (o *openAIChat) PrimaryModel() string { return o.model }

func (o *openAIChat) Chat(ctx context.Context, req ChatRequest) (ChatResponse, error) {
	if !o.Configured() {
		return ChatResponse{}, ErrUnavailable
	}
	payload := map[string]any{
		"model": o.model,
		"input": req.User,
	}
	if system := strings.TrimSpace(req.System); system != "" {
		payload["instructions"] = system
	}
	if req.JSON {
		payload["text"] = map[string]any{"format": map[string]any{"type": "json_object"}}
	}
	if req.MaxTokens > 0 {
		payload["max_output_tokens"] = req.MaxTokens
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return ChatResponse{}, ErrProvider
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, o.url, bytes.NewReader(body))
	if err != nil {
		return ChatResponse{}, ErrProvider
	}
	httpReq.Header.Set("Authorization", "Bearer "+o.key)
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := o.client.Do(httpReq)
	if err != nil {
		return ChatResponse{}, ErrProvider
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return ChatResponse{}, ErrProvider
	}
	if resp.StatusCode >= 400 {
		var apiErr struct {
			Error struct {
				Type string `json:"type"`
				Code string `json:"code"`
			} `json:"error"`
		}
		_ = json.Unmarshal(raw, &apiErr)
		slog.Warn("openai request failed", "status", resp.StatusCode, "type", apiErr.Error.Type, "code", apiErr.Error.Code)
		return ChatResponse{}, ErrProvider
	}
	text, usage, err := outputText(raw)
	if err != nil {
		return ChatResponse{}, ErrFailed
	}
	return ChatResponse{
		Text:         text,
		Provider:     "openai",
		Model:        o.model,
		InputTokens:  usage[0],
		OutputTokens: usage[1],
	}, nil
}

func outputText(raw []byte) (string, [2]int, error) {
	var envelope struct {
		OutputText string `json:"output_text"`
		Output     []struct {
			Type    string `json:"type"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"output"`
		Usage struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	}
	if json.Unmarshal(raw, &envelope) != nil {
		return "", [2]int{}, ErrFailed
	}
	usage := [2]int{envelope.Usage.InputTokens, envelope.Usage.OutputTokens}
	if strings.TrimSpace(envelope.OutputText) != "" {
		return envelope.OutputText, usage, nil
	}
	var b strings.Builder
	for _, item := range envelope.Output {
		if item.Type == "reasoning" {
			continue
		}
		for _, part := range item.Content {
			if part.Type == "output_text" || part.Type == "text" {
				b.WriteString(part.Text)
			}
		}
	}
	if b.Len() == 0 {
		return "", usage, errors.New("empty model output")
	}
	return b.String(), usage, nil
}
