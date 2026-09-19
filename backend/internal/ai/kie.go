package ai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// KieSettings mirrors the KIE_* environment block.
type KieSettings struct {
	APIKey          string
	BaseURL         string
	Model           string
	FallbackModel   string
	ReasoningEffort string
	MaxTokens       int
	Timeout         time.Duration
	// DisableStreaming turns off Server-Sent Events. Streaming is on by
	// default because the gateway drops a long non-streaming request before a
	// large generation finishes.
	DisableStreaming bool
}

// KieChat calls kie.ai. The primary model is a Claude model served through the
// Anthropic Messages schema at /claude/v1/messages. When that model is
// unavailable — no credit for it, model retired, provider error — the request
// is repeated against the GPT fallback at /codex/v1/responses, which speaks the
// Responses schema instead. Both are reached with the same API key.
type KieChat struct {
	settings KieSettings
	client   *http.Client
	// retryPause is the base of the backoff between attempts. Tests set it to
	// zero so the suite does not sleep through the real delays.
	retryPause time.Duration
}

func NewKieChat(settings KieSettings) *KieChat {
	if strings.TrimSpace(settings.APIKey) == "" {
		return nil
	}
	if settings.BaseURL == "" {
		settings.BaseURL = "https://api.kie.ai"
	}
	settings.BaseURL = strings.TrimRight(settings.BaseURL, "/")
	if settings.Model == "" {
		settings.Model = "claude-opus-5"
	}
	if settings.FallbackModel == "" {
		settings.FallbackModel = "gpt-5-6-terra"
	}
	if settings.ReasoningEffort == "" {
		settings.ReasoningEffort = "low"
	}
	if settings.MaxTokens <= 0 {
		settings.MaxTokens = 16000
	}
	if settings.Timeout <= 0 {
		settings.Timeout = 180 * time.Second
	}
	return &KieChat{
		settings:   settings,
		client:     &http.Client{Timeout: settings.Timeout},
		retryPause: 2 * time.Second,
	}
}

func (k *KieChat) Configured() bool { return k != nil && k.settings.APIKey != "" }

func (k *KieChat) PrimaryModel() string  { return k.settings.Model }
func (k *KieChat) FallbackModel() string { return k.settings.FallbackModel }

func (k *KieChat) Chat(ctx context.Context, req ChatRequest) (ChatResponse, error) {
	if !k.Configured() {
		return ChatResponse{}, ErrUnavailable
	}
	response, err := k.claude(ctx, req)
	if err == nil {
		return response, nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return ChatResponse{}, err
	}
	slog.Warn("kie primary model unavailable, switching to fallback",
		"primary", k.settings.Model, "fallback", k.settings.FallbackModel, "error", err)
	response, fallbackErr := k.codex(ctx, req)
	if fallbackErr != nil {
		slog.Error("kie fallback model failed", "model", k.settings.FallbackModel, "error", fallbackErr)
		return ChatResponse{}, ErrProvider
	}
	return response, nil
}

func (k *KieChat) claude(ctx context.Context, req ChatRequest) (ChatResponse, error) {
	maxTokens := req.MaxTokens
	if maxTokens <= 0 || maxTokens > k.settings.MaxTokens {
		maxTokens = k.settings.MaxTokens
	}
	stream := !k.settings.DisableStreaming
	payload := map[string]any{
		"model":      k.settings.Model,
		"max_tokens": maxTokens,
		"stream":     stream,
		"messages": []map[string]any{{
			"role":    "user",
			"content": []map[string]any{{"type": "text", "text": req.User}},
		}},
	}
	if system := strings.TrimSpace(req.System); system != "" {
		payload["system"] = system
	}
	if stream {
		response, err := k.claudeStream(ctx, payload)
		if err == nil || ctx.Err() != nil {
			return response, err
		}
		// A gateway that rejects the streaming form still answers the plain
		// one, so one non-streaming attempt comes before the fallback model.
		slog.Warn("kie streaming attempt failed, retrying without streaming", "path", "/claude/v1/messages", "error", err)
		payload["stream"] = false
	}
	raw, err := k.post(ctx, "/claude/v1/messages", payload)
	if err != nil {
		return ChatResponse{}, err
	}
	var envelope struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		Model string `json:"model"`
		Usage struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
		Error *struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return ChatResponse{}, fmt.Errorf("kie claude response is not JSON: %w", err)
	}
	if envelope.Error != nil {
		return ChatResponse{}, fmt.Errorf("kie claude error: %s", envelope.Error.Type)
	}
	var text strings.Builder
	for _, block := range envelope.Content {
		if block.Type == "text" {
			text.WriteString(block.Text)
		}
	}
	if strings.TrimSpace(text.String()) == "" {
		return ChatResponse{}, errors.New("kie claude returned no text")
	}
	model := envelope.Model
	if model == "" {
		model = k.settings.Model
	}
	return ChatResponse{
		Text:         text.String(),
		Provider:     "kie/claude",
		Model:        model,
		InputTokens:  envelope.Usage.InputTokens,
		OutputTokens: envelope.Usage.OutputTokens,
	}, nil
}

func (k *KieChat) codex(ctx context.Context, req ChatRequest) (ChatResponse, error) {
	effort := req.Effort
	if effort == "" {
		effort = k.settings.ReasoningEffort
	}
	// The Responses schema on kie.ai documents only model and input, so the
	// system text is prepended to the user turn rather than sent as its own
	// field that the gateway might reject.
	text := req.User
	if system := strings.TrimSpace(req.System); system != "" {
		text = system + "\n\n---\n\n" + req.User
	}
	stream := !k.settings.DisableStreaming
	payload := map[string]any{
		"model":  k.settings.FallbackModel,
		"stream": stream,
		"input": []map[string]any{{
			"role":    "user",
			"content": []map[string]any{{"type": "input_text", "text": text}},
		}},
		"reasoning": map[string]any{"effort": effort},
	}
	if stream {
		response, err := k.codexStream(ctx, payload)
		if err == nil || ctx.Err() != nil {
			return response, err
		}
		slog.Warn("kie streaming attempt failed, retrying without streaming", "path", "/codex/v1/responses", "error", err)
		payload["stream"] = false
	}
	raw, err := k.post(ctx, "/codex/v1/responses", payload)
	if err != nil {
		return ChatResponse{}, err
	}
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
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return ChatResponse{}, fmt.Errorf("kie codex response is not JSON: %w", err)
	}
	var out strings.Builder
	out.WriteString(envelope.OutputText)
	for _, item := range envelope.Output {
		if item.Type == "reasoning" {
			continue
		}
		for _, block := range item.Content {
			if block.Type == "output_text" || block.Type == "text" {
				out.WriteString(block.Text)
			}
		}
	}
	if strings.TrimSpace(out.String()) == "" {
		return ChatResponse{}, errors.New("kie codex returned no text")
	}
	return ChatResponse{
		Text:         out.String(),
		Provider:     "kie/codex",
		Model:        k.settings.FallbackModel,
		InputTokens:  envelope.Usage.InputTokens,
		OutputTokens: envelope.Usage.OutputTokens,
	}, nil
}

// post retries a rate limit or a server-side failure with a growing pause.
// The gateway fails intermittently under load, and a second or third attempt
// usually lands. Client errors are returned immediately: repeating them only
// burns time before the fallback model.
func (k *KieChat) post(ctx context.Context, path string, payload map[string]any) ([]byte, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(time.Duration(attempt*attempt+1) * k.retryPause):
			}
		}
		raw, status, err := k.once(ctx, path, body)
		if err != nil {
			lastErr = err
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return nil, err
			}
			continue
		}
		if status < 400 {
			return raw, nil
		}
		lastErr = fmt.Errorf("kie %s returned %d: %s", path, status, snippet(raw))
		if status != 429 && status < 500 {
			return nil, lastErr
		}
	}
	return nil, lastErr
}

func (k *KieChat) once(ctx context.Context, path string, body []byte) ([]byte, int, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, k.settings.BaseURL+path, bytes.NewReader(body))
	if err != nil {
		return nil, 0, err
	}
	request.Header.Set("Authorization", "Bearer "+k.settings.APIKey)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	response, err := k.client.Do(request)
	if err != nil {
		return nil, 0, err
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 8<<20))
	if err != nil {
		return nil, response.StatusCode, err
	}
	return raw, response.StatusCode, nil
}

// snippet keeps provider error text short and free of newlines for logs; it is
// never shown to an API client.
func snippet(raw []byte) string {
	text := strings.Join(strings.Fields(string(raw)), " ")
	if len(text) > 300 {
		text = text[:300]
	}
	return text
}

// Streaming
//
// A long generation is the normal case for the developer agent, and the
// gateway closes a non-streaming request that takes more than about two
// minutes. Server-Sent Events keep data flowing, so the same generation
// completes instead of being reset mid-answer.

func (k *KieChat) claudeStream(ctx context.Context, payload map[string]any) (ChatResponse, error) {
	response := ChatResponse{Provider: "kie/claude", Model: k.settings.Model}
	var text strings.Builder
	err := k.stream(ctx, "/claude/v1/messages", payload, func(event []byte) error {
		var frame struct {
			Type  string `json:"type"`
			Delta struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"delta"`
			ContentBlock struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content_block"`
			Message struct {
				Model string `json:"model"`
				Usage struct {
					InputTokens  int `json:"input_tokens"`
					OutputTokens int `json:"output_tokens"`
				} `json:"usage"`
			} `json:"message"`
			Usage struct {
				InputTokens  int `json:"input_tokens"`
				OutputTokens int `json:"output_tokens"`
			} `json:"usage"`
			Error *struct {
				Type    string `json:"type"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(event, &frame) != nil {
			return nil
		}
		if frame.Error != nil {
			return fmt.Errorf("kie claude stream error: %s", frame.Error.Type)
		}
		switch frame.Type {
		case "message_start":
			if frame.Message.Model != "" {
				response.Model = frame.Message.Model
			}
			response.InputTokens = frame.Message.Usage.InputTokens
		case "content_block_start":
			text.WriteString(frame.ContentBlock.Text)
		case "content_block_delta":
			if frame.Delta.Type == "text_delta" || frame.Delta.Type == "" {
				text.WriteString(frame.Delta.Text)
			}
		case "message_delta":
			if frame.Usage.OutputTokens > 0 {
				response.OutputTokens = frame.Usage.OutputTokens
			}
		}
		return nil
	})
	if err != nil {
		return ChatResponse{}, err
	}
	if strings.TrimSpace(text.String()) == "" {
		return ChatResponse{}, errors.New("kie claude stream returned no text")
	}
	response.Text = text.String()
	return response, nil
}

func (k *KieChat) codexStream(ctx context.Context, payload map[string]any) (ChatResponse, error) {
	response := ChatResponse{Provider: "kie/codex", Model: k.settings.FallbackModel}
	var text strings.Builder
	var complete string
	err := k.stream(ctx, "/codex/v1/responses", payload, func(event []byte) error {
		var frame struct {
			Type     string `json:"type"`
			Delta    string `json:"delta"`
			Text     string `json:"text"`
			Response struct {
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
			} `json:"response"`
		}
		if json.Unmarshal(event, &frame) != nil {
			return nil
		}
		switch frame.Type {
		case "response.output_text.delta":
			text.WriteString(frame.Delta)
		case "response.output_text.done":
			if text.Len() == 0 {
				text.WriteString(frame.Text)
			}
		case "response.completed", "response.done":
			response.InputTokens = frame.Response.Usage.InputTokens
			response.OutputTokens = frame.Response.Usage.OutputTokens
			var whole strings.Builder
			whole.WriteString(frame.Response.OutputText)
			for _, item := range frame.Response.Output {
				if item.Type == "reasoning" {
					continue
				}
				for _, block := range item.Content {
					if block.Type == "output_text" || block.Type == "text" {
						whole.WriteString(block.Text)
					}
				}
			}
			complete = whole.String()
		case "response.failed", "error":
			return errors.New("kie codex stream reported a failure")
		}
		return nil
	})
	if err != nil {
		return ChatResponse{}, err
	}
	// The deltas are the source of truth; the final frame is used only when no
	// delta arrived, which some gateways do for short answers.
	result := text.String()
	if strings.TrimSpace(result) == "" {
		result = complete
	}
	if strings.TrimSpace(result) == "" {
		return ChatResponse{}, errors.New("kie codex stream returned no text")
	}
	response.Text = result
	return response, nil
}

// stream posts the request and hands each SSE data payload to onEvent. It does
// not retry: a stream that fails halfway has already produced partial output,
// and the caller's fallback is the safer response.
func (k *KieChat) stream(ctx context.Context, path string, payload map[string]any, onEvent func([]byte) error) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, k.settings.BaseURL+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+k.settings.APIKey)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "text/event-stream")
	response, err := k.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode >= 400 {
		raw, _ := io.ReadAll(io.LimitReader(response.Body, 64<<10))
		return fmt.Errorf("kie %s returned %d: %s", path, response.StatusCode, snippet(raw))
	}
	reader := bufio.NewReaderSize(io.LimitReader(response.Body, 64<<20), 64<<10)
	var data strings.Builder
	for {
		line, err := reader.ReadString('\n')
		if len(line) > 0 {
			trimmed := strings.TrimRight(line, "\r\n")
			switch {
			case trimmed == "":
				if data.Len() > 0 {
					payload := strings.TrimSpace(data.String())
					data.Reset()
					if payload != "[DONE]" {
						if eventErr := onEvent([]byte(payload)); eventErr != nil {
							return eventErr
						}
					}
				}
			case strings.HasPrefix(trimmed, "data:"):
				if data.Len() > 0 {
					data.WriteString("\n")
				}
				data.WriteString(strings.TrimSpace(strings.TrimPrefix(trimmed, "data:")))
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return err
		}
	}
	if data.Len() > 0 {
		payload := strings.TrimSpace(data.String())
		if payload != "[DONE]" {
			return onEvent([]byte(payload))
		}
	}
	return nil
}
