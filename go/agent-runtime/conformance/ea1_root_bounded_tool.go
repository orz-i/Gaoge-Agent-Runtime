package conformance

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

// Same frozen Root entry: a known Tool no-effect is not an arbitrary middleware
// completion proof. Only the concrete first-party lifecycle may settle its layer.
// Kernel authority uses each real factory; the timeline port is a deterministic
// in-memory host fixture, not a claim of cross-process Host storage acceptance.
func ea1RootBoundedTool(t *testing.T, factory func(*testing.T) (kernel.Store, kernel.Store), automatic bool) {
	for _, bounded := range []bool{false, true} {
		name := "opaque-tool"
		if bounded {
			name = "bounded-tool-timeline"
		}
		t.Run("P1/"+name, func(t *testing.T) {
			backing, peer := factory(t)
			wrapped := ea1AgentAdmissionWrap(t, backing)
			rt := ea1StartRuntime(t, wrapped)
			observer := ea1StartRuntime(t, peer)
			request := ea1AdmissionRequest()
			request.ToolKeys = []string{"effect"}
			physical := &ea1UnknownAgentPhysical{tool: true, rt: observer}
			registry, err := tools.NewRegistry([]tools.Registration{{Definition: tools.Definition{Key: "effect", Name: "Effect", InputSchema: json.RawMessage(`{"type":"object"}`)}, Handler: tools.HandlerFunc(func(_ context.Context, req tools.ExecutionRequest) (tools.ExecutionResult, error) {
				if req.RunID != request.ID || req.Call.ID != "original-tool-call" || string(req.Call.Arguments) != `{"source":"original"}` {
					return tools.ExecutionResult{}, errors.New("foreign physical Tool request")
				}
				physical.mu.Lock()
				physical.tools = append(physical.tools, req)
				physical.mu.Unlock()
				return tools.RecoverableNoEffectResult(req.Call.ID, tools.NewRecoverableCallErrorAllowCompletion("fixture.known_no_effect", "original request was not applied", nil))
			})}})
			if err != nil {
				t.Fatal(err)
			}
			ports := &ea1BoundedTimelinePorts{runtime: observer, request: request}
			var middleware plugin.ToolMiddleware = ea1OpaqueToolMiddleware{}
			if bounded {
				middleware, err = agent.NewBoundedToolTimelineAdapter(ports)
				if err != nil {
					t.Fatal(err)
				}
			}
			var execution *agent.RunExecution
			if !automatic {
				execution = &agent.RunExecution{OwnerID: "original-owner", LeaseDuration: 3 * time.Minute}
			}
			runner, err := agent.NewRunner(agent.Dependencies{Runtime: rt, Model: physical, Catalog: registry, Executor: registry, RunExecution: execution, ToolMiddleware: []plugin.ToolMiddleware{middleware}})
			if err != nil {
				t.Fatal(err)
			}
			var origin kernel.Snapshot
			wrapped.afterCreate = func(_ context.Context, created kernel.Snapshot) error {
				if !runner.VerifyFrozenStartRequest(created, request) {
					return errors.New("actual Create did not preserve full original Start policy")
				}
				origin = created
				origin.State = append(json.RawMessage(nil), created.State...)
				return nil
			}
			result, callErr := runner.StartRun(t.Context(), request)
			models, toolCalls := physical.counts()
			if models != 2 || toolCalls != 1 || result.Run.Status != kernel.RunStatusCompleted {
				t.Fatalf("original known Tool business result changed: models=%d tools=%d status=%s err=%v", models, toolCalls, result.Run.Status, callErr)
			}
			ea1BoundedToolOrigin(t, origin, result, request)
			if bounded {
				if callErr != nil || len(ports.items) != 2 || ports.items[0].status != "started" || ports.items[1].status != "failed" || ports.items[1].parent != ports.items[0].id || !tools.ValidRecoverableNoEffectReceipt(ports.items[1].result, tools.NewRecoverableCallErrorAllowCompletion("fixture.known_no_effect", "original request was not applied", nil), "original-tool-call") {
					t.Fatalf("bounded original timeline not settled: %+v err=%v", ports.items, callErr)
				}
				if _, err = observer.LookupChildExecution(t.Context(), request.ID); !errors.Is(err, kernel.ErrNotFound) {
					t.Fatalf("bounded Tool scope retained: %v", err)
				}
				return
			}
			if !errors.Is(callErr, agent.ErrRootOutcomeUnknown) {
				t.Fatalf("opaque Tool error manufactured completion: %v", callErr)
			}
			grant, err := observer.LookupChildExecution(t.Context(), request.ID)
			if err != nil {
				t.Fatal(err)
			}
			before, err := observer.Load(t.Context(), request.ID)
			if err != nil {
				t.Fatal(err)
			}
			if err = runner.ReconcileRootPhysicalExecution(t.Context(), request.ID); !errors.Is(err, agent.ErrRootOutcomeUnknown) {
				t.Fatalf("opaque callback retired from known Tool receipt alone: %v", err)
			}
			same, err := observer.LookupChildExecution(t.Context(), request.ID)
			if err != nil || same != grant {
				t.Fatal("unknown reconciliation changed original grant")
			}
			ea1CompBarrierUnchanged(t, observer, before)
			afterModels, afterTools := physical.counts()
			if afterModels != models || afterTools != toolCalls {
				t.Fatal("unknown reconciliation redispatched")
			}
		})
	}
}

type ea1OpaqueToolMiddleware struct{}

func (ea1OpaqueToolMiddleware) Name() string { return "fixture.opaque.tool" }
func (ea1OpaqueToolMiddleware) Tool(ctx context.Context, _ plugin.ToolInvocation, next plugin.ToolNext) (tools.ExecutionResult, error) {
	return next(ctx)
}

type ea1TimelineItem struct {
	id, status, parent string
	result             tools.ExecutionResult
}
type ea1BoundedTimelinePorts struct {
	runtime  *kernel.Runtime
	request  agent.StartRequest
	scope    agent.BoundedToolTimelineScope
	original tools.ExecutionRequest
	items    []ea1TimelineItem
}

func (p *ea1BoundedTimelinePorts) ResolveToolTimeline(ctx context.Context, inv plugin.ToolInvocation) (agent.BoundedToolTimelineScope, error) {
	s, err := p.runtime.Load(ctx, inv.Run.ID)
	if err != nil {
		return agent.BoundedToolTimelineScope{}, err
	}
	var state struct {
		ToolDispatchID string `json:"toolDispatchID"`
	}
	if err = json.Unmarshal(s.State, &state); err != nil {
		return agent.BoundedToolTimelineScope{}, err
	}
	if inv.Run.ID != p.request.ID || inv.Request.RunID != s.Run.ID || s.Run.Actor != p.request.Actor || s.Run.Thread != p.request.Thread || state.ToolDispatchID != inv.Request.Call.ID {
		return agent.BoundedToolTimelineScope{}, kernel.ErrInvalidInput
	}
	source, err := json.Marshal(inv)
	if err != nil {
		return agent.BoundedToolTimelineScope{}, err
	}
	p.original = inv.Request
	p.scope = agent.BoundedToolTimelineScope{RunID: s.Run.ID, TurnID: "original-turn", InvocationID: "original-invocation", Active: true, SourceBinding: source}
	return p.scope, nil
}
func (p *ea1BoundedTimelinePorts) AppendToolTimeline(_ context.Context, scope agent.BoundedToolTimelineScope, inv plugin.ToolInvocation, result tools.ExecutionResult, status, parent string) (string, error) {
	if !reflect.DeepEqual(scope, p.scope) || !reflect.DeepEqual(inv.Request, p.original) {
		return "", kernel.ErrInvalidInput
	}
	id := "timeline:" + inv.Request.Call.ID + ":" + status
	p.items = append(p.items, ea1TimelineItem{id: id, status: status, parent: parent, result: tools.CloneExecutionResult(result)})
	return id, nil
}

// Verify the initial policy at actual Create, then compare immutable facts
// after the intentional, exact capability-refusal guidance insertion. The
// production initial-prefix verifier is unchanged.
func ea1BoundedToolOrigin(t *testing.T, before, after kernel.Snapshot, request agent.StartRequest) {
	t.Helper()
	a, b := before.Run, after.Run
	if a.ID == "" || a.ID != b.ID || a.Kind != b.Kind || a.Actor != b.Actor || a.Thread != b.Thread || a.RequestID != b.RequestID || a.ParentRunID != b.ParentRunID || a.Goal != b.Goal || !a.CreatedAt.Equal(b.CreatedAt) || !reflect.DeepEqual(a.DeadlineAt, b.DeadlineAt) {
		t.Fatal("completed Tool changed original Run identity")
	}
	var initial, final map[string]json.RawMessage
	if err := json.Unmarshal(before.State, &initial); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(after.State, &final); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"originFingerprint", "model", "modelOptions", "toolKeys", "requiredToolKeys", "hostedToolGrants", "outputContract"} {
		if string(initial[key]) != string(final[key]) {
			t.Fatalf("completed Tool changed original source field %s", key)
		}
	}
	var fingerprint string
	if raw := final["originFingerprint"]; len(raw) != 0 {
		if err := json.Unmarshal(raw, &fingerprint); err != nil {
			t.Fatal(err)
		}
	}
	if fingerprint != request.OriginFingerprint || fingerprint != "" {
		t.Fatal("Root acquired a child-only origin fingerprint")
	}
	var initialBudget, finalBudget map[string]json.RawMessage
	if err := json.Unmarshal(initial["budget"], &initialBudget); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(final["budget"], &finalBudget); err != nil {
		t.Fatal(err)
	}
	if string(initialBudget["limits"]) != string(finalBudget["limits"]) {
		t.Fatal("completed Tool changed effective original limits")
	}
	var initialMessages, finalMessages []model.Message
	if err := json.Unmarshal(initial["messages"], &initialMessages); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(final["messages"], &finalMessages); err != nil {
		t.Fatal(err)
	}
	guidance := model.Message{Role: model.RoleSystem, Content: model.TextParts("The unavailable capability cannot be recovered by repeating the same Tool call. A truthful final answer reporting missing sources is allowed now; do not fabricate evidence or citations.")}
	if len(initialMessages) != 1 || len(finalMessages) < 2 || !reflect.DeepEqual(finalMessages[0], guidance) || !reflect.DeepEqual(finalMessages[1], initialMessages[0]) {
		t.Fatal("capability guidance or original user source changed")
	}
}
