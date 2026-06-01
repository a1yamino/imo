package agent

import (
	"context"
	"time"
)

// AutonomyLevel is a policy input, not an execution mode baked into tools.
// Keeping it here lets the same run loop support low/medium/high behavior
// without scattering permission checks across handlers or executors.
type AutonomyLevel string

const (
	AutonomyLow    AutonomyLevel = "low"
	AutonomyMedium AutonomyLevel = "medium"
	AutonomyHigh   AutonomyLevel = "high"
)

// RiskLevel is reported by a tool before execution. Policy uses it together
// with autonomy and scope to decide whether to allow, pause, or deny a call.
type RiskLevel string

const (
	RiskLow    RiskLevel = "low"
	RiskMedium RiskLevel = "medium"
	RiskHigh   RiskLevel = "high"
)

// PolicyDecisionType is deliberately small so RunService can map each decision
// to a clear state transition: execute, wait for approval, or feed denial back
// as an observation.
type PolicyDecisionType string

const (
	PolicyAllow           PolicyDecisionType = "allow"
	PolicyRequireApproval PolicyDecisionType = "require_approval"
	PolicyDeny            PolicyDecisionType = "deny"
)

type PolicyRequest struct {
	Autonomy AutonomyLevel
	Risk     RiskLevel
	ToolName string
}

type PolicyDecision struct {
	Type   PolicyDecisionType `json:"type"`
	Reason string             `json:"reason"`
}

// RunStatus describes the externally visible lifecycle of one user goal.
// The waiting and blocked states are included now so the MVP data model can
// support approval gates and resumable workers later.
type RunStatus string

const (
	RunQueued          RunStatus = "queued"
	RunRunning         RunStatus = "running"
	RunWaitingApproval RunStatus = "waiting_approval"
	RunNeedsInput      RunStatus = "needs_input"
	RunBlocked         RunStatus = "blocked"
	RunCompleted       RunStatus = "completed"
	RunFailed          RunStatus = "failed"
	RunCancelled       RunStatus = "cancelled"
)

// StepType separates model intent from external observations. The admin
// dashboard relies on this distinction to show what the agent decided versus
// what the environment returned.
type StepType string

const (
	StepModelDecision StepType = "model_decision"
	StepObservation   StepType = "observation"
	StepResponse      StepType = "response"
)

type StepStatus string

const (
	StepStarted   StepStatus = "started"
	StepCompleted StepStatus = "completed"
	StepFailed    StepStatus = "failed"
)

type ToolCallStatus string

const (
	ToolCallRequested ToolCallStatus = "requested"
	ToolCallCompleted ToolCallStatus = "completed"
	ToolCallFailed    ToolCallStatus = "failed"
)

// LLMMessage is the model-facing conversation unit. The runtime builds these
// from durable runs so a session can span multiple independent executions.
type LLMMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type LLMRequest struct {
	SystemPrompt string
	UserPrompt   string
	Messages     []LLMMessage
}

type LLMResponse struct {
	Content string
}

type LLMClient interface {
	Complete(context.Context, LLMRequest) (LLMResponse, error)
}

// RuntimeCommand is an instruction consumed by the agent runtime. HTTP handlers
// should submit commands; they should not drive the runtime loop directly.
type RuntimeCommand struct {
	Type  RuntimeCommandType
	RunID string
}

type RuntimeCommandType string

const (
	RuntimeCommandStartRun RuntimeCommandType = "start_run"
)

// RuntimeEventType names domain events emitted by the runtime. UI delivery is
// only one consumer; the events also define the runtime's observable state flow.
type RuntimeEventType string

const (
	RuntimeEventRunCreated       RuntimeEventType = "run_created"
	RuntimeEventRunStatusChanged RuntimeEventType = "run_status_changed"
	RuntimeEventStepFinished     RuntimeEventType = "step_finished"
	RuntimeEventRunCompleted     RuntimeEventType = "run_completed"
	RuntimeEventRunFailed        RuntimeEventType = "run_failed"
)

// Run is the durable record for one agent execution. OwnerID and SessionID are
// present even in single-user mode to avoid a schema rewrite when multi-user
// isolation is added.
type Run struct {
	ID             string        `json:"id"`
	OwnerID        string        `json:"owner_id"`
	SessionID      string        `json:"session_id"`
	Goal           string        `json:"goal"`
	Status         RunStatus     `json:"status"`
	Autonomy       AutonomyLevel `json:"autonomy_level"`
	EnabledTools   []string      `json:"enabled_tools"`
	WorkspaceScope string        `json:"workspace_scope"`
	CreatedAt      time.Time     `json:"created_at"`
	UpdatedAt      time.Time     `json:"updated_at"`
	StartedAt      *time.Time    `json:"started_at,omitempty"`
	CompletedAt    *time.Time    `json:"completed_at,omitempty"`
}

// Step is the audit-friendly timeline unit. It stores summaries and raw model
// outputs, but not hidden chain-of-thought; ReasoningSummary is the product
// surface for explaining why a decision was made.
type Step struct {
	ID               string     `json:"id"`
	RunID            string     `json:"run_id"`
	Index            int        `json:"index"`
	Type             StepType   `json:"type"`
	Status           StepStatus `json:"status"`
	ModelInput       string     `json:"model_input,omitempty"`
	ModelOutput      string     `json:"model_output,omitempty"`
	ReasoningSummary string     `json:"reasoning_summary,omitempty"`
	Observation      string     `json:"observation,omitempty"`
	Error            string     `json:"error,omitempty"`
	CreatedAt        time.Time  `json:"created_at"`
	UpdatedAt        time.Time  `json:"updated_at"`
}

// ToolCall records the exact boundary crossing from agent intent to external
// capability. Arguments and results stay as JSON strings so the dashboard can
// display them before each tool has a strongly typed result model.
type ToolCall struct {
	ID             string             `json:"id"`
	RunID          string             `json:"run_id"`
	StepID         string             `json:"step_id"`
	ToolName       string             `json:"tool_name"`
	ArgumentsJSON  string             `json:"arguments_json"`
	RiskLevel      RiskLevel          `json:"risk_level"`
	PolicyDecision PolicyDecisionType `json:"policy_decision"`
	ApprovalStatus string             `json:"approval_status"`
	Status         ToolCallStatus     `json:"status"`
	ResultJSON     string             `json:"result_json,omitempty"`
	Error          string             `json:"error,omitempty"`
	StartedAt      time.Time          `json:"started_at"`
	FinishedAt     *time.Time         `json:"finished_at,omitempty"`
}

// AuditEvent is for accountability rather than agent context. It should record
// who or what caused a state change, approval, tool execution, or failure.
type AuditEvent struct {
	ID        string    `json:"id"`
	OwnerID   string    `json:"owner_id"`
	RunID     string    `json:"run_id"`
	Actor     string    `json:"actor"`
	Action    string    `json:"action"`
	Payload   string    `json:"payload_json"`
	CreatedAt time.Time `json:"created_at"`
}

// Artifact points to durable outputs created by a run, such as files, reports,
// or source snapshots. The MVP does not create artifacts yet, but snapshots keep
// the field stable for the dashboard API.
type Artifact struct {
	ID        string    `json:"id"`
	RunID     string    `json:"run_id"`
	Kind      string    `json:"kind"`
	URI       string    `json:"uri"`
	Metadata  string    `json:"metadata_json"`
	CreatedAt time.Time `json:"created_at"`
}

// RunSnapshot is the read model for the admin dashboard. It intentionally
// denormalizes related records so the UI can refresh a run with one request.
type RunSnapshot struct {
	Run         Run          `json:"run"`
	Steps       []Step       `json:"steps"`
	ToolCalls   []ToolCall   `json:"tool_calls"`
	AuditEvents []AuditEvent `json:"audit_events"`
	Artifacts   []Artifact   `json:"artifacts"`
}

// SessionSnapshot is the read model for a multi-turn conversation. Each user
// turn is still a separate run, but all runs in one session form the chat state.
type SessionSnapshot struct {
	SessionID string        `json:"session_id"`
	Runs      []RunSnapshot `json:"runs"`
}

// RuntimeEvent is the runtime's domain event envelope. The dashboard observes
// these events, but the names are intentionally runtime-first rather than UI-first.
type RuntimeEvent struct {
	Type  RuntimeEventType `json:"type"`
	RunID string           `json:"run_id"`
	Data  any              `json:"data,omitempty"`
}

// CreateRunRequest is shared by the HTTP API and store layer. Tool and scope
// fields are part of the run config so future Policy decisions can be replayed.
type CreateRunRequest struct {
	SessionID      string        `json:"session_id,omitempty"`
	Goal           string        `json:"goal"`
	Autonomy       AutonomyLevel `json:"autonomy_level"`
	EnabledTools   []string      `json:"enabled_tools"`
	WorkspaceScope string        `json:"workspace_scope"`
}
