package app

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"time"

	"imo/internal/agent"
	"imo/internal/logging"
	"imo/internal/webapp"

	"github.com/joho/godotenv"
)

func Run() error {
	if err := godotenv.Load(); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}

	cfg, err := LoadConfig()
	if err != nil {
		return err
	}
	logging.Configure(cfg.LogLevel, cfg.LogColor)

	store, err := agent.NewSQLiteAgentStore(context.Background(), cfg.AgentDBPath)
	if err != nil {
		return err
	}
	defer store.Close()

	httpClient := &http.Client{Timeout: 60 * time.Second}
	llm := agent.NewOpenAICompatibleLLMClient(httpClient, cfg.APIKey, cfg.BaseURL, cfg.Model)
	runService := agent.NewRunService(store, agent.PolicyEngine{}, llm)
	runService.SetLogger(slog.Default().With("component", "agent_runtime"))

	agent.RegisterFilesystemTools(runService.Tools())
	agent.RegisterWebFetchTool(runService.Tools(), httpClient)
	slog.Info("registered agent tools",
		"tools", []string{"filesystem.list_dir", "filesystem.read_file", "web.fetch"},
	)
	if cfg.WebSearchProvider == "serper" && cfg.SerperAPIKey != "" {
		agent.RegisterSerperWebTools(runService.Tools(), agent.SerperConfig{
			APIKey:    cfg.SerperAPIKey,
			SearchURL: cfg.SerperSearchURL,
			Client:    httpClient,
		})
		slog.Info("registered web search provider",
			"provider", cfg.WebSearchProvider,
			"search_url", cfg.SerperSearchURL,
		)
	} else {
		slog.Info("web search provider disabled", "provider", cfg.WebSearchProvider)
	}

	server := webapp.NewServer(runService)
	slog.Info("agent admin dashboard started",
		"addr", cfg.Addr,
		"url", "http://localhost"+cfg.Addr,
		"agent_db", cfg.AgentDBPath,
		"model", cfg.Model,
		"openai_base_url", cfg.BaseURL,
	)
	return http.ListenAndServe(cfg.Addr, server.Handler())
}
