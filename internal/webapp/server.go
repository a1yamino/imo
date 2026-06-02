package webapp

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"imo/internal/agent"

	"github.com/joho/godotenv"
)

type appConfig struct {
	Addr              string
	AgentDBPath       string
	APIKey            string
	BaseURL           string
	Model             string
	WebSearchProvider string
	SerperAPIKey      string
	SerperSearchURL   string
}

type streamEvent struct {
	Type    string `json:"type"`
	Delta   string `json:"delta,omitempty"`
	Message string `json:"message,omitempty"`
}

type server struct {
	config     appConfig
	runService *agent.RunService
}

// Run wires the agent admin runtime into one server. main.go stays as a thin
// process entrypoint, while this package owns HTTP routing and static assets.
func Run() error {
	if err := godotenv.Load(); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}

	cfg, err := loadConfig()
	if err != nil {
		return err
	}

	server := &server{config: cfg}

	store, err := agent.NewSQLiteAgentStore(context.Background(), cfg.AgentDBPath)
	if err != nil {
		return err
	}
	defer store.Close()
	httpClient := &http.Client{Timeout: 60 * time.Second}
	llm := agent.NewOpenAICompatibleLLMClient(httpClient, cfg.APIKey, cfg.BaseURL, cfg.Model)
	server.runService = agent.NewRunService(store, agent.PolicyEngine{}, llm)
	agent.RegisterFilesystemTools(server.runService.Tools())
	agent.RegisterWebFetchTool(server.runService.Tools(), httpClient)
	if cfg.WebSearchProvider == "serper" && cfg.SerperAPIKey != "" {
		agent.RegisterSerperWebTools(server.runService.Tools(), agent.SerperConfig{
			APIKey:    cfg.SerperAPIKey,
			SearchURL: cfg.SerperSearchURL,
			Client:    httpClient,
		})
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", server.admin)
	mux.HandleFunc("/admin", server.admin)
	mux.HandleFunc("/api/runs", server.runs)
	mux.HandleFunc("/api/runs/", server.runResource)
	mux.HandleFunc("/api/sessions/", server.sessionResource)

	fmt.Printf("Agent 管理员 Dashboard 已启动: http://localhost%s\n", cfg.Addr)
	fmt.Printf("agent_db=%s\n", cfg.AgentDBPath)
	return http.ListenAndServe(cfg.Addr, mux)
}

func loadConfig() (appConfig, error) {
	port := strings.TrimSpace(os.Getenv("PORT"))
	if port == "" {
		port = "8080"
	}

	agentDBPath := strings.TrimSpace(os.Getenv("AGENT_DB_PATH"))
	if agentDBPath == "" {
		agentDBPath = "agent.db"
	}
	baseURL := strings.TrimSpace(os.Getenv("OPENAI_BASE_URL"))
	if baseURL == "" {
		baseURL = "https://api.openai.com"
	}
	model := strings.TrimSpace(os.Getenv("OPENAI_MODEL"))
	if model == "" {
		model = "gpt-4o-mini"
	}
	webSearchProvider := strings.ToLower(strings.TrimSpace(os.Getenv("WEB_SEARCH_PROVIDER")))
	if webSearchProvider == "" {
		webSearchProvider = "none"
	}
	if webSearchProvider != "none" && webSearchProvider != "serper" {
		return appConfig{}, fmt.Errorf("unsupported WEB_SEARCH_PROVIDER %q", webSearchProvider)
	}
	serperSearchURL := strings.TrimSpace(os.Getenv("SERPER_SEARCH_URL"))
	if serperSearchURL == "" {
		serperSearchURL = "https://google.serper.dev/search"
	}

	return appConfig{
		Addr:              ":" + strings.TrimPrefix(port, ":"),
		AgentDBPath:       agentDBPath,
		APIKey:            strings.TrimSpace(os.Getenv("OPENAI_API_KEY")),
		BaseURL:           baseURL,
		Model:             model,
		WebSearchProvider: webSearchProvider,
		SerperAPIKey:      strings.TrimSpace(os.Getenv("SERPER_API_KEY")),
		SerperSearchURL:   serperSearchURL,
	}, nil
}

//go:embed assets/agent_admin.html
var adminHTML string
