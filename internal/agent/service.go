package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
)

type AgentStore interface {
	CreateRun(context.Context, CreateRunRequest) (Run, error)
	GetRun(context.Context, string) (Run, error)
	ListRuns(context.Context) ([]Run, error)
	ListRunsBySession(context.Context, string) ([]Run, error)
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
	llm    LLMClient
	tools  *ToolRegistry
	logger *slog.Logger

	commands chan RuntimeCommand

	mu        sync.Mutex
	observers map[string]map[chan RuntimeEvent]struct{}
}

func NewRunService(store AgentStore, policy PolicyEngine, llm LLMClient) *RunService {
	service := &RunService{
		store:     store,
		policy:    policy,
		llm:       llm,
		tools:     NewToolRegistry(),
		logger:    slog.Default(),
		commands:  make(chan RuntimeCommand, 64),
		observers: make(map[string]map[chan RuntimeEvent]struct{}),
	}
	go service.runtimeLoop()
	return service
}

func (s *RunService) Tools() *ToolRegistry {
	return s.tools
}

func (s *RunService) SetLogger(logger *slog.Logger) {
	if logger != nil {
		s.logger = logger
	}
}

func (s *RunService) CreateRun(ctx context.Context, req CreateRunRequest) (Run, error) {
	run, err := s.store.CreateRun(ctx, req)
	if err != nil {
		return Run{}, err
	}
	s.logger.Info("agent run created",
		"run_id", run.ID,
		"session_id", run.SessionID,
		"autonomy", run.Autonomy,
		"enabled_tools", run.EnabledTools,
		"workspace_scope", run.WorkspaceScope,
	)
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

	s.logger.Info("agent run start requested",
		"run_id", run.ID,
		"session_id", run.SessionID,
		"status", run.Status,
	)
	return s.SubmitCommand(ctx, RuntimeCommand{Type: RuntimeCommandStartRun, RunID: runID})
}

func (s *RunService) ListRuns(ctx context.Context) ([]Run, error) {
	return s.store.ListRuns(ctx)
}

func (s *RunService) Snapshot(ctx context.Context, runID string) (RunSnapshot, error) {
	return s.store.RunSnapshot(ctx, runID)
}

func (s *RunService) SessionSnapshot(ctx context.Context, sessionID string) (SessionSnapshot, error) {
	runs, err := s.store.ListRunsBySession(ctx, sessionID)
	if err != nil {
		return SessionSnapshot{}, err
	}
	snapshots := make([]RunSnapshot, 0, len(runs))
	for _, run := range runs {
		snapshot, err := s.store.RunSnapshot(ctx, run.ID)
		if err != nil {
			return SessionSnapshot{}, err
		}
		snapshots = append(snapshots, snapshot)
	}
	return SessionSnapshot{SessionID: sessionID, Runs: snapshots}, nil
}

func (s *RunService) SubmitCommand(ctx context.Context, command RuntimeCommand) error {
	select {
	case s.commands <- command:
		s.logger.Debug("runtime command queued", "command_type", command.Type, "run_id", command.RunID)
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
		s.logger.Debug("runtime command received", "command_type", command.Type, "run_id", command.RunID)
		switch command.Type {
		case RuntimeCommandStartRun:
			s.executeConversationRun(context.Background(), command.RunID)
		}
	}
}

func (s *RunService) executeConversationRun(ctx context.Context, runID string) {
	started := time.Now()
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
	s.logger.Info("agent run started",
		"run_id", run.ID,
		"session_id", run.SessionID,
		"autonomy", run.Autonomy,
		"enabled_tools", run.EnabledTools,
	)
	s.emitRuntimeEvent(RuntimeEvent{Type: RuntimeEventRunStatusChanged, RunID: runID, Data: run})
	_ = s.store.SaveAuditEvent(ctx, AuditEvent{
		OwnerID: run.OwnerID,
		RunID:   runID,
		Actor:   "system",
		Action:  "run_started",
		Payload: "{}",
	})

	if s.llm == nil {
		s.failRun(ctx, runID, errors.New("llm client is not configured"))
		return
	}
	messages, err := s.conversationMessages(ctx, run)
	if err != nil {
		s.failRun(ctx, runID, err)
		return
	}
	s.logger.Debug("agent context built",
		"run_id", run.ID,
		"session_id", run.SessionID,
		"message_count", len(messages),
	)
	response, err := s.runAgentLoop(ctx, run, messages)
	if err != nil {
		s.failRun(ctx, runID, err)
		return
	}

	if err := s.store.UpdateRunStatus(ctx, runID, RunCompleted); err != nil {
		s.failRun(ctx, runID, err)
		return
	}
	completed, _ := s.store.GetRun(ctx, runID)
	s.logger.Info("agent run completed",
		"run_id", run.ID,
		"session_id", run.SessionID,
		"response_step_id", response.ID,
		"duration_ms", time.Since(started).Milliseconds(),
	)
	_ = s.store.SaveAuditEvent(ctx, AuditEvent{
		OwnerID: run.OwnerID,
		RunID:   runID,
		Actor:   "agent",
		Action:  "llm_response_created",
		Payload: mustJSON(map[string]any{"step_id": response.ID}),
	})
	_ = s.store.SaveAuditEvent(ctx, AuditEvent{
		OwnerID: run.OwnerID,
		RunID:   runID,
		Actor:   "system",
		Action:  "run_completed",
		Payload: "{}",
	})
	s.emitRuntimeEvent(RuntimeEvent{Type: RuntimeEventRunCompleted, RunID: runID, Data: completed})
}

func (s *RunService) runAgentLoop(ctx context.Context, run Run, messages []LLMMessage) (Step, error) {
	for index := 1; index <= 8; index++ {
		llmStarted := time.Now()
		reply, err := s.llm.Complete(ctx, LLMRequest{Messages: messages})
		if err != nil {
			return Step{}, err
		}
		decision := parseAgentDecision(reply.Content)
		s.logger.Info("llm decision received",
			"run_id", run.ID,
			"session_id", run.SessionID,
			"step_index", index,
			"decision_type", decision.Type,
			"tool", decision.ToolName,
			"duration_ms", time.Since(llmStarted).Milliseconds(),
		)
		switch decision.Type {
		case "tool_call":
			decisionSummary := formatReasoning(decision)
			decisionStep, err := s.store.AppendStep(ctx, Step{
				RunID:            run.ID,
				Index:            index,
				Type:             StepModelDecision,
				Status:           StepCompleted,
				ModelInput:       messages[len(messages)-1].Content,
				ModelOutput:      reply.Content,
				ReasoningSummary: decisionSummary,
			})
			if err != nil {
				return Step{}, err
			}
			_ = s.store.SaveAuditEvent(ctx, AuditEvent{
				OwnerID: run.OwnerID,
				RunID:   run.ID,
				Actor:   "agent",
				Action:  "model_decision_created",
				Payload: mustJSON(map[string]any{
					"step_id":           decisionStep.ID,
					"type":              decision.Type,
					"tool":              decision.ToolName,
					"reasoning_summary": decisionSummary,
				}),
			})
			s.emitRuntimeEvent(RuntimeEvent{Type: RuntimeEventStepFinished, RunID: run.ID, Data: decisionStep})
			observation, err := s.executeToolDecision(ctx, run, decisionStep, decision)
			if err != nil {
				return Step{}, err
			}
			index++
			observationStep, err := s.store.AppendStep(ctx, Step{
				RunID:       run.ID,
				Index:       index,
				Type:        StepObservation,
				Status:      StepCompleted,
				Observation: observation,
			})
			if err != nil {
				return Step{}, err
			}
			s.emitRuntimeEvent(RuntimeEvent{Type: RuntimeEventStepFinished, RunID: run.ID, Data: observationStep})
			messages = append(messages,
				LLMMessage{Role: "assistant", Content: reply.Content},
				LLMMessage{Role: "user", Content: fmt.Sprintf("Tool result for %s: %s", decision.ToolName, observation)},
			)
		default:
			summary := formatReasoning(decision)
			response, err := s.store.AppendStep(ctx, Step{
				RunID:            run.ID,
				Index:            index,
				Type:             StepResponse,
				Status:           StepCompleted,
				ModelInput:       messages[len(messages)-1].Content,
				ModelOutput:      decision.Content,
				ReasoningSummary: summary,
			})
			if err != nil {
				return Step{}, err
			}
			s.logger.Info("agent final response created",
				"run_id", run.ID,
				"session_id", run.SessionID,
				"step_id", response.ID,
				"step_index", index,
			)
			s.emitRuntimeEvent(RuntimeEvent{Type: RuntimeEventStepFinished, RunID: run.ID, Data: response})
			return response, nil
		}
	}
	return Step{}, errors.New("agent loop exceeded maximum steps")
}

func (s *RunService) executeToolDecision(ctx context.Context, run Run, step Step, decision agentDecision) (string, error) {
	if decision.ToolName == "" {
		return "", errors.New("tool decision missing tool_name")
	}
	if !toolEnabled(run.EnabledTools, decision.ToolName) {
		return "", fmt.Errorf("tool %s is not enabled for this run", decision.ToolName)
	}
	tool, ok := s.tools.Get(decision.ToolName)
	if !ok {
		return "", fmt.Errorf("tool %s is not registered", decision.ToolName)
	}
	spec := tool.Spec()
	policyDecision := s.policy.Evaluate(PolicyRequest{
		Autonomy: run.Autonomy,
		Risk:     spec.Risk,
		ToolName: spec.Name,
	})
	if policyDecision.Type != PolicyAllow {
		s.logger.Warn("tool call blocked by policy",
			"run_id", run.ID,
			"session_id", run.SessionID,
			"tool", spec.Name,
			"risk", spec.Risk,
			"policy_decision", policyDecision.Type,
			"reason", policyDecision.Reason,
		)
		return "", fmt.Errorf("tool %s policy decision: %s", spec.Name, policyDecision.Type)
	}
	call, err := s.store.SaveToolCall(ctx, ToolCall{
		RunID:          run.ID,
		StepID:         step.ID,
		ToolName:       spec.Name,
		ArgumentsJSON:  mustJSON(decision.Arguments),
		RiskLevel:      spec.Risk,
		PolicyDecision: policyDecision.Type,
		ApprovalStatus: "not_required",
		Status:         ToolCallRequested,
	})
	if err != nil {
		return "", err
	}
	s.logger.Info("tool call started",
		"run_id", run.ID,
		"session_id", run.SessionID,
		"tool_call_id", call.ID,
		"tool", spec.Name,
		"risk", spec.Risk,
		"policy_decision", policyDecision.Type,
	)
	_ = s.store.SaveAuditEvent(ctx, AuditEvent{
		OwnerID: run.OwnerID,
		RunID:   run.ID,
		Actor:   "agent",
		Action:  "tool_call_started",
		Payload: mustJSON(map[string]any{
			"tool":         spec.Name,
			"tool_call_id": call.ID,
			"step_id":      step.ID,
			"arguments":    decision.Arguments,
			"risk_level":   spec.Risk,
			"policy":       policyDecision.Type,
		}),
	})
	result, executeErr := tool.Execute(ctx, ToolRequest{
		WorkspaceScope: run.WorkspaceScope,
		Arguments:      decision.Arguments,
	})
	call.Status = ToolCallCompleted
	call.ResultJSON = result.JSON
	if executeErr != nil {
		call.Status = ToolCallFailed
		call.Error = executeErr.Error()
	}
	if _, err := s.store.SaveToolCall(ctx, call); err != nil {
		return "", err
	}
	if executeErr != nil {
		s.logger.Error("tool call failed",
			"run_id", run.ID,
			"session_id", run.SessionID,
			"tool_call_id", call.ID,
			"tool", spec.Name,
			"error", executeErr,
		)
		return "", executeErr
	}
	s.logger.Info("tool call completed",
		"run_id", run.ID,
		"session_id", run.SessionID,
		"tool_call_id", call.ID,
		"tool", spec.Name,
	)
	_ = s.store.SaveAuditEvent(ctx, AuditEvent{
		OwnerID: run.OwnerID,
		RunID:   run.ID,
		Actor:   "agent",
		Action:  "tool_call_finished",
		Payload: mustJSON(map[string]any{"tool": spec.Name, "tool_call_id": call.ID}),
	})
	return result.JSON, nil
}

func (s *RunService) conversationMessages(ctx context.Context, run Run) ([]LLMMessage, error) {
	messages := []LLMMessage{{Role: "system", Content: s.systemPrompt(run)}}
	runs, err := s.store.ListRunsBySession(ctx, run.SessionID)
	if err != nil {
		return nil, err
	}
	for _, previous := range runs {
		if previous.ID == run.ID {
			continue
		}
		if previous.CreatedAt.After(run.CreatedAt) {
			continue
		}
		snapshot, err := s.store.RunSnapshot(ctx, previous.ID)
		if err != nil {
			return nil, err
		}
		answer := latestAssistantMessage(snapshot.Steps)
		if answer == "" {
			continue
		}
		messages = append(messages,
			LLMMessage{Role: "user", Content: previous.Goal},
			LLMMessage{Role: "assistant", Content: answer},
		)
	}
	messages = append(messages, LLMMessage{Role: "user", Content: run.Goal})
	return messages, nil
}

func (s *RunService) systemPrompt(run Run) string {
	var builder strings.Builder
	builder.WriteString("You are the agent runtime decision model.\n")
	builder.WriteString("Runtime contract:\n")
	builder.WriteString("- Return exactly one JSON object. No Markdown fences, no prose outside JSON.\n")
	builder.WriteString("- If the user request can be answered from conversation context alone, return a final JSON object.\n")
	builder.WriteString("- If the request needs current, recent, external, web, sports, price, schedule, or unknown factual information, you MUST call an enabled tool instead of answering from memory.\n")
	builder.WriteString("- If the request depends on local files, you MUST call a filesystem tool before claiming file contents.\n")
	builder.WriteString("- Never promise future tool use. Do not say you will search, browse, inspect, or read; actually call the tool by returning tool_call JSON.\n")
	builder.WriteString("- Use web.search for current or unknown web information. Use web.fetch when you have a URL and need the page contents. Use filesystem.list_dir before filesystem.read_file when the file path is unknown.\n")
	builder.WriteString("- Include reasoning_summary and a concise, user-visible reasoning_trace with 2-5 short steps. Do not include hidden chain-of-thought.\n")
	builder.WriteString(`Tool call shape: {"type":"tool_call","tool_name":"tool.name","arguments":{},"reasoning_summary":"why this tool is required","reasoning_trace":["step 1","step 2"]}.` + "\n")
	builder.WriteString(`Final shape: {"type":"final","content":"answer","reasoning_summary":"why no tool is needed or how observations support the answer","reasoning_trace":["step 1","step 2"]}.`)
	specs := s.tools.Specs()
	if len(specs) == 0 || len(run.EnabledTools) == 0 {
		return builder.String()
	}
	builder.WriteString("\nEnabled tools:")
	for _, spec := range specs {
		if toolEnabled(run.EnabledTools, spec.Name) {
			builder.WriteString("\n- ")
			builder.WriteString(spec.Name)
			builder.WriteString(": ")
			builder.WriteString(spec.Description)
			example := toolCallExample(spec.Name)
			if example != "" {
				builder.WriteString("\n  Example: ")
				builder.WriteString(example)
			}
		}
	}
	return builder.String()
}

func toolCallExample(name string) string {
	switch name {
	case "filesystem.list_dir":
		return `{"type":"tool_call","tool_name":"filesystem.list_dir","arguments":{"path":"."},"reasoning_summary":"Need to inspect the directory.","reasoning_trace":["The request depends on local files.","Listing the directory is the lowest-risk first observation."]}`
	case "filesystem.read_file":
		return `{"type":"tool_call","tool_name":"filesystem.read_file","arguments":{"path":"README.md"},"reasoning_summary":"Need to read the file.","reasoning_trace":["The answer requires file content.","Reading the specific file gives direct evidence."]}`
	case "web.search":
		return `{"type":"tool_call","tool_name":"web.search","arguments":{"query":"current topic","max_results":5},"reasoning_summary":"Need current information.","reasoning_trace":["The request may depend on current web information.","Searching first provides candidate sources."]}`
	case "web.fetch":
		return `{"type":"tool_call","tool_name":"web.fetch","arguments":{"url":"https://example.com","max_chars":12000},"reasoning_summary":"Need to read the source page.","reasoning_trace":["A source URL is available.","Fetching the page gives evidence before answering."]}`
	default:
		return ""
	}
}

func latestAssistantMessage(steps []Step) string {
	for i := len(steps) - 1; i >= 0; i-- {
		if steps[i].Type == StepResponse && steps[i].ModelOutput != "" {
			return steps[i].ModelOutput
		}
	}
	return ""
}

type agentDecision struct {
	Type             string         `json:"type"`
	ToolName         string         `json:"tool_name"`
	Arguments        map[string]any `json:"arguments"`
	Content          string         `json:"content"`
	ReasoningSummary string         `json:"reasoning_summary"`
	ReasoningTrace   []string       `json:"reasoning_trace"`
}

func parseAgentDecision(content string) agentDecision {
	var decision agentDecision
	if err := json.Unmarshal([]byte(extractJSONObject(content)), &decision); err == nil && decision.Type != "" {
		if decision.Type == "final" && decision.Content == "" {
			decision.Content = content
		}
		return decision
	}
	return agentDecision{Type: "final", Content: strings.TrimSpace(content)}
}

func extractJSONObject(content string) string {
	trimmed := strings.TrimSpace(content)
	if strings.HasPrefix(trimmed, "```") {
		lines := strings.Split(trimmed, "\n")
		if len(lines) >= 3 {
			trimmed = strings.Join(lines[1:len(lines)-1], "\n")
		}
	}
	start := strings.Index(trimmed, "{")
	end := strings.LastIndex(trimmed, "}")
	if start >= 0 && end > start {
		return trimmed[start : end+1]
	}
	return trimmed
}

func formatReasoning(decision agentDecision) string {
	summary := strings.TrimSpace(decision.ReasoningSummary)
	if summary == "" {
		switch decision.Type {
		case "tool_call":
			summary = "Selected a tool based on the current request."
		default:
			summary = "Responded based on the available context."
		}
	}
	if len(decision.ReasoningTrace) == 0 {
		return summary
	}
	var builder strings.Builder
	builder.WriteString(summary)
	builder.WriteString("\n\nVisible reasoning trace:")
	for i, item := range decision.ReasoningTrace {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		builder.WriteString("\n")
		builder.WriteString(fmt.Sprintf("%d. %s", i+1, item))
	}
	return builder.String()
}

func toolEnabled(enabled []string, name string) bool {
	for _, item := range enabled {
		if item == name {
			return true
		}
	}
	return false
}

func (s *RunService) failRun(ctx context.Context, runID string, err error) {
	_ = s.store.UpdateRunStatus(ctx, runID, RunFailed)
	s.logger.Error("agent run failed", "run_id", runID, "error", err)
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
