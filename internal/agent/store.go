package agent

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	_ "modernc.org/sqlite"
)

// defaultOwnerID preserves the future multi-user boundary while the MVP remains
// single-user. Queries and audit rows already carry owner_id for migration later.
const defaultOwnerID = "default"

type SQLiteAgentStore struct {
	db *sql.DB
}

func NewSQLiteAgentStore(ctx context.Context, path string) (*SQLiteAgentStore, error) {
	if strings.TrimSpace(path) == "" {
		path = "agent.db"
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	// modernc SQLite is safest here with one open connection; it also keeps
	// in-memory test databases consistent across queries.
	db.SetMaxOpenConns(1)

	store := &SQLiteAgentStore{db: db}
	if err := store.init(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return store, nil
}

func (s *SQLiteAgentStore) Close() error {
	return s.db.Close()
}

func (s *SQLiteAgentStore) init(ctx context.Context) error {
	// Schema is intentionally explicit instead of reflection-based. These tables
	// are the audit surface for the admin dashboard and future worker replay.
	statements := []string{
		`PRAGMA foreign_keys = ON`,
		`CREATE TABLE IF NOT EXISTS runs (
			id TEXT PRIMARY KEY,
			owner_id TEXT NOT NULL,
			session_id TEXT NOT NULL,
			goal TEXT NOT NULL,
			status TEXT NOT NULL,
			autonomy_level TEXT NOT NULL,
			enabled_tools_json TEXT NOT NULL,
			workspace_scope TEXT NOT NULL,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			started_at TEXT,
			completed_at TEXT
		)`,
		`CREATE TABLE IF NOT EXISTS steps (
			id TEXT PRIMARY KEY,
			run_id TEXT NOT NULL,
			idx INTEGER NOT NULL,
			type TEXT NOT NULL,
			status TEXT NOT NULL,
			model_input TEXT NOT NULL,
			model_output TEXT NOT NULL,
			reasoning_summary TEXT NOT NULL,
			observation TEXT NOT NULL,
			error TEXT NOT NULL,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			FOREIGN KEY(run_id) REFERENCES runs(id)
		)`,
		`CREATE TABLE IF NOT EXISTS tool_calls (
			id TEXT PRIMARY KEY,
			run_id TEXT NOT NULL,
			step_id TEXT NOT NULL,
			tool_name TEXT NOT NULL,
			arguments_json TEXT NOT NULL,
			risk_level TEXT NOT NULL,
			policy_decision TEXT NOT NULL,
			approval_status TEXT NOT NULL,
			status TEXT NOT NULL,
			result_json TEXT NOT NULL,
			error TEXT NOT NULL,
			started_at TEXT NOT NULL,
			finished_at TEXT,
			FOREIGN KEY(run_id) REFERENCES runs(id),
			FOREIGN KEY(step_id) REFERENCES steps(id)
		)`,
		`CREATE TABLE IF NOT EXISTS audit_events (
			id TEXT PRIMARY KEY,
			owner_id TEXT NOT NULL,
			run_id TEXT NOT NULL,
			actor TEXT NOT NULL,
			action TEXT NOT NULL,
			payload_json TEXT NOT NULL,
			created_at TEXT NOT NULL,
			FOREIGN KEY(run_id) REFERENCES runs(id)
		)`,
		`CREATE TABLE IF NOT EXISTS artifacts (
			id TEXT PRIMARY KEY,
			run_id TEXT NOT NULL,
			kind TEXT NOT NULL,
			uri TEXT NOT NULL,
			metadata_json TEXT NOT NULL,
			created_at TEXT NOT NULL,
			FOREIGN KEY(run_id) REFERENCES runs(id)
		)`,
		`CREATE TABLE IF NOT EXISTS session_runtime_options (
			session_id TEXT PRIMARY KEY,
			stream_enabled INTEGER NOT NULL,
			usage_enabled INTEGER NOT NULL DEFAULT 0,
			updated_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS llm_usage (
			id TEXT PRIMARY KEY,
			run_id TEXT NOT NULL,
			step_index INTEGER NOT NULL,
			prompt_tokens INTEGER NOT NULL,
			completion_tokens INTEGER NOT NULL,
			total_tokens INTEGER NOT NULL,
			cached_tokens INTEGER NOT NULL,
			reasoning_tokens INTEGER NOT NULL,
			created_at TEXT NOT NULL,
			FOREIGN KEY(run_id) REFERENCES runs(id)
		)`,
	}
	for _, statement := range statements {
		if _, err := s.db.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	if err := s.ensureColumn(ctx, "session_runtime_options", "usage_enabled", `INTEGER NOT NULL DEFAULT 0`); err != nil {
		return err
	}
	return nil
}

func (s *SQLiteAgentStore) ensureColumn(ctx context.Context, table, column, definition string) error {
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf(`PRAGMA table_info(%s)`, table))
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var cid int
		var name, columnType string
		var notNull int
		var defaultValue any
		var primaryKey int
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			return err
		}
		if name == column {
			return rows.Err()
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, fmt.Sprintf(`ALTER TABLE %s ADD COLUMN %s %s`, table, column, definition))
	return err
}

func (s *SQLiteAgentStore) CreateRun(ctx context.Context, req CreateRunRequest) (Run, error) {
	goal := strings.TrimSpace(req.Goal)
	if goal == "" {
		return Run{}, errors.New("goal is required")
	}
	sessionID := strings.TrimSpace(req.SessionID)
	if sessionID == "" {
		sessionID = uuid.NewString()
	}
	autonomy := req.Autonomy
	if autonomy == "" {
		autonomy = AutonomyMedium
	}
	scope := strings.TrimSpace(req.WorkspaceScope)
	if scope == "" {
		scope = "."
	}
	toolsJSON, err := json.Marshal(req.EnabledTools)
	if err != nil {
		return Run{}, err
	}
	now := time.Now().UTC()
	run := Run{
		ID:             uuid.NewString(),
		OwnerID:        defaultOwnerID,
		SessionID:      sessionID,
		Goal:           goal,
		Status:         RunQueued,
		Autonomy:       autonomy,
		EnabledTools:   req.EnabledTools,
		WorkspaceScope: scope,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO runs (
		id, owner_id, session_id, goal, status, autonomy_level, enabled_tools_json,
		workspace_scope, created_at, updated_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		run.ID, run.OwnerID, run.SessionID, run.Goal, run.Status, run.Autonomy, string(toolsJSON),
		run.WorkspaceScope, formatTime(run.CreatedAt), formatTime(run.UpdatedAt))
	if err != nil {
		return Run{}, err
	}
	return run, nil
}

func (s *SQLiteAgentStore) GetSessionRuntimeOptions(ctx context.Context, sessionID string) (SessionRuntimeOptions, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return SessionRuntimeOptions{}, errors.New("session_id is required")
	}
	var streamEnabled int
	var usageEnabled int
	var updatedAt string
	err := s.db.QueryRowContext(ctx, `SELECT stream_enabled, usage_enabled, updated_at
		FROM session_runtime_options WHERE session_id = ?`, sessionID).Scan(&streamEnabled, &usageEnabled, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return SessionRuntimeOptions{SessionID: sessionID, Stream: false, Usage: false}, nil
	}
	if err != nil {
		return SessionRuntimeOptions{}, err
	}
	return SessionRuntimeOptions{
		SessionID: sessionID,
		Stream:    streamEnabled == 1,
		Usage:     usageEnabled == 1,
		UpdatedAt: parseTime(updatedAt),
	}, nil
}

func (s *SQLiteAgentStore) SetSessionRuntimeOptions(ctx context.Context, options SessionRuntimeOptions) (SessionRuntimeOptions, error) {
	options.SessionID = strings.TrimSpace(options.SessionID)
	if options.SessionID == "" {
		return SessionRuntimeOptions{}, errors.New("session_id is required")
	}
	now := time.Now().UTC()
	options.UpdatedAt = now
	streamEnabled := 0
	if options.Stream {
		streamEnabled = 1
	}
	usageEnabled := 0
	if options.Usage {
		usageEnabled = 1
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO session_runtime_options (
		session_id, stream_enabled, usage_enabled, updated_at
	) VALUES (?, ?, ?, ?)
	ON CONFLICT(session_id) DO UPDATE SET
		stream_enabled = excluded.stream_enabled,
		usage_enabled = excluded.usage_enabled,
		updated_at = excluded.updated_at`,
		options.SessionID, streamEnabled, usageEnabled, formatTime(now))
	if err != nil {
		return SessionRuntimeOptions{}, err
	}
	return options, nil
}

func (s *SQLiteAgentStore) SaveLLMUsage(ctx context.Context, record LLMUsageRecord) (LLMUsageRecord, error) {
	record.RunID = strings.TrimSpace(record.RunID)
	if record.RunID == "" {
		return LLMUsageRecord{}, errors.New("run_id is required")
	}
	now := time.Now().UTC()
	if record.ID == "" {
		record.ID = uuid.NewString()
	}
	if record.CreatedAt.IsZero() {
		record.CreatedAt = now
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO llm_usage (
		id, run_id, step_index, prompt_tokens, completion_tokens, total_tokens,
		cached_tokens, reasoning_tokens, created_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		record.ID, record.RunID, record.StepIndex, record.Usage.PromptTokens,
		record.Usage.CompletionTokens, record.Usage.TotalTokens, record.Usage.CachedTokens,
		record.Usage.ReasoningTokens, formatTime(record.CreatedAt))
	if err != nil {
		return LLMUsageRecord{}, err
	}
	return record, nil
}

func (s *SQLiteAgentStore) SessionUsage(ctx context.Context, sessionID string) (LLMUsageSummary, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return LLMUsageSummary{}, errors.New("session_id is required")
	}
	summary := LLMUsageSummary{SessionID: sessionID}
	err := s.db.QueryRowContext(ctx, `SELECT
			COUNT(u.id),
			COALESCE(SUM(u.prompt_tokens), 0),
			COALESCE(SUM(u.completion_tokens), 0),
			COALESCE(SUM(u.total_tokens), 0),
			COALESCE(SUM(u.cached_tokens), 0),
			COALESCE(SUM(u.reasoning_tokens), 0)
		FROM llm_usage u
		INNER JOIN runs r ON r.id = u.run_id
		WHERE r.session_id = ?`, sessionID).Scan(
		&summary.LLMCalls,
		&summary.PromptTokens,
		&summary.CompletionTokens,
		&summary.TotalTokens,
		&summary.CachedTokens,
		&summary.ReasoningTokens,
	)
	if err != nil {
		return LLMUsageSummary{}, err
	}
	return summary, nil
}

func (s *SQLiteAgentStore) GetRun(ctx context.Context, id string) (Run, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id, owner_id, session_id, goal, status, autonomy_level,
		enabled_tools_json, workspace_scope, created_at, updated_at, started_at, completed_at
		FROM runs WHERE id = ?`, id)
	return scanRun(row)
}

func (s *SQLiteAgentStore) ListRuns(ctx context.Context) ([]Run, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, owner_id, session_id, goal, status, autonomy_level,
		enabled_tools_json, workspace_scope, created_at, updated_at, started_at, completed_at
		FROM runs ORDER BY updated_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var runs []Run
	for rows.Next() {
		run, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		runs = append(runs, run)
	}
	return runs, rows.Err()
}

func (s *SQLiteAgentStore) ListRunsBySession(ctx context.Context, sessionID string) ([]Run, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, owner_id, session_id, goal, status, autonomy_level,
		enabled_tools_json, workspace_scope, created_at, updated_at, started_at, completed_at
		FROM runs WHERE session_id = ? ORDER BY created_at ASC, rowid ASC`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var runs []Run
	for rows.Next() {
		run, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		runs = append(runs, run)
	}
	return runs, rows.Err()
}

func (s *SQLiteAgentStore) UpdateRunStatus(ctx context.Context, id string, status RunStatus) error {
	now := time.Now().UTC()
	var startedAt any
	var completedAt any
	if status == RunRunning {
		startedAt = formatTime(now)
	}
	if status == RunCompleted || status == RunFailed || status == RunCancelled {
		completedAt = formatTime(now)
	}
	// started_at and completed_at are set once so retries or duplicate events do
	// not rewrite the original lifecycle timestamps.
	_, err := s.db.ExecContext(ctx, `UPDATE runs
		SET status = ?, updated_at = ?,
		    started_at = COALESCE(started_at, ?),
		    completed_at = COALESCE(completed_at, ?)
		WHERE id = ?`, status, formatTime(now), startedAt, completedAt, id)
	return err
}

func (s *SQLiteAgentStore) AppendStep(ctx context.Context, step Step) (Step, error) {
	now := time.Now().UTC()
	if step.ID == "" {
		step.ID = uuid.NewString()
	}
	if step.CreatedAt.IsZero() {
		step.CreatedAt = now
	}
	if step.UpdatedAt.IsZero() {
		step.UpdatedAt = now
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO steps (
		id, run_id, idx, type, status, model_input, model_output,
		reasoning_summary, observation, error, created_at, updated_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		step.ID, step.RunID, step.Index, step.Type, step.Status, step.ModelInput, step.ModelOutput,
		step.ReasoningSummary, step.Observation, step.Error, formatTime(step.CreatedAt), formatTime(step.UpdatedAt))
	if err != nil {
		return Step{}, err
	}
	return step, nil
}

func (s *SQLiteAgentStore) SaveToolCall(ctx context.Context, call ToolCall) (ToolCall, error) {
	now := time.Now().UTC()
	if call.ID == "" {
		call.ID = uuid.NewString()
	}
	if call.StartedAt.IsZero() {
		call.StartedAt = now
	}
	if call.Status == ToolCallCompleted && call.FinishedAt == nil {
		call.FinishedAt = &now
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO tool_calls (
		id, run_id, step_id, tool_name, arguments_json, risk_level, policy_decision,
		approval_status, status, result_json, error, started_at, finished_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			status = excluded.status,
			result_json = excluded.result_json,
			error = excluded.error,
			finished_at = excluded.finished_at`,
		call.ID, call.RunID, call.StepID, call.ToolName, call.ArgumentsJSON, call.RiskLevel, call.PolicyDecision,
		call.ApprovalStatus, call.Status, call.ResultJSON, call.Error, formatTime(call.StartedAt), formatOptionalTime(call.FinishedAt))
	if err != nil {
		return ToolCall{}, err
	}
	return call, nil
}

func (s *SQLiteAgentStore) SaveAuditEvent(ctx context.Context, event AuditEvent) error {
	if event.ID == "" {
		event.ID = uuid.NewString()
	}
	if event.CreatedAt.IsZero() {
		event.CreatedAt = time.Now().UTC()
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO audit_events (
		id, owner_id, run_id, actor, action, payload_json, created_at
	) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		event.ID, event.OwnerID, event.RunID, event.Actor, event.Action, event.Payload, formatTime(event.CreatedAt))
	return err
}

func (s *SQLiteAgentStore) RunSnapshot(ctx context.Context, runID string) (RunSnapshot, error) {
	run, err := s.GetRun(ctx, runID)
	if err != nil {
		return RunSnapshot{}, err
	}
	steps, err := s.listSteps(ctx, runID)
	if err != nil {
		return RunSnapshot{}, err
	}
	toolCalls, err := s.listToolCalls(ctx, runID)
	if err != nil {
		return RunSnapshot{}, err
	}
	auditEvents, err := s.listAuditEvents(ctx, runID)
	if err != nil {
		return RunSnapshot{}, err
	}
	artifacts, err := s.listArtifacts(ctx, runID)
	if err != nil {
		return RunSnapshot{}, err
	}
	// Prefer [] over null in API responses; the dashboard can render stable shapes.
	steps = emptyIfNil(steps)
	toolCalls = emptyIfNil(toolCalls)
	auditEvents = emptyIfNil(auditEvents)
	artifacts = emptyIfNil(artifacts)
	return RunSnapshot{Run: run, Steps: steps, ToolCalls: toolCalls, AuditEvents: auditEvents, Artifacts: artifacts}, nil
}

func (s *SQLiteAgentStore) listSteps(ctx context.Context, runID string) ([]Step, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, run_id, idx, type, status, model_input, model_output,
		reasoning_summary, observation, error, created_at, updated_at
		FROM steps WHERE run_id = ? ORDER BY idx ASC`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var steps []Step
	for rows.Next() {
		var step Step
		var createdAt, updatedAt string
		if err := rows.Scan(&step.ID, &step.RunID, &step.Index, &step.Type, &step.Status, &step.ModelInput,
			&step.ModelOutput, &step.ReasoningSummary, &step.Observation, &step.Error, &createdAt, &updatedAt); err != nil {
			return nil, err
		}
		step.CreatedAt = parseTime(createdAt)
		step.UpdatedAt = parseTime(updatedAt)
		steps = append(steps, step)
	}
	return steps, rows.Err()
}

func (s *SQLiteAgentStore) listToolCalls(ctx context.Context, runID string) ([]ToolCall, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, run_id, step_id, tool_name, arguments_json, risk_level,
		policy_decision, approval_status, status, result_json, error, started_at, finished_at
		FROM tool_calls WHERE run_id = ? ORDER BY started_at ASC`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var calls []ToolCall
	for rows.Next() {
		var call ToolCall
		var startedAt string
		var finishedAt sql.NullString
		if err := rows.Scan(&call.ID, &call.RunID, &call.StepID, &call.ToolName, &call.ArgumentsJSON,
			&call.RiskLevel, &call.PolicyDecision, &call.ApprovalStatus, &call.Status, &call.ResultJSON,
			&call.Error, &startedAt, &finishedAt); err != nil {
			return nil, err
		}
		call.StartedAt = parseTime(startedAt)
		call.FinishedAt = parseOptionalTime(finishedAt)
		calls = append(calls, call)
	}
	return calls, rows.Err()
}

func (s *SQLiteAgentStore) listAuditEvents(ctx context.Context, runID string) ([]AuditEvent, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, owner_id, run_id, actor, action, payload_json, created_at
		FROM audit_events WHERE run_id = ? ORDER BY created_at ASC`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var events []AuditEvent
	for rows.Next() {
		var event AuditEvent
		var createdAt string
		if err := rows.Scan(&event.ID, &event.OwnerID, &event.RunID, &event.Actor, &event.Action, &event.Payload, &createdAt); err != nil {
			return nil, err
		}
		event.CreatedAt = parseTime(createdAt)
		events = append(events, event)
	}
	return events, rows.Err()
}

func (s *SQLiteAgentStore) listArtifacts(ctx context.Context, runID string) ([]Artifact, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, run_id, kind, uri, metadata_json, created_at
		FROM artifacts WHERE run_id = ? ORDER BY created_at ASC`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var artifacts []Artifact
	for rows.Next() {
		var artifact Artifact
		var createdAt string
		if err := rows.Scan(&artifact.ID, &artifact.RunID, &artifact.Kind, &artifact.URI, &artifact.Metadata, &createdAt); err != nil {
			return nil, err
		}
		artifact.CreatedAt = parseTime(createdAt)
		artifacts = append(artifacts, artifact)
	}
	return artifacts, rows.Err()
}

type runScanner interface {
	Scan(dest ...any) error
}

func scanRun(scanner runScanner) (Run, error) {
	var run Run
	var toolsJSON string
	var createdAt, updatedAt string
	var startedAt, completedAt sql.NullString
	if err := scanner.Scan(&run.ID, &run.OwnerID, &run.SessionID, &run.Goal, &run.Status, &run.Autonomy,
		&toolsJSON, &run.WorkspaceScope, &createdAt, &updatedAt, &startedAt, &completedAt); err != nil {
		return Run{}, err
	}
	if err := json.Unmarshal([]byte(toolsJSON), &run.EnabledTools); err != nil {
		return Run{}, fmt.Errorf("parse enabled tools: %w", err)
	}
	run.CreatedAt = parseTime(createdAt)
	run.UpdatedAt = parseTime(updatedAt)
	run.StartedAt = parseOptionalTime(startedAt)
	run.CompletedAt = parseOptionalTime(completedAt)
	return run, nil
}

func formatTime(t time.Time) string {
	return t.UTC().Format(time.RFC3339Nano)
}

func formatOptionalTime(t *time.Time) any {
	if t == nil {
		return nil
	}
	return formatTime(*t)
}

func parseTime(value string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}
	}
	return t
}

func parseOptionalTime(value sql.NullString) *time.Time {
	if !value.Valid || value.String == "" {
		return nil
	}
	t := parseTime(value.String)
	return &t
}

func emptyIfNil[T any](items []T) []T {
	if items == nil {
		return []T{}
	}
	return items
}
