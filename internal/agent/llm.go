package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

type OpenAICompatibleLLMClient struct {
	client  *http.Client
	apiKey  string
	baseURL string
	model   string
}

func NewOpenAICompatibleLLMClient(client *http.Client, apiKey, baseURL, model string) *OpenAICompatibleLLMClient {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		baseURL = "https://api.openai.com"
	}
	model = strings.TrimSpace(model)
	if model == "" {
		model = "gpt-4o-mini"
	}
	return &OpenAICompatibleLLMClient{
		client:  client,
		apiKey:  strings.TrimSpace(apiKey),
		baseURL: baseURL,
		model:   model,
	}
}

func (c *OpenAICompatibleLLMClient) Complete(ctx context.Context, req LLMRequest) (LLMResponse, error) {
	if c.apiKey == "" {
		return LLMResponse{}, errors.New("OPENAI_API_KEY is required for AI conversation runs")
	}
	client := c.client
	if client == nil {
		client = http.DefaultClient
	}

	body, err := json.Marshal(chatCompletionRequest{
		Model:    c.model,
		Messages: chatCompletionMessages(req),
		Stream:   req.Stream,
	})
	if err != nil {
		return LLMResponse{}, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return LLMResponse{}, err
	}
	httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(httpReq)
	if err != nil {
		return LLMResponse{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBody, err := io.ReadAll(resp.Body)
		if err != nil {
			return LLMResponse{}, err
		}
		var parsed chatCompletionResponse
		_ = json.Unmarshal(respBody, &parsed)
		if parsed.Error != nil && parsed.Error.Message != "" {
			return LLMResponse{}, fmt.Errorf("chat completion HTTP %d: %s", resp.StatusCode, parsed.Error.Message)
		}
		return LLMResponse{}, fmt.Errorf("chat completion HTTP %d: %s", resp.StatusCode, string(respBody))
	}
	if req.Stream {
		return parseChatCompletionStream(resp.Body)
	}

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return LLMResponse{}, err
	}

	var parsed chatCompletionResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return LLMResponse{}, fmt.Errorf("parse chat completion response: %w; raw=%s", err, string(respBody))
	}
	if len(parsed.Choices) == 0 {
		return LLMResponse{}, errors.New("chat completion response has no choices")
	}

	return LLMResponse{Content: strings.TrimSpace(parsed.Choices[0].Message.Content)}, nil
}

func parseChatCompletionStream(body io.Reader) (LLMResponse, error) {
	scanner := bufio.NewScanner(body)
	// Some providers emit larger JSON chunks than the default 64 KiB scanner cap.
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	var builder strings.Builder
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, ":") {
			continue
		}
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			return LLMResponse{Content: strings.TrimSpace(builder.String())}, nil
		}
		var event chatCompletionStreamEvent
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			return LLMResponse{}, fmt.Errorf("parse chat completion stream event: %w; raw=%s", err, data)
		}
		for _, choice := range event.Choices {
			builder.WriteString(choice.Delta.Content)
		}
	}
	if err := scanner.Err(); err != nil {
		return LLMResponse{}, err
	}
	content := strings.TrimSpace(builder.String())
	if content == "" {
		return LLMResponse{}, errors.New("chat completion stream produced no content")
	}
	return LLMResponse{Content: content}, nil
}

func chatCompletionMessages(req LLMRequest) []chatCompletionMessage {
	if len(req.Messages) > 0 {
		messages := make([]chatCompletionMessage, 0, len(req.Messages))
		for _, message := range req.Messages {
			if strings.TrimSpace(message.Content) == "" {
				continue
			}
			messages = append(messages, chatCompletionMessage{Role: message.Role, Content: message.Content})
		}
		return messages
	}
	messages := make([]chatCompletionMessage, 0, 2)
	if strings.TrimSpace(req.SystemPrompt) != "" {
		messages = append(messages, chatCompletionMessage{Role: "system", Content: req.SystemPrompt})
	}
	if strings.TrimSpace(req.UserPrompt) != "" {
		messages = append(messages, chatCompletionMessage{Role: "user", Content: req.UserPrompt})
	}
	return messages
}

type chatCompletionRequest struct {
	Model    string                  `json:"model"`
	Messages []chatCompletionMessage `json:"messages"`
	Stream   bool                    `json:"stream,omitempty"`
}

type chatCompletionMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatCompletionResponse struct {
	Choices []struct {
		Message chatCompletionMessage `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type,omitempty"`
	} `json:"error,omitempty"`
}

type chatCompletionStreamEvent struct {
	Choices []struct {
		Delta chatCompletionMessage `json:"delta"`
	} `json:"choices"`
}
