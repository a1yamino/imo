package webapp

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"imo/internal/agent"
)

func (s *Server) admin(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" && r.URL.Path != "/admin" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, adminHTML)
}

func (s *Server) runs(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		runs, err := s.runService.ListRuns(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]any{"runs": runs})
	case http.MethodPost:
		var req agent.CreateRunRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid json", http.StatusBadRequest)
			return
		}
		run, err := s.runService.CreateRun(r.Context(), req)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := s.runService.StartRun(r.Context(), run.ID); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, run)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) runResource(w http.ResponseWriter, r *http.Request) {
	// Keep subresource routing here until the API surface grows enough to justify
	// a router dependency. The paths are intentionally simple and explicit.
	rest := strings.TrimPrefix(r.URL.Path, "/api/runs/")
	parts := strings.Split(strings.Trim(rest, "/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		http.NotFound(w, r)
		return
	}
	runID := parts[0]
	if len(parts) == 1 {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		snapshot, err := s.runService.Snapshot(r.Context(), runID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		writeJSON(w, snapshot)
		return
	}

	switch parts[1] {
	case "events":
		s.runEvents(w, r, runID)
	case "steps":
		s.snapshotPart(w, r, runID, "steps")
	case "tool-calls":
		s.snapshotPart(w, r, runID, "tool-calls")
	case "audit-events":
		s.snapshotPart(w, r, runID, "audit-events")
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) sessionResource(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	sessionID := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/sessions/"), "/")
	if sessionID == "" {
		http.NotFound(w, r)
		return
	}
	snapshot, err := s.runService.SessionSnapshot(r.Context(), sessionID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	writeJSON(w, snapshot)
}

func (s *Server) snapshotPart(w http.ResponseWriter, r *http.Request, runID string, part string) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	snapshot, err := s.runService.Snapshot(r.Context(), runID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	switch part {
	case "steps":
		writeJSON(w, map[string]any{"steps": snapshot.Steps})
	case "tool-calls":
		writeJSON(w, map[string]any{"tool_calls": snapshot.ToolCalls})
	case "audit-events":
		writeJSON(w, map[string]any{"audit_events": snapshot.AuditEvents})
	}
}

func (s *Server) runEvents(w http.ResponseWriter, r *http.Request, runID string) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	events, cancel := s.runService.ObserveRun(runID)
	defer cancel()

	// Send a snapshot first so clients connecting after completion still render a
	// full run without waiting for another event.
	if snapshot, err := s.runService.Snapshot(r.Context(), runID); err == nil {
		_ = writeSSE(w, flusher, streamEvent{Type: "snapshot", Message: jsonString(snapshot)})
	}
	for {
		select {
		case <-r.Context().Done():
			return
		case event := <-events:
			_ = writeSSE(w, flusher, streamEvent{Type: string(event.Type), Message: jsonString(event)})
		}
	}
}

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(w).Encode(value)
}

func writeSSE(w io.Writer, flusher http.Flusher, event streamEvent) error {
	data, err := json.Marshal(event)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "data: %s\n\n", data); err != nil {
		return err
	}
	flusher.Flush()
	return nil
}

func jsonString(value any) string {
	data, err := json.Marshal(value)
	if err != nil {
		return "{}"
	}
	return string(data)
}
