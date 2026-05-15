package main

import (
	"bufio"
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/joho/godotenv"
)

type appConfig struct {
	APIKey  string
	BaseURL string
	Model   string
	Addr    string
}

type message struct {
	Role             string `json:"role"`
	Content          string `json:"content"`
	ReasoningContent string `json:"reasoning_content,omitempty"`
}

type chatRequest struct {
	Model           string    `json:"model"`
	Messages        []message `json:"messages"`
	ReasoningEffort string    `json:"reasoning_effort,omitempty"`
	Stream          bool      `json:"stream,omitempty"`
}

type chatResponse struct {
	Choices []struct {
		Message struct {
			Role             string          `json:"role"`
			Content          string          `json:"content"`
			ReasoningContent string          `json:"reasoning_content,omitempty"`
			Reasoning        json.RawMessage `json:"reasoning,omitempty"`
		} `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type,omitempty"`
	} `json:"error,omitempty"`
}

type chatStreamChunk struct {
	Choices []struct {
		Delta struct {
			Role             string          `json:"role"`
			Content          string          `json:"content"`
			ReasoningContent string          `json:"reasoning_content,omitempty"`
			Reasoning        json.RawMessage `json:"reasoning,omitempty"`
		} `json:"delta"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type,omitempty"`
	} `json:"error,omitempty"`
}

type chatAPIRequest struct {
	Messages        []message `json:"messages"`
	ReasoningEffort string    `json:"reasoning_effort,omitempty"`
	Model           string    `json:"model,omitempty"`
}

type streamEvent struct {
	Type    string `json:"type"`
	Delta   string `json:"delta,omitempty"`
	Message string `json:"message,omitempty"`
}

type modelsResponse struct {
	Data []struct {
		ID string `json:"id"`
	} `json:"data"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type,omitempty"`
	} `json:"error,omitempty"`
}

type modelsAPIResponse struct {
	DefaultModel string   `json:"default_model"`
	Models       []string `json:"models"`
}

type chatServer struct {
	config appConfig
	client *http.Client
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run() error {
	if err := godotenv.Load(); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}

	cfg, err := loadConfig()
	if err != nil {
		return err
	}

	server := &chatServer{
		config: cfg,
		client: &http.Client{Timeout: 60 * time.Second},
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", server.index)
	mux.HandleFunc("/api/chat", server.chat)
	mux.HandleFunc("/api/models", server.models)

	fmt.Printf("OpenAI 兼容多轮对话网页已启动: http://localhost%s\n", cfg.Addr)
	fmt.Printf("model=%s base_url=%s\n", cfg.Model, cfg.BaseURL)
	return http.ListenAndServe(cfg.Addr, mux)
}

func loadConfig() (appConfig, error) {
	apiKey := strings.TrimSpace(os.Getenv("OPENAI_API_KEY"))
	if apiKey == "" {
		return appConfig{}, errors.New("请先设置 OPENAI_API_KEY")
	}

	baseURL := strings.TrimRight(strings.TrimSpace(os.Getenv("OPENAI_BASE_URL")), "/")
	if baseURL == "" {
		baseURL = "https://api.openai.com"
	}

	model := strings.TrimSpace(os.Getenv("OPENAI_MODEL"))
	if model == "" {
		model = "gpt-4o-mini"
	}

	port := strings.TrimSpace(os.Getenv("PORT"))
	if port == "" {
		port = "8080"
	}

	return appConfig{
		APIKey:  apiKey,
		BaseURL: baseURL,
		Model:   model,
		Addr:    ":" + strings.TrimPrefix(port, ":"),
	}, nil
}

func (s *chatServer) index(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, pageHTML)
}

func (s *chatServer) chat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req chatAPIRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	if len(req.Messages) == 0 {
		http.Error(w, "messages is required", http.StatusBadRequest)
		return
	}

	reasoningEffort := strings.TrimSpace(req.ReasoningEffort)
	if !isValidReasoningEffort(reasoningEffort) {
		http.Error(w, "reasoning_effort must be low, medium, high, or empty", http.StatusBadRequest)
		return
	}

	model := strings.TrimSpace(req.Model)
	if model == "" {
		model = s.config.Model
	}

	messages := []message{
		{Role: "system", Content: "You are a helpful assistant."},
	}
	for _, msg := range req.Messages {
		role := strings.TrimSpace(msg.Role)
		content := strings.TrimSpace(msg.Content)
		if content == "" {
			continue
		}
		if role != "user" && role != "assistant" {
			http.Error(w, "role must be user or assistant", http.StatusBadRequest)
			return
		}
		messages = append(messages, message{Role: role, Content: content})
	}
	if len(messages) == 1 {
		http.Error(w, "messages is required", http.StatusBadRequest)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	emit := func(event streamEvent) error {
		return writeSSE(w, flusher, event)
	}
	if err := streamChatCompletion(r.Context(), s.client, s.config.BaseURL, s.config.APIKey, model, messages, reasoningEffort, emit); err != nil {
		emit(streamEvent{Type: "error", Message: err.Error()})
		return
	}
	emit(streamEvent{Type: "done"})
}

func (s *chatServer) models(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	models, err := listModels(r.Context(), s.client, s.config.BaseURL, s.config.APIKey)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(w).Encode(modelsAPIResponse{
		DefaultModel: s.config.Model,
		Models:       models,
	})
}

func listModels(ctx context.Context, client *http.Client, baseURL, apiKey string) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/v1/models", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var parsed modelsResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return nil, fmt.Errorf("解析模型列表失败: %w; raw=%s", err, string(respBody))
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if parsed.Error != nil && parsed.Error.Message != "" {
			return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, parsed.Error.Message)
		}
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(respBody))
	}

	models := make([]string, 0, len(parsed.Data))
	seen := make(map[string]struct{}, len(parsed.Data))
	for _, item := range parsed.Data {
		id := strings.TrimSpace(item.ID)
		if id == "" {
			continue
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		models = append(models, id)
	}
	sort.Strings(models)

	return models, nil
}

func writeSSE(w io.Writer, flusher http.Flusher, event streamEvent) error {
	data, err := json.Marshal(event)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "data: %s\n\n", data); err != nil {
		return err
	}
	flusher.Flush()
	return nil
}

func streamChatCompletion(
	ctx context.Context,
	client *http.Client,
	baseURL string,
	apiKey string,
	model string,
	messages []message,
	reasoningEffort string,
	emit func(streamEvent) error,
) error {
	body, err := json.Marshal(chatRequest{
		Model:           model,
		Messages:        messages,
		ReasoningEffort: reasoningEffort,
		Stream:          true,
	})
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBody, err := io.ReadAll(resp.Body)
		if err != nil {
			return err
		}

		var parsed chatResponse
		if err := json.Unmarshal(respBody, &parsed); err == nil && parsed.Error != nil && parsed.Error.Message != "" {
			return fmt.Errorf("HTTP %d: %s", resp.StatusCode, parsed.Error.Message)
		}
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(respBody))
	}

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 1024), 1024*1024)
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
			return nil
		}

		var chunk chatStreamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			return fmt.Errorf("解析流式响应失败: %w; raw=%s", err, data)
		}
		if chunk.Error != nil && chunk.Error.Message != "" {
			return errors.New(chunk.Error.Message)
		}

		for _, choice := range chunk.Choices {
			if reasoning := strings.TrimSpace(choice.Delta.ReasoningContent); reasoning != "" {
				if err := emit(streamEvent{Type: "reasoning_delta", Delta: reasoning}); err != nil {
					return err
				}
			} else if reasoning := extractReasoningContent(choice.Delta.Reasoning); reasoning != "" {
				if err := emit(streamEvent{Type: "reasoning_delta", Delta: reasoning}); err != nil {
					return err
				}
			}

			if choice.Delta.Content != "" {
				if err := emit(streamEvent{Type: "content_delta", Delta: choice.Delta.Content}); err != nil {
					return err
				}
			}
		}
	}

	return scanner.Err()
}

func isValidReasoningEffort(value string) bool {
	switch value {
	case "", "low", "medium", "high":
		return true
	default:
		return false
	}
}

func extractReasoningContent(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}

	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return strings.TrimSpace(text)
	}

	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil {
		return ""
	}
	for _, key := range []string{"content", "text", "summary"} {
		if value, ok := obj[key].(string); ok {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

//go:embed web/index.html
var pageHTML string
