package httpapi

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRootAnswersBrowsersAndClients(t *testing.T) {
	handler := API{}.Handler()

	browser := httptest.NewRequest(http.MethodGet, "/", nil)
	browser.Header.Set("Accept", "text/html,application/xhtml+xml")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, browser)
	if rec.Code != http.StatusOK {
		t.Fatalf("browser status %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "GameDev API работает") {
		t.Fatalf("browser body %q", rec.Body.String())
	}

	client := httptest.NewRequest(http.MethodGet, "/", nil)
	client.Header.Set("Accept", "application/json")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, client)
	if rec.Code != http.StatusOK {
		t.Fatalf("client status %d", rec.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["status"] != "ok" || body["service"] != "gamedev" {
		t.Fatalf("client body %#v", body)
	}

	missing := httptest.NewRequest(http.MethodGet, "/no-such-page", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, missing)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown path status %d", rec.Code)
	}
	raw, _ := io.ReadAll(rec.Body)
	if !strings.Contains(string(raw), "404") {
		t.Fatalf("unknown path body %q", raw)
	}
}
