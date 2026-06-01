package webapp

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLoadConfigDoesNotRequireOpenAIKey(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("OPENAI_BASE_URL", "")
	t.Setenv("OPENAI_MODEL", "")
	t.Setenv("PORT", "")
	t.Setenv("AGENT_DB_PATH", "")

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig returned error: %v", err)
	}
	if cfg.AgentDBPath != "agent.db" {
		t.Fatalf("AgentDBPath=%q, want agent.db", cfg.AgentDBPath)
	}
	if cfg.Addr != ":8080" {
		t.Fatalf("Addr=%q, want :8080", cfg.Addr)
	}
}

func TestDashboardServesRootAndAdmin(t *testing.T) {
	s := &server{}
	for _, path := range []string{"/", "/admin"} {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			rec := httptest.NewRecorder()

			s.admin(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("status=%d, want %d", rec.Code, http.StatusOK)
			}
			if !strings.Contains(rec.Body.String(), "Agent Runs") {
				t.Fatal("response does not contain dashboard content")
			}
		})
	}
}
