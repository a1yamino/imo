package app

import (
	"fmt"
	"os"
	"strings"
)

type Config struct {
	Addr              string
	AgentDBPath       string
	APIKey            string
	BaseURL           string
	Model             string
	LogLevel          string
	LogColor          string
	WebSearchProvider string
	SerperAPIKey      string
	SerperSearchURL   string
}

func LoadConfig() (Config, error) {
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
	logLevel := strings.ToLower(strings.TrimSpace(os.Getenv("LOG_LEVEL")))
	if logLevel == "" {
		logLevel = "info"
	}
	if logLevel != "debug" && logLevel != "info" && logLevel != "warn" && logLevel != "error" {
		return Config{}, fmt.Errorf("unsupported LOG_LEVEL %q", logLevel)
	}
	logColor := strings.ToLower(strings.TrimSpace(os.Getenv("LOG_COLOR")))
	if logColor == "" {
		logColor = "auto"
	}
	if logColor != "auto" && logColor != "always" && logColor != "never" {
		return Config{}, fmt.Errorf("unsupported LOG_COLOR %q", logColor)
	}
	webSearchProvider := strings.ToLower(strings.TrimSpace(os.Getenv("WEB_SEARCH_PROVIDER")))
	if webSearchProvider == "" {
		webSearchProvider = "none"
	}
	if webSearchProvider != "none" && webSearchProvider != "serper" {
		return Config{}, fmt.Errorf("unsupported WEB_SEARCH_PROVIDER %q", webSearchProvider)
	}
	serperSearchURL := strings.TrimSpace(os.Getenv("SERPER_SEARCH_URL"))
	if serperSearchURL == "" {
		serperSearchURL = "https://google.serper.dev/search"
	}

	return Config{
		Addr:              ":" + strings.TrimPrefix(port, ":"),
		AgentDBPath:       agentDBPath,
		APIKey:            strings.TrimSpace(os.Getenv("OPENAI_API_KEY")),
		BaseURL:           baseURL,
		Model:             model,
		LogLevel:          logLevel,
		LogColor:          logColor,
		WebSearchProvider: webSearchProvider,
		SerperAPIKey:      strings.TrimSpace(os.Getenv("SERPER_API_KEY")),
		SerperSearchURL:   serperSearchURL,
	}, nil
}
