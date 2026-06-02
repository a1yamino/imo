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
	Addr        string
	AgentDBPath string
	APIKey      string
	BaseURL     string
	Model       string
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
	llm := agent.NewOpenAICompatibleLLMClient(&http.Client{Timeout: 60 * time.Second}, cfg.APIKey, cfg.BaseURL, cfg.Model)
	server.runService = agent.NewRunService(store, agent.PolicyEngine{}, llm)
	agent.RegisterFilesystemTools(server.runService.Tools())

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

	return appConfig{
		Addr:        ":" + strings.TrimPrefix(port, ":"),
		AgentDBPath: agentDBPath,
		APIKey:      strings.TrimSpace(os.Getenv("OPENAI_API_KEY")),
		BaseURL:     baseURL,
		Model:       model,
	}, nil
}

//go:embed assets/agent_admin.html
var adminHTML string
