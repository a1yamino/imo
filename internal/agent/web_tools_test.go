package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSerperWebSearchToolMapsOrganicResults(t *testing.T) {
	var gotKey string
	var gotRequest struct {
		Query string `json:"q"`
		Num   int    `json:"num"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get("X-API-KEY")
		if r.URL.Path != "/search" {
			t.Fatalf("path=%s, want /search", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&gotRequest); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"organic": [
				{"title": "First", "link": "https://example.com/first", "snippet": "One"},
				{"title": "Second", "link": "https://example.com/second", "snippet": "Two"}
			]
		}`))
	}))
	defer server.Close()

	registry := NewToolRegistry()
	RegisterSerperWebTools(registry, SerperConfig{
		APIKey:    "secret",
		SearchURL: server.URL + "/search",
		Client:    server.Client(),
	})

	tool, ok := registry.Get("web.search")
	if !ok {
		t.Fatal("web.search not registered")
	}
	result, err := tool.Execute(context.Background(), ToolRequest{
		Arguments: map[string]any{"query": "agent runtime", "max_results": 2},
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if gotKey != "secret" {
		t.Fatalf("X-API-KEY=%q, want secret", gotKey)
	}
	if gotRequest.Query != "agent runtime" || gotRequest.Num != 2 {
		t.Fatalf("request=%+v", gotRequest)
	}
	var payload struct {
		Query   string `json:"query"`
		Results []struct {
			Title   string `json:"title"`
			URL     string `json:"url"`
			Snippet string `json:"snippet"`
			Source  string `json:"source"`
		} `json:"results"`
	}
	if err := json.Unmarshal([]byte(result.JSON), &payload); err != nil {
		t.Fatalf("parse result: %v", err)
	}
	if len(payload.Results) != 2 {
		t.Fatalf("results=%d, want 2", len(payload.Results))
	}
	if payload.Results[0].URL != "https://example.com/first" || payload.Results[0].Source != "serper" {
		t.Fatalf("first result=%+v", payload.Results[0])
	}
}

func TestWebFetchToolFetchesHTTPContentAndLimitsText(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/page" {
			t.Fatalf("path=%s, want /page", r.URL.Path)
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`<html><head><title>Example</title><meta name="description" content="Demo page"></head><body><h1>Hello</h1><script>ignore()</script><p>abcdefghijklmnopqrstuvwxyz</p></body></html>`))
	}))
	defer server.Close()

	registry := NewToolRegistry()
	RegisterWebFetchTool(registry, server.Client())

	tool, ok := registry.Get("web.fetch")
	if !ok {
		t.Fatal("web.fetch not registered")
	}
	result, err := tool.Execute(context.Background(), ToolRequest{
		Arguments: map[string]any{"url": server.URL + "/page", "max_chars": 10},
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var payload struct {
		URL         string `json:"url"`
		Title       string `json:"title"`
		Text        string `json:"text"`
		Truncated   bool   `json:"truncated"`
		Description string `json:"description"`
	}
	if err := json.Unmarshal([]byte(result.JSON), &payload); err != nil {
		t.Fatalf("parse result: %v", err)
	}
	if payload.URL != server.URL+"/page" || payload.Title != "Example" {
		t.Fatalf("payload url/title=%+v", payload)
	}
	if payload.Text != "Hello abcd" || !payload.Truncated {
		t.Fatalf("text=%q truncated=%v", payload.Text, payload.Truncated)
	}
	if payload.Description != "Demo page" {
		t.Fatalf("description=%q", payload.Description)
	}
}
