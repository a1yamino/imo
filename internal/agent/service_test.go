package agent

import (
	"context"
	"testing"
	"time"
)

func TestRunServiceCompletesAIConversationRun(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteAgentStore(ctx, ":memory:")
	if err != nil {
		t.Fatalf("NewSQLiteAgentStore: %v", err)
	}
	defer store.Close()

	llm := &fakeLLMClient{response: "这是一个 AI 回复。"}
	service := NewRunService(store, PolicyEngine{}, llm)
	run, err := service.CreateRun(ctx, CreateRunRequest{
		Goal:           "你好，介绍一下你自己",
		Autonomy:       AutonomyMedium,
		EnabledTools:   nil,
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
	if len(snapshot.Steps) != 1 {
		t.Fatalf("steps=%d, want 1", len(snapshot.Steps))
	}
	if snapshot.Steps[0].Type != StepResponse {
		t.Fatalf("step type=%s, want %s", snapshot.Steps[0].Type, StepResponse)
	}
	if snapshot.Steps[0].ModelInput != "你好，介绍一下你自己" {
		t.Fatalf("model input=%q", snapshot.Steps[0].ModelInput)
	}
	if snapshot.Steps[0].ModelOutput != "这是一个 AI 回复。" {
		t.Fatalf("model output=%q", snapshot.Steps[0].ModelOutput)
	}
	if len(snapshot.ToolCalls) != 0 {
		t.Fatalf("tool calls=%d, want 0", len(snapshot.ToolCalls))
	}
	if len(snapshot.AuditEvents) < 3 {
		t.Fatalf("audit events=%d, want at least 3", len(snapshot.AuditEvents))
	}
	if got := llm.lastRequest.Messages[len(llm.lastRequest.Messages)-1].Content; got != "你好，介绍一下你自己" {
		t.Fatalf("llm final user message=%q", got)
	}
}

func TestRunServiceCarriesSessionHistoryAcrossTurns(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteAgentStore(ctx, ":memory:")
	if err != nil {
		t.Fatalf("NewSQLiteAgentStore: %v", err)
	}
	defer store.Close()

	llm := &fakeLLMClient{response: "我是一个测试助手。"}
	service := NewRunService(store, PolicyEngine{}, llm)
	first, err := service.CreateRun(ctx, CreateRunRequest{Goal: "你是谁？"})
	if err != nil {
		t.Fatalf("CreateRun first: %v", err)
	}
	if err := service.StartRun(ctx, first.ID); err != nil {
		t.Fatalf("StartRun first: %v", err)
	}
	waitForRunStatus(t, service, first.ID, RunCompleted)

	llm.response = "我刚才说我是测试助手。"
	second, err := service.CreateRun(ctx, CreateRunRequest{SessionID: first.SessionID, Goal: "你刚才说了什么？"})
	if err != nil {
		t.Fatalf("CreateRun second: %v", err)
	}
	if second.SessionID != first.SessionID {
		t.Fatalf("second session=%q, want %q", second.SessionID, first.SessionID)
	}
	if err := service.StartRun(ctx, second.ID); err != nil {
		t.Fatalf("StartRun second: %v", err)
	}
	waitForRunStatus(t, service, second.ID, RunCompleted)

	want := []LLMMessage{
		{Role: "system", Content: "You are a helpful assistant."},
		{Role: "user", Content: "你是谁？"},
		{Role: "assistant", Content: "我是一个测试助手。"},
		{Role: "user", Content: "你刚才说了什么？"},
	}
	if len(llm.lastRequest.Messages) != len(want) {
		t.Fatalf("messages=%v, want %v", llm.lastRequest.Messages, want)
	}
	for i := range want {
		if llm.lastRequest.Messages[i] != want[i] {
			t.Fatalf("message[%d]=%v, want %v", i, llm.lastRequest.Messages[i], want[i])
		}
	}

	session, err := service.SessionSnapshot(ctx, first.SessionID)
	if err != nil {
		t.Fatalf("SessionSnapshot: %v", err)
	}
	if len(session.Runs) != 2 {
		t.Fatalf("session runs=%d, want 2", len(session.Runs))
	}
}

func TestRunServiceConsumesStartRunCommandAndPublishesRuntimeEvents(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteAgentStore(ctx, ":memory:")
	if err != nil {
		t.Fatalf("NewSQLiteAgentStore: %v", err)
	}
	defer store.Close()

	service := NewRunService(store, PolicyEngine{}, &fakeLLMClient{response: "done"})
	run, err := service.CreateRun(ctx, CreateRunRequest{
		Goal:           "inspect the project",
		Autonomy:       AutonomyMedium,
		EnabledTools:   nil,
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
	if !seen[RuntimeEventStepFinished] {
		t.Fatalf("did not observe %s event; seen=%v", RuntimeEventStepFinished, seen)
	}
}

func waitForRunStatus(t *testing.T, service *RunService, runID string, status RunStatus) RunSnapshot {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		snapshot, err := service.Snapshot(context.Background(), runID)
		if err != nil {
			t.Fatalf("Snapshot: %v", err)
		}
		if snapshot.Run.Status == status {
			return snapshot
		}
		time.Sleep(10 * time.Millisecond)
	}
	snapshot, _ := service.Snapshot(context.Background(), runID)
	t.Fatalf("status=%s, want %s", snapshot.Run.Status, status)
	return RunSnapshot{}
}

type fakeLLMClient struct {
	response    string
	lastRequest LLMRequest
}

func (f *fakeLLMClient) Complete(ctx context.Context, req LLMRequest) (LLMResponse, error) {
	f.lastRequest = req
	return LLMResponse{Content: f.response}, nil
}
