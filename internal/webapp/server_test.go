package webapp

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDashboardServesRootAndAdmin(t *testing.T) {
	s := NewServer(nil)
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
			if !strings.Contains(rec.Body.String(), `"filesystem.list_dir", "filesystem.read_file", "web.search", "web.fetch"`) {
				t.Fatal("response does not enable default chat tools")
			}
			if !strings.Contains(rec.Body.String(), `id="streamToggleBtn"`) {
				t.Fatal("response does not contain stream toggle")
			}
			if !strings.Contains(rec.Body.String(), `sendSlashCommand(nextStreamEnabled ? "/stream on" : "/stream off")`) {
				t.Fatal("stream toggle should send slash commands")
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
