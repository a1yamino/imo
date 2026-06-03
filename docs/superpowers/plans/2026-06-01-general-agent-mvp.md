# General Agent MVP Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build a runnable single-user general agent MVP with SQLite persistence, a command-driven AI conversation runtime, runtime events, and an admin dashboard showing run details.

**Architecture:** Keep the existing Go web server, but split the agent runtime into focused files: model types, SQLite store, policy engine, LLM client, run service, API handlers, and admin UI. The first runtime consumes commands and calls an OpenAI-compatible chat completion model for ordinary AI conversation.

**Tech Stack:** Go `net/http`, `database/sql`, `modernc.org/sqlite`, server-sent events, embedded HTML/CSS/JS.

---

## File Structure

- `main.go`: small application entrypoint that calls `internal/app.Run`.
- `internal/agent/types.go`: shared run, step, tool call, audit, policy, and event types.
- `internal/agent/store.go`: SQLite schema, persistence methods, list/detail queries.
- `internal/agent/policy.go`: autonomy/risk policy decisions.
- `internal/agent/service.go`: create runs, consume runtime commands, execute AI conversation runs, publish runtime events, expose snapshots.
- `internal/agent/llm.go`: OpenAI-compatible chat completion client.
- `internal/webapp/server.go`: config loading, route registration, and static embeds.
- `internal/webapp/agent_api.go`: `/api/runs`, `/api/runs/{id}`, `/api/runs/{id}/events`, `/api/runs/{id}/steps`, `/api/runs/{id}/tool-calls`, `/api/runs/{id}/audit-events`.
- `internal/webapp/assets/agent_admin.html`: admin dashboard UI.
- `internal/agent/store_test.go`: SQLite persistence tests.
- `internal/agent/policy_test.go`: policy matrix tests.
- `internal/agent/service_test.go`: run lifecycle tests.
- `docs/agent-mvp.md`: operator documentation for the implemented MVP.

## Task 1: Policy Engine

**Files:**
- Create: `internal/agent/policy_test.go`
- Create: `internal/agent/types.go`
- Create: `internal/agent/policy.go`

- [ ] **Step 1: Write failing policy tests**

Create `internal/agent/policy_test.go` with table-driven tests for low, medium, high autonomy decisions:

```go
package agent

import "testing"

func TestPolicyEngineEvaluate(t *testing.T) {
	tests := []struct {
		name     string
		autonomy AutonomyLevel
		risk     RiskLevel
		want     PolicyDecisionType
	}{
		{"low autonomy requires approval for low risk", AutonomyLow, RiskLow, PolicyRequireApproval},
		{"medium autonomy allows low risk", AutonomyMedium, RiskLow, PolicyAllow},
		{"medium autonomy requires approval for medium risk", AutonomyMedium, RiskMedium, PolicyRequireApproval},
		{"high autonomy allows medium risk", AutonomyHigh, RiskMedium, PolicyAllow},
		{"high risk is denied", AutonomyHigh, RiskHigh, PolicyDeny},
	}

	engine := PolicyEngine{}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := engine.Evaluate(PolicyRequest{Autonomy: tt.autonomy, Risk: tt.risk})
			if got.Type != tt.want {
				t.Fatalf("decision=%s, want %s, reason=%s", got.Type, tt.want, got.Reason)
			}
		})
	}
}
```

- [ ] **Step 2: Run RED**

Run: `go test ./...`

Expected: FAIL because `AutonomyLevel`, `RiskLevel`, `PolicyEngine`, and related types are undefined.

- [ ] **Step 3: Implement policy types and decisions**

Create `internal/agent/types.go` with autonomy, risk, policy, run, step, tool call, audit, and event types used by later tasks.

Create `internal/agent/policy.go` with `PolicyEngine.Evaluate(req PolicyRequest) PolicyDecision`.

- [ ] **Step 4: Run GREEN**

Run: `go test ./...`

Expected: PASS.

## Task 2: SQLite Store

**Files:**
- Create: `internal/agent/store_test.go`
- Create: `internal/agent/store.go`
- Modify: `go.mod`

- [ ] **Step 1: Write failing store tests**

Create `internal/agent/store_test.go` to open an in-memory SQLite DB, initialize schema, create a run, append a step, save a tool call, save an audit event, and read a full snapshot.

- [ ] **Step 2: Run RED**

Run: `go test ./...`

Expected: FAIL because `NewSQLiteAgentStore` and store methods are undefined.

- [ ] **Step 3: Add SQLite dependency and store implementation**

Add `modernc.org/sqlite` to `go.mod`. Implement schema creation and methods:

- `CreateRun`
- `GetRun`
- `ListRuns`
- `ListRunsBySession`
- `UpdateRunStatus`
- `AppendStep`
- `SaveToolCall`
- `SaveAuditEvent`
- `RunSnapshot`

- [ ] **Step 4: Run GREEN**

Run: `go test ./...`

Expected: PASS.

## Task 3: Run Service AI Conversation Loop

**Files:**
- Create: `internal/agent/service_test.go`
- Create: `internal/agent/service.go`

- [ ] **Step 1: Write failing run service tests**

Create `internal/agent/service_test.go` to verify `CreateRun` persists a queued run, `StartRun` eventually completes it, and the snapshot contains one AI response step plus audit data.

- [ ] **Step 2: Run RED**

Run: `go test ./...`

Expected: FAIL because `RunService` is undefined.

- [ ] **Step 3: Implement RunService**

Implement `RunService` with an in-process command consumer and AI conversation loop:

1. status `queued`
2. status `running`
3. load prior completed runs in the same `session_id`
4. call `LLMClient.Complete`
5. response step with model input/output
6. status `completed`

- [ ] **Step 4: Run GREEN**

Run: `go test ./...`

Expected: PASS.

## Task 4: Agent API and Admin Dashboard

**Files:**
- Create: `internal/webapp/agent_api.go`
- Create: `internal/webapp/assets/agent_admin.html`
- Modify: `internal/webapp/server.go`
- Modify: `main.go`

- [ ] **Step 1: Add API handlers**

Implement:

- `GET /admin`
- `POST /api/runs`
- `GET /api/runs`
- `GET /api/runs/{id}`
- `GET /api/runs/{id}/events`
- `GET /api/runs/{id}/steps`
- `GET /api/runs/{id}/tool-calls`
- `GET /api/runs/{id}/audit-events`

- [ ] **Step 2: Add dashboard UI**

Create a dashboard with run list, create-run form, run detail, timeline, tool calls, and audit events. Use SSE to refresh the selected run while it executes.

- [ ] **Step 3: Wire server startup**

Initialize SQLite store at `AGENT_DB_PATH` or `agent.db`, create `RunService`, and register admin and run APIs.

- [ ] **Step 4: Run verification**

Run: `go test ./...`

Expected: PASS.

## Task 5: Documentation

**Files:**
- Create: `docs/agent-mvp.md`

- [ ] **Step 1: Write MVP docs**

Document how to run the app, open `/admin`, create an AI conversation run, inspect timeline/tool/audit details, and configure `AGENT_DB_PATH` plus OpenAI-compatible model settings.

- [ ] **Step 2: Final verification**

Run:

```bash
go test ./...
go build ./...
```

Expected: both commands exit 0.
