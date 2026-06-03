package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
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
		{Role: "system", Content: ""},
		{Role: "user", Content: "你是谁？"},
		{Role: "assistant", Content: "我是一个测试助手。"},
		{Role: "user", Content: "你刚才说了什么？"},
	}
	if len(llm.lastRequest.Messages) != len(want) {
		t.Fatalf("messages=%v, want %v", llm.lastRequest.Messages, want)
	}
	for i := range want {
		if i == 0 {
			if llm.lastRequest.Messages[i].Role != "system" || !strings.Contains(llm.lastRequest.Messages[i].Content, "Runtime contract") {
				t.Fatalf("message[%d]=%v, want system prompt", i, llm.lastRequest.Messages[i])
			}
			continue
		}
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

func TestRunServiceAppliesStreamSlashCommandToSession(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteAgentStore(ctx, ":memory:")
	if err != nil {
		t.Fatalf("NewSQLiteAgentStore: %v", err)
	}
	defer store.Close()

	llm := &fakeLLMClient{response: `{"type":"final","content":"streamed answer"}`}
	service := NewRunService(store, PolicyEngine{}, llm)
	commandRun, err := service.CreateRun(ctx, CreateRunRequest{Goal: "/stream on"})
	if err != nil {
		t.Fatalf("CreateRun command: %v", err)
	}
	if err := service.StartRun(ctx, commandRun.ID); err != nil {
		t.Fatalf("StartRun command: %v", err)
	}

	commandSnapshot := waitForRunStatus(t, service, commandRun.ID, RunCompleted)
	if len(llm.requests) != 0 {
		t.Fatalf("slash command called llm %d times, want 0", len(llm.requests))
	}
	if got := latestAssistantMessage(commandSnapshot.Steps); !strings.Contains(strings.ToLower(got), "stream on") {
		t.Fatalf("slash command response=%q, want stream on confirmation", got)
	}
	auditActions := map[string]bool{}
	for _, event := range commandSnapshot.AuditEvents {
		auditActions[event.Action] = true
	}
	if !auditActions["runtime_option_updated"] {
		t.Fatalf("runtime_option_updated audit event missing; got %v", auditActions)
	}

	chatRun, err := service.CreateRun(ctx, CreateRunRequest{SessionID: commandRun.SessionID, Goal: "正常对话"})
	if err != nil {
		t.Fatalf("CreateRun chat: %v", err)
	}
	if err := service.StartRun(ctx, chatRun.ID); err != nil {
		t.Fatalf("StartRun chat: %v", err)
	}
	waitForRunStatus(t, service, chatRun.ID, RunCompleted)
	if len(llm.requests) != 1 {
		t.Fatalf("llm calls=%d, want 1", len(llm.requests))
	}
	if !llm.lastRequest.Stream {
		t.Fatalf("llm request stream=false, want true")
	}
}

func TestRunServiceCanDisableStreamWithSlashCommand(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteAgentStore(ctx, ":memory:")
	if err != nil {
		t.Fatalf("NewSQLiteAgentStore: %v", err)
	}
	defer store.Close()

	llm := &fakeLLMClient{response: `{"type":"final","content":"plain answer"}`}
	service := NewRunService(store, PolicyEngine{}, llm)
	onRun, err := service.CreateRun(ctx, CreateRunRequest{Goal: "/stream on"})
	if err != nil {
		t.Fatalf("CreateRun on: %v", err)
	}
	if err := service.StartRun(ctx, onRun.ID); err != nil {
		t.Fatalf("StartRun on: %v", err)
	}
	waitForRunStatus(t, service, onRun.ID, RunCompleted)

	offRun, err := service.CreateRun(ctx, CreateRunRequest{SessionID: onRun.SessionID, Goal: "/stream off"})
	if err != nil {
		t.Fatalf("CreateRun off: %v", err)
	}
	if err := service.StartRun(ctx, offRun.ID); err != nil {
		t.Fatalf("StartRun off: %v", err)
	}
	waitForRunStatus(t, service, offRun.ID, RunCompleted)

	chatRun, err := service.CreateRun(ctx, CreateRunRequest{SessionID: onRun.SessionID, Goal: "正常对话"})
	if err != nil {
		t.Fatalf("CreateRun chat: %v", err)
	}
	if err := service.StartRun(ctx, chatRun.ID); err != nil {
		t.Fatalf("StartRun chat: %v", err)
	}
	waitForRunStatus(t, service, chatRun.ID, RunCompleted)
	if len(llm.requests) != 1 {
		t.Fatalf("llm calls=%d, want 1", len(llm.requests))
	}
	if llm.lastRequest.Stream {
		t.Fatalf("llm request stream=true, want false")
	}
}

func TestRunServiceExecutesFilesystemToolDecision(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "README.md"), []byte("# Demo"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	store, err := NewSQLiteAgentStore(ctx, ":memory:")
	if err != nil {
		t.Fatalf("NewSQLiteAgentStore: %v", err)
	}
	defer store.Close()

	llm := &fakeLLMClient{responses: []string{
		`{"type":"tool_call","tool_name":"filesystem.list_dir","arguments":{"path":"."},"reasoning_summary":"Need to inspect the workspace."}`,
		`{"type":"final","content":"I found README.md.","reasoning_summary":"The directory listing contains README.md."}`,
	}}
	service := NewRunService(store, PolicyEngine{}, llm)
	RegisterFilesystemTools(service.Tools())
	run, err := service.CreateRun(ctx, CreateRunRequest{
		Goal:           "List files",
		Autonomy:       AutonomyMedium,
		EnabledTools:   []string{"filesystem.list_dir"},
		WorkspaceScope: workspace,
	})
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if err := service.StartRun(ctx, run.ID); err != nil {
		t.Fatalf("StartRun: %v", err)
	}

	snapshot := waitForRunStatus(t, service, run.ID, RunCompleted)
	if len(snapshot.ToolCalls) != 1 {
		t.Fatalf("tool calls=%d, want 1", len(snapshot.ToolCalls))
	}
	if snapshot.ToolCalls[0].ToolName != "filesystem.list_dir" {
		t.Fatalf("tool name=%q", snapshot.ToolCalls[0].ToolName)
	}
	if snapshot.ToolCalls[0].Status != ToolCallCompleted {
		t.Fatalf("tool status=%q", snapshot.ToolCalls[0].Status)
	}
	if !strings.Contains(snapshot.ToolCalls[0].ResultJSON, "README.md") {
		t.Fatalf("tool result=%s, want README.md", snapshot.ToolCalls[0].ResultJSON)
	}
	if len(snapshot.Steps) != 3 {
		t.Fatalf("steps=%d, want decision, observation, response", len(snapshot.Steps))
	}
	if snapshot.Steps[0].Type != StepModelDecision || snapshot.Steps[1].Type != StepObservation || snapshot.Steps[2].Type != StepResponse {
		t.Fatalf("step types=%v", []StepType{snapshot.Steps[0].Type, snapshot.Steps[1].Type, snapshot.Steps[2].Type})
	}
	if snapshot.Steps[2].ModelOutput != "I found README.md." {
		t.Fatalf("final output=%q", snapshot.Steps[2].ModelOutput)
	}
	auditActions := map[string]bool{}
	for _, event := range snapshot.AuditEvents {
		auditActions[event.Action] = true
	}
	for _, action := range []string{"model_decision_created", "tool_call_started", "tool_call_finished"} {
		if !auditActions[action] {
			t.Fatalf("audit action %q missing; got %v", action, auditActions)
		}
	}
	if len(llm.requests) != 2 {
		t.Fatalf("llm calls=%d, want 2", len(llm.requests))
	}
	if !strings.Contains(llm.requests[1].Messages[len(llm.requests[1].Messages)-1].Content, "README.md") {
		t.Fatalf("second llm request did not include tool observation: %#v", llm.requests[1].Messages)
	}
}

func TestRunServiceStoresVisibleReasoningTrace(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteAgentStore(ctx, ":memory:")
	if err != nil {
		t.Fatalf("NewSQLiteAgentStore: %v", err)
	}
	defer store.Close()

	llm := &fakeLLMClient{response: `{"type":"final","content":"可以。","reasoning_summary":"Answer from context.","reasoning_trace":["Read the user request.","No external tool is needed.","Reply directly."]}`}
	service := NewRunService(store, PolicyEngine{}, llm)
	run, err := service.CreateRun(ctx, CreateRunRequest{Goal: "直接回答"})
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if err := service.StartRun(ctx, run.ID); err != nil {
		t.Fatalf("StartRun: %v", err)
	}

	snapshot := waitForRunStatus(t, service, run.ID, RunCompleted)
	if len(snapshot.Steps) != 1 {
		t.Fatalf("steps=%d, want 1", len(snapshot.Steps))
	}
	summary := snapshot.Steps[0].ReasoningSummary
	for _, want := range []string{"Answer from context.", "1. Read the user request.", "2. No external tool is needed.", "3. Reply directly."} {
		if !strings.Contains(summary, want) {
			t.Fatalf("reasoning summary %q does not contain %q", summary, want)
		}
	}
}

func TestSystemPromptIncludesEnabledWebSearchExample(t *testing.T) {
	service := NewRunService(nil, PolicyEngine{}, nil)
	RegisterSerperWebTools(service.Tools(), SerperConfig{APIKey: "key"})
	RegisterWebFetchTool(service.Tools(), nil)

	prompt := service.systemPrompt(Run{
		EnabledTools: []string{"web.search", "web.fetch"},
	})

	if !strings.Contains(prompt, `"tool_name":"web.search"`) {
		t.Fatalf("prompt does not include web.search tool-call example:\n%s", prompt)
	}
	for _, want := range []string{
		"Runtime contract",
		"MUST call an enabled tool",
		"Never promise future tool use",
		"Return exactly one JSON object",
		"Use web.search for current",
		"Use web.fetch when you have a URL",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt does not contain %q:\n%s", want, prompt)
		}
	}
	if !strings.Contains(prompt, "reasoning_trace") {
		t.Fatalf("prompt does not request visible reasoning trace:\n%s", prompt)
	}
}

func TestParseAgentDecisionAcceptsJSONCodeBlock(t *testing.T) {
	decision := parseAgentDecision("```json\n{\"type\":\"tool_call\",\"tool_name\":\"web.search\",\"arguments\":{\"query\":\"latest agent news\"},\"reasoning_summary\":\"Need current information.\"}\n```")

	if decision.Type != "tool_call" || decision.ToolName != "web.search" {
		t.Fatalf("decision=%+v", decision)
	}
	if decision.Arguments["query"] != "latest agent news" {
		t.Fatalf("arguments=%+v", decision.Arguments)
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
	responses   []string
	lastRequest LLMRequest
	requests    []LLMRequest
}

func (f *fakeLLMClient) Complete(ctx context.Context, req LLMRequest) (LLMResponse, error) {
	f.lastRequest = req
	f.requests = append(f.requests, req)
	if len(f.responses) > 0 {
		response := f.responses[0]
		f.responses = f.responses[1:]
		return LLMResponse{Content: response}, nil
	}
	return LLMResponse{Content: f.response}, nil
}
