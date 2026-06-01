package agent

import (
	"context"
	"testing"
)

func TestSQLiteAgentStorePersistsRunSnapshot(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteAgentStore(ctx, ":memory:")
	if err != nil {
		t.Fatalf("NewSQLiteAgentStore: %v", err)
	}
	defer store.Close()

	run, err := store.CreateRun(ctx, CreateRunRequest{
		Goal:           "inspect the project",
		Autonomy:       AutonomyMedium,
		EnabledTools:   []string{"filesystem.list_dir"},
		WorkspaceScope: ".",
	})
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if run.Status != RunQueued {
		t.Fatalf("status=%s, want %s", run.Status, RunQueued)
	}

	step, err := store.AppendStep(ctx, Step{
		RunID:            run.ID,
		Index:            1,
		Type:             StepModelDecision,
		Status:           StepCompleted,
		ReasoningSummary: "Need to inspect the top-level files.",
		ModelOutput:      `{"type":"call_tool"}`,
	})
	if err != nil {
		t.Fatalf("AppendStep: %v", err)
	}

	call, err := store.SaveToolCall(ctx, ToolCall{
		RunID:          run.ID,
		StepID:         step.ID,
		ToolName:       "filesystem.list_dir",
		ArgumentsJSON:  `{"path":"."}`,
		RiskLevel:      RiskLow,
		PolicyDecision: PolicyAllow,
		Status:         ToolCallCompleted,
		ResultJSON:     `{"entries":["main.go"]}`,
	})
	if err != nil {
		t.Fatalf("SaveToolCall: %v", err)
	}
	if call.ID == "" {
		t.Fatal("tool call ID is empty")
	}

	if err := store.SaveAuditEvent(ctx, AuditEvent{
		OwnerID: run.OwnerID,
		RunID:   run.ID,
		Actor:   "agent",
		Action:  "tool_call_finished",
		Payload: `{"tool":"filesystem.list_dir"}`,
	}); err != nil {
		t.Fatalf("SaveAuditEvent: %v", err)
	}

	if err := store.UpdateRunStatus(ctx, run.ID, RunCompleted); err != nil {
		t.Fatalf("UpdateRunStatus: %v", err)
	}

	snapshot, err := store.RunSnapshot(ctx, run.ID)
	if err != nil {
		t.Fatalf("RunSnapshot: %v", err)
	}
	if snapshot.Run.Status != RunCompleted {
		t.Fatalf("snapshot status=%s, want %s", snapshot.Run.Status, RunCompleted)
	}
	if len(snapshot.Steps) != 1 {
		t.Fatalf("steps=%d, want 1", len(snapshot.Steps))
	}
	if len(snapshot.ToolCalls) != 1 {
		t.Fatalf("tool calls=%d, want 1", len(snapshot.ToolCalls))
	}
	if len(snapshot.AuditEvents) != 1 {
		t.Fatalf("audit events=%d, want 1", len(snapshot.AuditEvents))
	}
}

func TestSQLiteAgentStoreListsRunsBySession(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteAgentStore(ctx, ":memory:")
	if err != nil {
		t.Fatalf("NewSQLiteAgentStore: %v", err)
	}
	defer store.Close()

	first, err := store.CreateRun(ctx, CreateRunRequest{Goal: "first"})
	if err != nil {
		t.Fatalf("CreateRun first: %v", err)
	}
	second, err := store.CreateRun(ctx, CreateRunRequest{SessionID: first.SessionID, Goal: "second"})
	if err != nil {
		t.Fatalf("CreateRun second: %v", err)
	}
	other, err := store.CreateRun(ctx, CreateRunRequest{Goal: "other"})
	if err != nil {
		t.Fatalf("CreateRun other: %v", err)
	}

	runs, err := store.ListRunsBySession(ctx, first.SessionID)
	if err != nil {
		t.Fatalf("ListRunsBySession: %v", err)
	}
	if len(runs) != 2 {
		t.Fatalf("runs=%d, want 2", len(runs))
	}
	if runs[0].ID != first.ID || runs[1].ID != second.ID {
		t.Fatalf("runs order=%v, want first then second", []string{runs[0].ID, runs[1].ID})
	}
	if runs[0].SessionID == other.SessionID {
		t.Fatal("session filter included an unrelated run")
	}
}
