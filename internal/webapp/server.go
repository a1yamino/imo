package webapp

import (
	_ "embed"
	"net/http"

	"imo/internal/agent"
)

type streamEvent struct {
	Type    string `json:"type"`
	Delta   string `json:"delta,omitempty"`
	Message string `json:"message,omitempty"`
}

type Server struct {
	runService *agent.RunService
}

func NewServer(runService *agent.RunService) *Server {
	return &Server{runService: runService}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.admin)
	mux.HandleFunc("/admin", s.admin)
	mux.HandleFunc("/api/runs", s.runs)
	mux.HandleFunc("/api/runs/", s.runResource)
	mux.HandleFunc("/api/sessions/", s.sessionResource)
	return mux
}

//go:embed assets/agent_admin.html
var adminHTML string
