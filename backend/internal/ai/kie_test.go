package ai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// newTestKie builds a client with streaming off, which is the simpler path to
// assert against. The streaming tests below opt back in.
func newTestKie(t *testing.T, handler http.Handler) *KieChat {
	t.Helper()
	return newTestKieStreaming(t, handler, false)
}

func newTestKieStreaming(t *testing.T, handler http.Handler, streaming bool) *KieChat {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	chat := NewKieChat(KieSettings{
		APIKey: "test-key", BaseURL: server.URL,
		Model: "claude-opus-5", FallbackModel: "gpt-5-6-terra",
		ReasoningEffort: "low", MaxTokens: 2000, Timeout: 5 * time.Second,
		DisableStreaming: !streaming,
	})
	chat.retryPause = 0
	return chat
}

func TestKieUsesClaudeFirst(t *testing.T) {
	var seen []string
	chat := newTestKie(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.URL.Path)
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("missing bearer token, got %q", got)
		}
		body, _ := io.ReadAll(r.Body)
		var payload struct {
			Model    string `json:"model"`
			System   string `json:"system"`
			Messages []struct {
				Role    string `json:"role"`
				Content []struct {
					Text string `json:"text"`
				} `json:"content"`
			} `json:"messages"`
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Errorf("request is not JSON: %v", err)
		}
		if payload.Model != "claude-opus-5" || payload.System != "system text" {
			t.Errorf("unexpected payload: %s", body)
		}
		writeTestJSON(w, map[string]any{
			"content": []map[string]any{{"type": "text", "text": `{"ok":true}`}},
			"model":   "claude-opus-5-20260101",
			"usage":   map[string]any{"input_tokens": 12, "output_tokens": 3},
		})
	}))
	response, err := chat.Chat(context.Background(), ChatRequest{System: "system text", User: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	if response.Provider != "kie/claude" || response.Model != "claude-opus-5-20260101" {
		t.Fatalf("unexpected response: %+v", response)
	}
	if response.InputTokens != 12 || response.OutputTokens != 3 {
		t.Fatalf("usage not read: %+v", response)
	}
	if len(seen) != 1 || seen[0] != "/claude/v1/messages" {
		t.Fatalf("unexpected call path: %v", seen)
	}
}

// A model that the account cannot reach must not fail the request: the GPT
// fallback answers instead.
func TestKieFallsBackToCodex(t *testing.T) {
	var seen []string
	chat := newTestKie(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.URL.Path)
		if r.URL.Path == "/claude/v1/messages" {
			http.Error(w, `{"error":{"type":"not_found_error","message":"model not found"}}`, http.StatusNotFound)
			return
		}
		body, _ := io.ReadAll(r.Body)
		var payload struct {
			Model string `json:"model"`
			Input []struct {
				Content []struct {
					Text string `json:"text"`
				} `json:"content"`
			} `json:"input"`
			Reasoning struct {
				Effort string `json:"effort"`
			} `json:"reasoning"`
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Errorf("request is not JSON: %v", err)
		}
		if payload.Model != "gpt-5-6-terra" || payload.Reasoning.Effort != "low" {
			t.Errorf("unexpected fallback payload: %s", body)
		}
		// The system text must survive the switch to the Responses schema.
		if len(payload.Input) == 0 || !strings.Contains(payload.Input[0].Content[0].Text, "system text") {
			t.Errorf("system prompt lost in fallback: %s", body)
		}
		writeTestJSON(w, map[string]any{
			"output": []map[string]any{
				{"type": "reasoning", "content": []map[string]any{{"type": "output_text", "text": "ignored"}}},
				{"type": "message", "content": []map[string]any{{"type": "output_text", "text": `{"ok":true}`}}},
			},
			"usage": map[string]any{"input_tokens": 5, "output_tokens": 7},
		})
	}))
	response, err := chat.Chat(context.Background(), ChatRequest{System: "system text", User: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	if response.Provider != "kie/codex" || response.Model != "gpt-5-6-terra" {
		t.Fatalf("unexpected response: %+v", response)
	}
	if response.Text != `{"ok":true}` {
		t.Fatalf("reasoning block must not leak into the text: %q", response.Text)
	}
	if len(seen) != 2 || seen[1] != "/codex/v1/responses" {
		t.Fatalf("unexpected call paths: %v", seen)
	}
}

// The gateway fails intermittently under load, so a 5xx is retried with a
// growing pause before the fallback model is tried at all.
func TestKieRetriesServerErrorsBeforeFallback(t *testing.T) {
	attempts := 0
	chat := newTestKie(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/claude/v1/messages" {
			attempts++
			http.Error(w, "upstream busy", http.StatusBadGateway)
			return
		}
		writeTestJSON(w, map[string]any{
			"output": []map[string]any{{"type": "message", "content": []map[string]any{{"type": "output_text", "text": "{}"}}}},
		})
	}))
	if _, err := chat.Chat(context.Background(), ChatRequest{User: "hi"}); err != nil {
		t.Fatal(err)
	}
	if attempts != 3 {
		t.Fatalf("a 502 must be retried twice, got %d attempts", attempts)
	}
}

func TestKieReportsProviderErrorWhenBothFail(t *testing.T) {
	chat := newTestKie(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusForbidden)
	}))
	if _, err := chat.Chat(context.Background(), ChatRequest{User: "hi"}); err != ErrProvider {
		t.Fatalf("got %v", err)
	}
}

func TestExtractJSONHandlesFencesAndProse(t *testing.T) {
	cases := map[string]string{
		"{\"a\":1}":               `{"a":1}`,
		"```json\n{\"a\":1}\n```": `{"a":1}`,
		"Вот результат:\n{\"a\":1}\nготово": `{"a":1}`,
		"{\"text\":\"}\"}":                     `{"text":"}"}`,
		"```\n{\"nested\":{\"b\":[1,2]}}\n```": `{"nested":{"b":[1,2]}}`,
	}
	for input, want := range cases {
		got, ok := ExtractJSON(input)
		if !ok || string(got) != want {
			t.Fatalf("ExtractJSON(%q) = %q, %v; want %q", input, got, ok, want)
		}
	}
	if _, ok := ExtractJSON("никакого json здесь нет"); ok {
		t.Fatal("prose without JSON must not parse")
	}
}

func writeTestJSON(w http.ResponseWriter, payload map[string]any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(payload)
}

func sse(w http.ResponseWriter, frames ...string) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)
	for _, frame := range frames {
		_, _ = w.Write([]byte("data: " + frame + "\n\n"))
		if flusher != nil {
			flusher.Flush()
		}
	}
}

// Streaming is the default because a long generation is otherwise cut off by
// the gateway, so the delta frames must be reassembled exactly.
func TestKieStreamsClaudeDeltas(t *testing.T) {
	chat := newTestKieStreaming(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), `"stream":true`) {
			t.Errorf("streaming must be requested: %s", body)
		}
		sse(w,
			`{"type":"message_start","message":{"model":"claude-opus-5-20260101","usage":{"input_tokens":40}}}`,
			`{"type":"content_block_start","content_block":{"type":"text","text":"{\"le"}}`,
			`{"type":"content_block_delta","delta":{"type":"text_delta","text":"vels\":"}}`,
			`{"type":"content_block_delta","delta":{"type":"text_delta","text":"[1,2]}"}}`,
			`{"type":"message_delta","usage":{"output_tokens":11}}`,
			`{"type":"message_stop"}`,
		)
	}), true)
	response, err := chat.Chat(context.Background(), ChatRequest{User: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	if response.Text != `{"levels":[1,2]}` {
		t.Fatalf("stream not reassembled: %q", response.Text)
	}
	if response.Model != "claude-opus-5-20260101" || response.InputTokens != 40 || response.OutputTokens != 11 {
		t.Fatalf("stream metadata lost: %+v", response)
	}
}

func TestKieStreamsCodexDeltas(t *testing.T) {
	chat := newTestKieStreaming(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/claude/v1/messages" {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		sse(w,
			`{"type":"response.output_text.delta","delta":"{\"ok\":"}`,
			`{"type":"response.output_text.delta","delta":"true}"}`,
			`{"type":"response.completed","response":{"usage":{"input_tokens":9,"output_tokens":4}}}`,
			`[DONE]`,
		)
	}), true)
	response, err := chat.Chat(context.Background(), ChatRequest{User: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	if response.Text != `{"ok":true}` || response.Provider != "kie/codex" {
		t.Fatalf("unexpected stream response: %+v", response)
	}
	if response.InputTokens != 9 || response.OutputTokens != 4 {
		t.Fatalf("usage lost: %+v", response)
	}
}

// Some gateways send only a final frame for a short answer.
func TestKieStreamFallsBackToCompletedFrame(t *testing.T) {
	chat := newTestKieStreaming(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/claude/v1/messages" {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		sse(w, `{"type":"response.completed","response":{"output":[{"type":"reasoning","content":[{"type":"output_text","text":"skip"}]},{"type":"message","content":[{"type":"output_text","text":"{\"a\":1}"}]}]}}`)
	}), true)
	response, err := chat.Chat(context.Background(), ChatRequest{User: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	if response.Text != `{"a":1}` {
		t.Fatalf("final frame not used: %q", response.Text)
	}
}

// A gateway that rejects the streaming form must not cost the primary model:
// the same request is repeated without streaming before the fallback runs.
func TestKieRetriesWithoutStreamingBeforeFallback(t *testing.T) {
	var paths []string
	chat := newTestKieStreaming(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		streaming := strings.Contains(string(body), `"stream":true`)
		paths = append(paths, r.URL.Path+map[bool]string{true: " stream", false: " plain"}[streaming])
		if streaming {
			http.Error(w, "streaming not supported", http.StatusBadRequest)
			return
		}
		writeTestJSON(w, map[string]any{
			"content": []map[string]any{{"type": "text", "text": `{"ok":true}`}},
			"model":   "claude-opus-5",
		})
	}), true)
	response, err := chat.Chat(context.Background(), ChatRequest{User: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	if response.Provider != "kie/claude" {
		t.Fatalf("the primary model must still answer: %+v", response)
	}
	if len(paths) != 2 || paths[0] != "/claude/v1/messages stream" || paths[1] != "/claude/v1/messages plain" {
		t.Fatalf("unexpected attempts: %v", paths)
	}
}
