package webapp

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"

	"imo/internal/agent"

	"github.com/joho/godotenv"
)

type appConfig struct {
	Addr        string
	AgentDBPath string
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
	// The MVP uses a deterministic mock loop; real tool execution can be added
	// behind the agent.RunService boundary without changing route registration.
	server.runService = agent.NewRunService(store, agent.PolicyEngine{})

	mux := http.NewServeMux()
	mux.HandleFunc("/", server.admin)
	mux.HandleFunc("/admin", server.admin)
	mux.HandleFunc("/api/runs", server.runs)
	mux.HandleFunc("/api/runs/", server.runResource)

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

	return appConfig{
		Addr:        ":" + strings.TrimPrefix(port, ":"),
		AgentDBPath: agentDBPath,
	}, nil
}

//go:embed assets/agent_admin.html
var adminHTML string
