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
			if !strings.Contains(rec.Body.String(), "输入消息开始多轮对话") {
				t.Fatal("response does not contain multi-turn chat entry")
			}
			if !strings.Contains(rec.Body.String(), "session_id: selectedSessionId") {
				t.Fatal("response does not keep the selected session when sending")
			}
			if !strings.Contains(rec.Body.String(), "groupRunsBySession") {
				t.Fatal("response does not group the run list by session")
			}
			if strings.Contains(rec.Body.String(), "session.id !== selectedSessionId") {
				t.Fatal("selected sessions should still be collapsible")
			}
			if strings.Contains(rec.Body.String(), "创建并运行") {
				t.Fatal("response still contains legacy create-run form")
			}
		})
	}
}
