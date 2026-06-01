package agent

import (
	"context"
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
