package app

import "testing"

func TestLoadConfigDoesNotRequireOpenAIKey(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("OPENAI_BASE_URL", "")
	t.Setenv("OPENAI_MODEL", "")
	t.Setenv("WEB_SEARCH_PROVIDER", "")
	t.Setenv("SERPER_API_KEY", "")
	t.Setenv("SERPER_SEARCH_URL", "")
	t.Setenv("PORT", "")
	t.Setenv("AGENT_DB_PATH", "")
	t.Setenv("LOG_LEVEL", "")
	t.Setenv("LOG_COLOR", "")

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig returned error: %v", err)
	}
	if cfg.AgentDBPath != "agent.db" {
		t.Fatalf("AgentDBPath=%q, want agent.db", cfg.AgentDBPath)
	}
	if cfg.Addr != ":8080" {
		t.Fatalf("Addr=%q, want :8080", cfg.Addr)
	}
	if cfg.LogLevel != "info" {
		t.Fatalf("LogLevel=%q, want info", cfg.LogLevel)
	}
	if cfg.LogColor != "auto" {
		t.Fatalf("LogColor=%q, want auto", cfg.LogColor)
	}
	if cfg.WebSearchProvider != "none" {
		t.Fatalf("WebSearchProvider=%q, want none", cfg.WebSearchProvider)
	}
	if cfg.SerperSearchURL != "https://google.serper.dev/search" {
		t.Fatalf("SerperSearchURL=%q", cfg.SerperSearchURL)
	}
}

func TestLoadConfigSupportsSerperSearchProvider(t *testing.T) {
	t.Setenv("WEB_SEARCH_PROVIDER", "serper")
	t.Setenv("SERPER_API_KEY", "key")
	t.Setenv("SERPER_SEARCH_URL", "https://example.test/search")
	t.Setenv("LOG_LEVEL", "debug")
	t.Setenv("LOG_COLOR", "always")

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig returned error: %v", err)
	}
	if cfg.WebSearchProvider != "serper" {
		t.Fatalf("WebSearchProvider=%q, want serper", cfg.WebSearchProvider)
	}
	if cfg.SerperAPIKey != "key" || cfg.SerperSearchURL != "https://example.test/search" {
		t.Fatalf("serper config=%+v", cfg)
	}
	if cfg.LogLevel != "debug" {
		t.Fatalf("LogLevel=%q, want debug", cfg.LogLevel)
	}
	if cfg.LogColor != "always" {
		t.Fatalf("LogColor=%q, want always", cfg.LogColor)
	}
}
