package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOpenAICompatibleLLMClientRequiresAPIKey(t *testing.T) {
	client := NewOpenAICompatibleLLMClient(nil, "", "https://example.test", "test-model")

	_, err := client.Complete(context.Background(), LLMRequest{UserPrompt: "hello"})
	if err == nil {
		t.Fatal("Complete returned nil error without API key")
	}
	if !strings.Contains(err.Error(), "OPENAI_API_KEY") {
		t.Fatalf("error=%q, want mention OPENAI_API_KEY", err.Error())
	}
}

func TestOpenAICompatibleLLMClientSendsStreamAndParsesDeltas(t *testing.T) {
	var requestBody chatCompletionRequest
	var deltas []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"hello \"}}]}\n\n"))
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"world\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: {\"choices\":[],\"usage\":{\"prompt_tokens\":7,\"completion_tokens\":2,\"total_tokens\":9,\"prompt_tokens_details\":{\"cached_tokens\":3},\"completion_tokens_details\":{\"reasoning_tokens\":1}}}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	client := NewOpenAICompatibleLLMClient(server.Client(), "key", server.URL, "test-model")
	response, err := client.Complete(context.Background(), LLMRequest{
		UserPrompt: "hello",
		Stream:     true,
		Usage:      true,
		OnDelta: func(delta LLMDelta) {
			deltas = append(deltas, delta.Content)
		},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}

	if !requestBody.Stream {
		t.Fatalf("request stream=false, want true")
	}
	if requestBody.StreamOptions == nil || !requestBody.StreamOptions.IncludeUsage {
		t.Fatalf("request stream_options=%+v, want include_usage=true", requestBody.StreamOptions)
	}
	if response.Content != "hello world" {
		t.Fatalf("content=%q, want hello world", response.Content)
	}
	if strings.Join(deltas, "") != "hello world" {
		t.Fatalf("deltas=%q, want hello world", strings.Join(deltas, ""))
	}
	if response.Usage == nil {
		t.Fatal("usage=nil, want token usage from stream")
	}
	if response.Usage.PromptTokens != 7 || response.Usage.CompletionTokens != 2 || response.Usage.TotalTokens != 9 {
		t.Fatalf("usage=%+v, want prompt=7 completion=2 total=9", response.Usage)
	}
	if response.Usage.CachedTokens != 3 || response.Usage.ReasoningTokens != 1 {
		t.Fatalf("usage details=%+v, want cached=3 reasoning=1", response.Usage)
	}
}

func TestOpenAICompatibleLLMClientParsesNonStreamingUsage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"choices": [{
				"message": {
					"role": "assistant",
					"content": "{\"type\":\"final\",\"content\":\"ok\"}"
				}
			}],
			"usage": {
				"prompt_tokens": 11,
				"completion_tokens": 5,
				"total_tokens": 16,
				"prompt_tokens_details": {"cached_tokens": 4},
				"completion_tokens_details": {"reasoning_tokens": 2}
			}
		}`))
	}))
	defer server.Close()

	client := NewOpenAICompatibleLLMClient(server.Client(), "key", server.URL, "test-model")
	response, err := client.Complete(context.Background(), LLMRequest{UserPrompt: "hello"})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if response.Usage == nil {
		t.Fatal("usage=nil, want token usage")
	}
	if response.Usage.PromptTokens != 11 || response.Usage.CompletionTokens != 5 || response.Usage.TotalTokens != 16 {
		t.Fatalf("usage=%+v, want prompt=11 completion=5 total=16", response.Usage)
	}
	if response.Usage.CachedTokens != 4 || response.Usage.ReasoningTokens != 2 {
		t.Fatalf("usage details=%+v, want cached=4 reasoning=2", response.Usage)
	}
}

func TestOpenAICompatibleLLMClientParsesStreamingToolCalls(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"type\":\"function\",\"function\":{\"name\":\"web__search\",\"arguments\":\"{\\\"query\\\":\"}}]}}]}\n\n"))
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"\\\"2026 Cologne Major\\\"}\"}}]}}]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	client := NewOpenAICompatibleLLMClient(server.Client(), "key", server.URL, "test-model")
	response, err := client.Complete(context.Background(), LLMRequest{
		UserPrompt: "search",
		Stream:     true,
		Tools: []LLMToolSpec{{
			Name:        "web.search",
			Description: "Search web.",
			Parameters:  map[string]any{"type": "object", "properties": map[string]any{}},
		}},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}

	if response.Content != "" {
		t.Fatalf("content=%q, want empty content for tool call", response.Content)
	}
	if len(response.ToolCalls) != 1 {
		t.Fatalf("tool calls=%d, want 1", len(response.ToolCalls))
	}
	call := response.ToolCalls[0]
	if call.ID != "call_1" || call.Name != "web.search" || call.Arguments["query"] != "2026 Cologne Major" {
		t.Fatalf("tool call=%+v", call)
	}
}

func TestOpenAICompatibleLLMClientSendsNativeToolsAndParsesToolCalls(t *testing.T) {
	var requestBody chatCompletionRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"choices": [{
				"message": {
					"role": "assistant",
					"tool_calls": [{
						"id": "call_1",
						"type": "function",
						"function": {
							"name": "filesystem__list_dir",
							"arguments": "{\"path\":\".\"}"
						}
					}]
				}
			}]
		}`))
	}))
	defer server.Close()

	client := NewOpenAICompatibleLLMClient(server.Client(), "key", server.URL, "test-model")
	response, err := client.Complete(context.Background(), LLMRequest{
		UserPrompt: "list files",
		Tools: []LLMToolSpec{{
			Name:        "filesystem.list_dir",
			Description: "List files.",
			Parameters: map[string]any{
				"type":       "object",
				"properties": map[string]any{"path": map[string]any{"type": "string"}},
			},
		}},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}

	if len(requestBody.Tools) != 1 {
		t.Fatalf("tools=%d, want 1", len(requestBody.Tools))
	}
	if requestBody.Tools[0].Function.Name != "filesystem__list_dir" {
		t.Fatalf("tool name=%q, want API-safe name", requestBody.Tools[0].Function.Name)
	}
	if requestBody.ToolChoice != "auto" {
		t.Fatalf("tool_choice=%v, want auto", requestBody.ToolChoice)
	}
	if len(response.ToolCalls) != 1 {
		t.Fatalf("tool calls=%d, want 1", len(response.ToolCalls))
	}
	call := response.ToolCalls[0]
	if call.ID != "call_1" || call.Name != "filesystem.list_dir" || call.Arguments["path"] != "." {
		t.Fatalf("tool call=%+v", call)
	}
}
