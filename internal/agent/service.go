package agent

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
)

type AgentStore interface {
	CreateRun(context.Context, CreateRunRequest) (Run, error)
	GetRun(context.Context, string) (Run, error)
	ListRuns(context.Context) ([]Run, error)
	UpdateRunStatus(context.Context, string, RunStatus) error
	AppendStep(context.Context, Step) (Step, error)
	SaveToolCall(context.Context, ToolCall) (ToolCall, error)
	SaveAuditEvent(context.Context, AuditEvent) error
	RunSnapshot(context.Context, string) (RunSnapshot, error)
}

// RunService owns the runtime boundary: it consumes RuntimeCommand values,
// advances run state, persists results, and emits RuntimeEvent values. HTTP and
// the dashboard are observers/controllers around this runtime, not the driver.
type RunService struct {
	store  AgentStore
	policy PolicyEngine

	commands chan RuntimeCommand

	mu        sync.Mutex
	observers map[string]map[chan RuntimeEvent]struct{}
}

func NewRunService(store AgentStore, policy PolicyEngine) *RunService {
	service := &RunService{
		store:     store,
		policy:    policy,
		commands:  make(chan RuntimeCommand, 64),
		observers: make(map[string]map[chan RuntimeEvent]struct{}),
	}
	go service.runtimeLoop()
	return service
}

func (s *RunService) CreateRun(ctx context.Context, req CreateRunRequest) (Run, error) {
	run, err := s.store.CreateRun(ctx, req)
	if err != nil {
		return Run{}, err
	}
	if err := s.store.SaveAuditEvent(ctx, AuditEvent{
		OwnerID: run.OwnerID,
		RunID:   run.ID,
		Actor:   "user",
		Action:  "run_created",
		Payload: mustJSON(map[string]any{"goal": run.Goal, "autonomy_level": run.Autonomy}),
	}); err != nil {
		return Run{}, err
	}
	s.emitRuntimeEvent(RuntimeEvent{Type: RuntimeEventRunCreated, RunID: run.ID, Data: run})
	return run, nil
}

func (s *RunService) StartRun(ctx context.Context, runID string) error {
	run, err := s.store.GetRun(ctx, runID)
	if err != nil {
		return err
	}
	if run.Status != RunQueued {
		return errors.New("run must be queued to start")
	}

	return s.SubmitCommand(ctx, RuntimeCommand{Type: RuntimeCommandStartRun, RunID: runID})
}

func (s *RunService) ListRuns(ctx context.Context) ([]Run, error) {
	return s.store.ListRuns(ctx)
}

func (s *RunService) Snapshot(ctx context.Context, runID string) (RunSnapshot, error) {
	return s.store.RunSnapshot(ctx, runID)
}

func (s *RunService) SubmitCommand(ctx context.Context, command RuntimeCommand) error {
	select {
	case s.commands <- command:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// ObserveRun returns a best-effort stream of runtime events for observers such
// as dashboards or logs. It is not the runtime execution path; command execution
// enters through SubmitCommand and is consumed by runtimeLoop.
func (s *RunService) ObserveRun(runID string) (<-chan RuntimeEvent, func()) {
	// Buffered channels keep a slow observer from blocking the runtime loop.
	ch := make(chan RuntimeEvent, 32)
	s.mu.Lock()
	if s.observers[runID] == nil {
		s.observers[runID] = make(map[chan RuntimeEvent]struct{})
	}
	s.observers[runID][ch] = struct{}{}
	s.mu.Unlock()

	cancel := func() {
		s.mu.Lock()
		delete(s.observers[runID], ch)
		if len(s.observers[runID]) == 0 {
			delete(s.observers, runID)
		}
		s.mu.Unlock()
		close(ch)
	}
	return ch, cancel
}

func (s *RunService) runtimeLoop() {
	// This is the runtime's command consumer. Later commands such as approve,
	// cancel, or user input should enter here rather than calling execution
	// methods from HTTP handlers.
	for command := range s.commands {
		switch command.Type {
		case RuntimeCommandStartRun:
			s.executeMockRun(context.Background(), command.RunID)
		}
	}
}

func (s *RunService) executeMockRun(ctx context.Context, runID string) {
	run, err := s.store.GetRun(ctx, runID)
	if err != nil {
		s.failRun(ctx, runID, err)
		return
	}

	if err := s.store.UpdateRunStatus(ctx, runID, RunRunning); err != nil {
		s.failRun(ctx, runID, err)
		return
	}
	run.Status = RunRunning
	s.emitRuntimeEvent(RuntimeEvent{Type: RuntimeEventRunStatusChanged, RunID: runID, Data: run})
	_ = s.store.SaveAuditEvent(ctx, AuditEvent{
		OwnerID: run.OwnerID,
		RunID:   runID,
		Actor:   "system",
		Action:  "run_started",
		Payload: "{}",
	})

	// This deterministic decision is the MVP stand-in for AgentCore. It exercises
	// the same persistence and dashboard path that future LLM decisions will use.
	decision := map[string]any{
		"type":              "call_tool",
		"reasoning_summary": "需要先查看工作区顶层文件，建立任务上下文。",
		"tool_name":         "filesystem.list_dir",
		"arguments":         map[string]any{"path": run.WorkspaceScope},
	}
	decisionJSON := mustJSON(decision)
	step, err := s.store.AppendStep(ctx, Step{
		RunID:            runID,
		Index:            1,
		Type:             StepModelDecision,
		Status:           StepCompleted,
		ModelInput:       run.Goal,
		ModelOutput:      decisionJSON,
		ReasoningSummary: "需要先查看工作区顶层文件，建立任务上下文。",
	})
	if err != nil {
		s.failRun(ctx, runID, err)
		return
	}
	s.emitRuntimeEvent(RuntimeEvent{Type: RuntimeEventModelDecision, RunID: runID, Data: step})

	// Keep Policy in the loop even for mock data so approval/deny behavior can
	// replace this tool call without changing RunService's outer flow.
	policy := s.policy.Evaluate(PolicyRequest{Autonomy: run.Autonomy, Risk: RiskLow, ToolName: "filesystem.list_dir"})
	call, err := s.store.SaveToolCall(ctx, ToolCall{
		RunID:          runID,
		StepID:         step.ID,
		ToolName:       "filesystem.list_dir",
		ArgumentsJSON:  mustJSON(map[string]any{"path": run.WorkspaceScope}),
		RiskLevel:      RiskLow,
		PolicyDecision: policy.Type,
		ApprovalStatus: "not_required",
		Status:         ToolCallCompleted,
		ResultJSON:     mustJSON(map[string]any{"entries": []string{"main.go", "internal/", "docs/"}}),
	})
	if err != nil {
		s.failRun(ctx, runID, err)
		return
	}
	s.emitRuntimeEvent(RuntimeEvent{Type: RuntimeEventToolCallFinished, RunID: runID, Data: call})
	_ = s.store.SaveAuditEvent(ctx, AuditEvent{
		OwnerID: run.OwnerID,
		RunID:   runID,
		Actor:   "agent",
		Action:  "tool_call_finished",
		Payload: mustJSON(map[string]any{"tool_name": call.ToolName, "policy_decision": call.PolicyDecision}),
	})

	observation, err := s.store.AppendStep(ctx, Step{
		RunID:       runID,
		Index:       2,
		Type:        StepObservation,
		Status:      StepCompleted,
		Observation: "filesystem.list_dir 返回 main.go、internal/、docs/，说明当前是一个分层后的 Go Web 项目。",
	})
	if err != nil {
		s.failRun(ctx, runID, err)
		return
	}
	s.emitRuntimeEvent(RuntimeEvent{Type: RuntimeEventStepFinished, RunID: runID, Data: observation})

	response, err := s.store.AppendStep(ctx, Step{
		RunID:            runID,
		Index:            3,
		Type:             StepResponse,
		Status:           StepCompleted,
		ReasoningSummary: "已完成第一轮环境观察，MVP mock run 到此结束。",
		ModelOutput:      "Mock run completed. The agent inspected the workspace and recorded the result.",
	})
	if err != nil {
		s.failRun(ctx, runID, err)
		return
	}
	s.emitRuntimeEvent(RuntimeEvent{Type: RuntimeEventStepFinished, RunID: runID, Data: response})

	if err := s.store.UpdateRunStatus(ctx, runID, RunCompleted); err != nil {
		s.failRun(ctx, runID, err)
		return
	}
	completed, _ := s.store.GetRun(ctx, runID)
	_ = s.store.SaveAuditEvent(ctx, AuditEvent{
		OwnerID: run.OwnerID,
		RunID:   runID,
		Actor:   "system",
		Action:  "run_completed",
		Payload: "{}",
	})
	s.emitRuntimeEvent(RuntimeEvent{Type: RuntimeEventRunCompleted, RunID: runID, Data: completed})
}

func (s *RunService) failRun(ctx context.Context, runID string, err error) {
	_ = s.store.UpdateRunStatus(ctx, runID, RunFailed)
	_ = s.store.SaveAuditEvent(ctx, AuditEvent{
		OwnerID: defaultOwnerID,
		RunID:   runID,
		Actor:   "system",
		Action:  "run_failed",
		Payload: mustJSON(map[string]any{"error": err.Error()}),
	})
	s.emitRuntimeEvent(RuntimeEvent{Type: RuntimeEventRunFailed, RunID: runID, Data: map[string]string{"error": err.Error()}})
}

func (s *RunService) emitRuntimeEvent(event RuntimeEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// This is an in-memory observation stream, not the durable event log.
	// Durable state is still written through the store before events are emitted.
	for ch := range s.observers[event.RunID] {
		select {
		case ch <- event:
		default:
			// Dropping is acceptable: clients can reload a complete snapshot by run ID.
		}
	}
}

func mustJSON(value any) string {
	data, err := json.Marshal(value)
	if err != nil {
		return "{}"
	}
	return string(data)
}
