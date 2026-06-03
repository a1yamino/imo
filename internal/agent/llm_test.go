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
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"hello \"}}]}\n\n"))
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"world\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	client := NewOpenAICompatibleLLMClient(server.Client(), "key", server.URL, "test-model")
	response, err := client.Complete(context.Background(), LLMRequest{UserPrompt: "hello", Stream: true})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}

	if !requestBody.Stream {
		t.Fatalf("request stream=false, want true")
	}
	if response.Content != "hello world" {
		t.Fatalf("content=%q, want hello world", response.Content)
	}
}
