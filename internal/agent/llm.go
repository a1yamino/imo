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
	"sort"
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

	request := chatCompletionRequest{
		Model:      c.model,
		Messages:   chatCompletionMessages(req),
		Tools:      chatCompletionTools(req.Tools),
		ToolChoice: chatCompletionToolChoice(req.Tools),
		Stream:     req.Stream,
	}
	if req.Stream && req.Usage {
		request.StreamOptions = &chatCompletionStreamOptions{IncludeUsage: true}
	}
	body, err := json.Marshal(request)
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
		return parseChatCompletionStream(resp.Body, req.OnDelta)
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

	message := parsed.Choices[0].Message
	toolCalls, err := llmToolCalls(message.ToolCalls)
	if err != nil {
		return LLMResponse{}, err
	}
	return LLMResponse{Content: strings.TrimSpace(message.Content), ToolCalls: toolCalls, Usage: llmUsage(parsed.Usage)}, nil
}

func parseChatCompletionStream(body io.Reader, onDelta func(LLMDelta)) (LLMResponse, error) {
	scanner := bufio.NewScanner(body)
	// Some providers emit larger JSON chunks than the default 64 KiB scanner cap.
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	var builder strings.Builder
	streamToolCalls := map[int]*chatCompletionStreamToolCall{}
	var usage *chatCompletionUsage
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
			return finishChatCompletionStream(builder.String(), streamToolCalls, usage)
		}
		var event chatCompletionStreamEvent
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			return LLMResponse{}, fmt.Errorf("parse chat completion stream event: %w; raw=%s", err, data)
		}
		if event.Usage != nil {
			usage = event.Usage
		}
		for _, choice := range event.Choices {
			if choice.Delta.Content != "" {
				builder.WriteString(choice.Delta.Content)
				if onDelta != nil {
					onDelta(LLMDelta{Content: choice.Delta.Content})
				}
			}
			for _, call := range choice.Delta.ToolCalls {
				current := streamToolCalls[call.Index]
				if current == nil {
					current = &chatCompletionStreamToolCall{Index: call.Index}
					streamToolCalls[call.Index] = current
				}
				if call.ID != "" {
					current.ID = call.ID
				}
				if call.Type != "" {
					current.Type = call.Type
				}
				if call.Function.Name != "" {
					current.Function.Name += call.Function.Name
				}
				if call.Function.Arguments != "" {
					current.Function.Arguments += call.Function.Arguments
				}
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return LLMResponse{}, err
	}
	return finishChatCompletionStream(builder.String(), streamToolCalls, usage)
}

func finishChatCompletionStream(content string, streamToolCalls map[int]*chatCompletionStreamToolCall, usage *chatCompletionUsage) (LLMResponse, error) {
	content = strings.TrimSpace(content)
	toolCalls, err := llmStreamToolCalls(streamToolCalls)
	if err != nil {
		return LLMResponse{}, err
	}
	if content == "" && len(toolCalls) == 0 {
		return LLMResponse{}, errors.New("chat completion stream produced no content")
	}
	return LLMResponse{Content: content, ToolCalls: toolCalls, Usage: llmUsage(usage)}, nil
}

func chatCompletionMessages(req LLMRequest) []chatCompletionMessage {
	if len(req.Messages) > 0 {
		messages := make([]chatCompletionMessage, 0, len(req.Messages))
		for _, message := range req.Messages {
			if strings.TrimSpace(message.Content) == "" && len(message.ToolCalls) == 0 {
				continue
			}
			messages = append(messages, chatCompletionMessage{
				Role:       message.Role,
				Content:    message.Content,
				ToolCallID: message.ToolCallID,
				ToolCalls:  chatCompletionToolCalls(message.ToolCalls),
			})
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

func chatCompletionTools(tools []LLMToolSpec) []chatCompletionTool {
	if len(tools) == 0 {
		return nil
	}
	result := make([]chatCompletionTool, 0, len(tools))
	for _, tool := range tools {
		if strings.TrimSpace(tool.Name) == "" {
			continue
		}
		parameters := tool.Parameters
		if parameters == nil {
			parameters = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		result = append(result, chatCompletionTool{
			Type: "function",
			Function: chatCompletionFunctionTool{
				Name:        toolAPIFunctionName(tool.Name),
				Description: tool.Description,
				Parameters:  parameters,
			},
		})
	}
	return result
}

func chatCompletionToolChoice(tools []LLMToolSpec) any {
	if len(tools) == 0 {
		return nil
	}
	return "auto"
}

func chatCompletionToolCalls(calls []LLMToolCall) []chatCompletionToolCall {
	if len(calls) == 0 {
		return nil
	}
	result := make([]chatCompletionToolCall, 0, len(calls))
	for _, call := range calls {
		arguments, err := json.Marshal(call.Arguments)
		if err != nil {
			arguments = []byte("{}")
		}
		result = append(result, chatCompletionToolCall{
			ID:   call.ID,
			Type: "function",
			Function: chatCompletionToolCallFunction{
				Name:      toolAPIFunctionName(call.Name),
				Arguments: string(arguments),
			},
		})
	}
	return result
}

func llmToolCalls(calls []chatCompletionToolCall) ([]LLMToolCall, error) {
	if len(calls) == 0 {
		return nil, nil
	}
	result := make([]LLMToolCall, 0, len(calls))
	for _, call := range calls {
		if call.Type != "" && call.Type != "function" {
			continue
		}
		var arguments map[string]any
		rawArgs := strings.TrimSpace(call.Function.Arguments)
		if rawArgs == "" {
			arguments = map[string]any{}
		} else if err := json.Unmarshal([]byte(rawArgs), &arguments); err != nil {
			return nil, fmt.Errorf("parse tool call arguments for %s: %w", call.Function.Name, err)
		}
		result = append(result, LLMToolCall{
			ID:        call.ID,
			Name:      toolInternalName(call.Function.Name),
			Arguments: arguments,
		})
	}
	return result, nil
}

func llmStreamToolCalls(calls map[int]*chatCompletionStreamToolCall) ([]LLMToolCall, error) {
	if len(calls) == 0 {
		return nil, nil
	}
	indexes := make([]int, 0, len(calls))
	for index := range calls {
		indexes = append(indexes, index)
	}
	sort.Ints(indexes)
	result := make([]LLMToolCall, 0, len(indexes))
	for _, index := range indexes {
		call := calls[index]
		parsed, err := llmToolCalls([]chatCompletionToolCall{{
			ID:       call.ID,
			Type:     call.Type,
			Function: call.Function,
		}})
		if err != nil {
			return nil, err
		}
		result = append(result, parsed...)
	}
	return result, nil
}

func llmUsage(usage *chatCompletionUsage) *LLMUsage {
	if usage == nil {
		return nil
	}
	return &LLMUsage{
		PromptTokens:     usage.PromptTokens,
		CompletionTokens: usage.CompletionTokens,
		TotalTokens:      usage.TotalTokens,
		CachedTokens:     usage.PromptTokensDetails.CachedTokens,
		ReasoningTokens:  usage.CompletionTokensDetails.ReasoningTokens,
	}
}

func toolAPIFunctionName(name string) string {
	return strings.ReplaceAll(name, ".", "__")
}

func toolInternalName(name string) string {
	return strings.ReplaceAll(name, "__", ".")
}

type chatCompletionRequest struct {
	Model         string                       `json:"model"`
	Messages      []chatCompletionMessage      `json:"messages"`
	Tools         []chatCompletionTool         `json:"tools,omitempty"`
	ToolChoice    any                          `json:"tool_choice,omitempty"`
	Stream        bool                         `json:"stream,omitempty"`
	StreamOptions *chatCompletionStreamOptions `json:"stream_options,omitempty"`
}

type chatCompletionStreamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type chatCompletionMessage struct {
	Role       string                   `json:"role"`
	Content    string                   `json:"content,omitempty"`
	ToolCallID string                   `json:"tool_call_id,omitempty"`
	ToolCalls  []chatCompletionToolCall `json:"tool_calls,omitempty"`
}

type chatCompletionTool struct {
	Type     string                     `json:"type"`
	Function chatCompletionFunctionTool `json:"function"`
}

type chatCompletionFunctionTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters,omitempty"`
}

type chatCompletionToolCall struct {
	ID       string                         `json:"id"`
	Type     string                         `json:"type"`
	Function chatCompletionToolCallFunction `json:"function"`
}

type chatCompletionToolCallFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type chatCompletionResponse struct {
	Choices []struct {
		Message chatCompletionMessage `json:"message"`
	} `json:"choices"`
	Usage *chatCompletionUsage `json:"usage,omitempty"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type,omitempty"`
	} `json:"error,omitempty"`
}

type chatCompletionStreamEvent struct {
	Choices []struct {
		Delta chatCompletionStreamDelta `json:"delta"`
	} `json:"choices"`
	Usage *chatCompletionUsage `json:"usage,omitempty"`
}

type chatCompletionUsage struct {
	PromptTokens        int `json:"prompt_tokens"`
	CompletionTokens    int `json:"completion_tokens"`
	TotalTokens         int `json:"total_tokens"`
	PromptTokensDetails struct {
		CachedTokens int `json:"cached_tokens"`
	} `json:"prompt_tokens_details,omitempty"`
	CompletionTokensDetails struct {
		ReasoningTokens int `json:"reasoning_tokens"`
	} `json:"completion_tokens_details,omitempty"`
}

type chatCompletionStreamDelta struct {
	Content   string                         `json:"content"`
	ToolCalls []chatCompletionStreamToolCall `json:"tool_calls"`
}

type chatCompletionStreamToolCall struct {
	Index    int                            `json:"index"`
	ID       string                         `json:"id"`
	Type     string                         `json:"type"`
	Function chatCompletionToolCallFunction `json:"function"`
}
