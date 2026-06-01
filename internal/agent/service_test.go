package agent

import (
	"context"
	"testing"
	"time"
)

func TestRunServiceCompletesMockRun(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteAgentStore(ctx, ":memory:")
	if err != nil {
		t.Fatalf("NewSQLiteAgentStore: %v", err)
	}
	defer store.Close()

	service := NewRunService(store, PolicyEngine{})
	run, err := service.CreateRun(ctx, CreateRunRequest{
		Goal:           "inspect the project",
		Autonomy:       AutonomyMedium,
		EnabledTools:   []string{"filesystem.list_dir"},
		WorkspaceScope: ".",
	})
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	if err := service.StartRun(ctx, run.ID); err != nil {
		t.Fatalf("StartRun: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	var snapshot RunSnapshot
	for time.Now().Before(deadline) {
		snapshot, err = service.Snapshot(ctx, run.ID)
		if err != nil {
			t.Fatalf("Snapshot: %v", err)
		}
		if snapshot.Run.Status == RunCompleted {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if snapshot.Run.Status != RunCompleted {
		t.Fatalf("status=%s, want %s", snapshot.Run.Status, RunCompleted)
	}
	if len(snapshot.Steps) != 3 {
		t.Fatalf("steps=%d, want 3", len(snapshot.Steps))
	}
	if snapshot.Steps[0].ReasoningSummary == "" {
		t.Fatal("first step reasoning summary is empty")
	}
	if len(snapshot.ToolCalls) != 1 {
		t.Fatalf("tool calls=%d, want 1", len(snapshot.ToolCalls))
	}
	if snapshot.ToolCalls[0].ToolName != "filesystem.list_dir" {
		t.Fatalf("tool=%s, want filesystem.list_dir", snapshot.ToolCalls[0].ToolName)
	}
	if len(snapshot.AuditEvents) < 3 {
		t.Fatalf("audit events=%d, want at least 3", len(snapshot.AuditEvents))
	}
}

func TestRunServiceConsumesStartRunCommandAndPublishesRuntimeEvents(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteAgentStore(ctx, ":memory:")
	if err != nil {
		t.Fatalf("NewSQLiteAgentStore: %v", err)
	}
	defer store.Close()

	service := NewRunService(store, PolicyEngine{})
	run, err := service.CreateRun(ctx, CreateRunRequest{
		Goal:           "inspect the project",
		Autonomy:       AutonomyMedium,
		EnabledTools:   []string{"filesystem.list_dir"},
		WorkspaceScope: ".",
	})
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	events, cancel := service.ObserveRun(run.ID)
	defer cancel()

	if err := service.StartRun(ctx, run.ID); err != nil {
		t.Fatalf("StartRun: %v", err)
	}

	seen := map[RuntimeEventType]bool{}
	deadline := time.After(2 * time.Second)
	for !seen[RuntimeEventRunCompleted] {
		select {
		case event := <-events:
			seen[event.Type] = true
		case <-deadline:
			t.Fatalf("timed out waiting for runtime events; seen=%v", seen)
		}
	}
	if !seen[RuntimeEventModelDecision] {
		t.Fatalf("did not observe %s event; seen=%v", RuntimeEventModelDecision, seen)
	}
	if !seen[RuntimeEventToolCallFinished] {
		t.Fatalf("did not observe %s event; seen=%v", RuntimeEventToolCallFinished, seen)
	}
}
