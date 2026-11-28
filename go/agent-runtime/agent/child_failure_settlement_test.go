package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	interactionadapter "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/adapters/interaction"
	runtimecontext "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/context"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/interaction"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

var errChildFailureFixture = errors.New("fixture unresolved physical outcome")

type childFailurePorts struct {
	pressureTestPorts
	deny  bool
	forge bool
}

func (p *childFailurePorts) ResolveWindow(ctx context.Context, request model.Request) (runtimecontext.ModelWindow, error) {
	plan, err := p.Plan(ctx, request)
	return plan.ModelWindow, err
}
func (p *childFailurePorts) Plan(ctx context.Context, request model.Request) (BoundedContextPlan, error) {
	if p.forge {
		return BoundedContextPlan{}, model.NewClassifiedError(errChildFailureFixture, "context.input_budget_exceeded")
	}
	if !p.deny {
		return BoundedContextPlan{Request: request}, nil
	}
	return p.pressureTestPorts.Plan(ctx, request)
}

type childFailureModel struct {
	calls int
	tool  bool
	fail  bool
}

func (m *childFailureModel) Generate(context.Context, model.Request) (model.Response, error) {
	m.calls++
	if m.fail {
		return model.Response{}, errChildFailureFixture
	}
	if m.tool {
		return model.Response{ToolCalls: []tools.Call{{ID: "approval-call", ToolKey: "effect", Arguments: json.RawMessage("{}")}}}, nil
	}
	return model.Response{Content: "done"}, nil
}

type childFailureApproval struct{ fail bool }

func (childFailureApproval) Name() string { return "child-failure-approval" }
func (p childFailureApproval) Approval(context.Context, plugin.ToolInvocation) (plugin.ApprovalRequirement, error) {
	if p.fail {
		return "", errChildFailureFixture
	}
	return plugin.ApprovalRequired, nil
}

type childFailureCompletion struct{}

func (childFailureCompletion) ValidateCompletion(context.Context, kernel.Run, model.Response) (CompletionCorrection, error) {
	return CompletionCorrection{}, errChildFailureFixture
}

type childFailureMiddleware struct{}

func (childFailureMiddleware) Name() string { return "child-failure-middleware" }
func (childFailureMiddleware) Run(ctx context.Context, _ plugin.RunInvocation, next plugin.RunNext) (kernel.Snapshot, error) {
	result, err := next(ctx)
	return result, errors.Join(err, errChildFailureFixture)
}

type childFailureStore struct {
	*memory.Store
	fault           string
	marks, finishes int
}

func (s *childFailureStore) Apply(ctx context.Context, id string, rev uint64, mutation kernel.StoreMutation) (kernel.Snapshot, error) {
	if mutation.Record.Run.Status == kernel.RunStatusFailed && s.fault == "failure-cas" {
		return kernel.Snapshot{}, errChildFailureFixture
	}
	return s.Store.Apply(ctx, id, rev, mutation)
}
func (s *childFailureStore) MarkChildExecutionSettled(ctx context.Context, g kernel.ChildExecutionGrant, p kernel.ChildExecutionSettlement) error {
	s.marks++
	if s.fault == "mark" {
		return errChildFailureFixture
	}
	err := s.Store.MarkChildExecutionSettled(ctx, g, p)
	if err == nil && s.fault == "mark-ack" {
		return errChildFailureFixture
	}
	return err
}
func (s *childFailureStore) FinishChildExecution(ctx context.Context, g kernel.ChildExecutionGrant, p kernel.ChildExecutionSettlement) error {
	s.finishes++
	if s.fault == "finish" {
		return errChildFailureFixture
	}
	return s.Store.FinishChildExecution(ctx, g, p)
}

func TestChildPhysicalFailureSettlement(t *testing.T) {
	for _, tc := range []struct {
		name, operation, fault, code string
		wantSettled                  bool
		wantCalls, wantTools         int
	}{
		{"start known context denial", "start", "", "context.input_budget_exceeded", true, 0, 0},
		{"resume known context denial", "resume", "", "context.input_budget_exceeded", true, 0, 0},
		{"approval rejected then context denial", "reject", "", "context.input_budget_exceeded", true, 1, 0},
		{"approval accepted then context denial", "approve", "", "context.input_budget_exceeded", true, 1, 1},
		{"start forged classification", "start", "forged", "context.input_budget_exceeded", false, 0, 0},
		{"resume forged classification", "resume", "forged", "context.input_budget_exceeded", false, 0, 0},
		{"approval forged classification", "reject", "forged", "context.input_budget_exceeded", false, 1, 0},
		{"unknown model effect", "start", "model", "agent.model_failed", false, 1, 0},
		{"unknown tool effect after approval", "approve", "tool", "", false, 1, 1},
		{"unknown approval policy", "start", "approval-policy", "agent.approval_policy_failed", false, 1, 0},
		{"unknown completion policy", "start", "completion", "agent.completion_validation_failed", false, 1, 0},
		{"opaque middleware adds error", "start", "middleware", "context.input_budget_exceeded", false, 0, 0},
		{"terminal CAS fails", "start", "failure-cas", "", false, 0, 0},
		{"settlement marker fails", "start", "mark", "context.input_budget_exceeded", false, 0, 0},
		{"settlement marker ACK lost", "start", "mark-ack", "context.input_budget_exceeded", false, 0, 0},
		{"retirement fails", "start", "finish", "context.input_budget_exceeded", false, 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &childFailureStore{Store: memory.NewStore()}
			rt, err := kernel.New(kernel.Dependencies{Store: store})
			if err != nil {
				t.Fatal(err)
			}
			actor := kernel.ActorRef{TenantID: "tenant", ActorID: "actor"}
			thread := kernel.ThreadRef{Kind: "conversation", ID: "thread"}
			parent, err := rt.Create(t.Context(), kernel.CreateRequest{ID: "parent", Kind: "workflow", Actor: actor, Thread: thread, Goal: "parent", State: json.RawMessage("{}")})
			if err != nil {
				t.Fatal(err)
			}
			approval := tc.operation == "approve" || tc.operation == "reject" || tc.fault == "approval-policy"
			ports := &childFailurePorts{}
			m := &childFailureModel{tool: approval, fail: tc.fault == "model"}
			adapter, err := NewBoundedContextAdapter(runtimecontext.NewManager(runtimecontext.Dependencies{}), ports)
			if err != nil {
				t.Fatal(err)
			}
			deps := Dependencies{Runtime: rt, Model: m, BoundedContext: adapter, RunMiddleware: []plugin.RunMiddleware{adapter},
				StartControlPreflight:    func(context.Context, StartRequest) error { return nil },
				ApprovalControlPreflight: func(context.Context, kernel.Snapshot) error { return nil }}
			if tc.fault == "completion" {
				deps.CompletionPolicies = []CompletionPolicy{childFailureCompletion{}}
			}
			if tc.fault == "middleware" {
				deps.RunMiddleware = append(deps.RunMiddleware, childFailureMiddleware{})
			}
			toolCalls := 0
			request := StartRequest{ID: "child", RequestID: "request", ParentRunID: parent.Run.ID, OriginFingerprint: FrozenOriginFingerprint([]byte("{}")), Actor: actor, Thread: thread, Goal: strings.Repeat("B", 512), Model: "model"}
			if approval {
				registry, e := tools.NewRegistry([]tools.Registration{{
					Definition: tools.Definition{Key: "effect", Name: "effect", InputSchema: json.RawMessage(`{"type":"object"}`)},
					Handler: tools.HandlerFunc(func(_ context.Context, r tools.ExecutionRequest) (tools.ExecutionResult, error) {
						toolCalls++
						if tc.fault == "tool" {
							return tools.ExecutionResult{}, errChildFailureFixture
						}
						return tools.ExecutionResult{Content: json.RawMessage("{}"), Receipt: tools.Receipt{ExecutionID: r.Call.ID, Disposition: "committed"}}, nil
					}),
				}})
				if e != nil {
					t.Fatal(e)
				}
				gate, e := interaction.New(rt)
				if e != nil {
					t.Fatal(e)
				}
				deps.Catalog, deps.Executor = registry, registry
				deps.Approvals = interactionadapter.New(gate)
				deps.ApprovalPolicies = []plugin.ApprovalPolicy{childFailureApproval{fail: tc.fault == "approval-policy"}}
				request.ToolKeys = []string{"effect"}
			}
			runner, err := NewRunner(deps)
			if err != nil {
				t.Fatal(err)
			}
			var result kernel.Snapshot
			switch tc.operation {
			case "resume":
				result, err = runner.PrepareRun(t.Context(), request)
				if err != nil {
					t.Fatal(err)
				}
			case "approve", "reject":
				result, err = runner.StartRun(t.Context(), request)
				if err != nil || result.Run.Status != kernel.RunStatusWaitingInput {
					t.Fatalf("approval setup: %s %v", result.Run.Status, err)
				}
			}
			// The preceding Prepare/wait is a separate successfully retired grant.
			store.marks, store.finishes = 0, 0
			store.fault = tc.fault
			ports.deny = tc.fault != "model" && tc.fault != "completion" && tc.fault != "approval-policy" && tc.fault != "tool"
			ports.forge = tc.fault == "forged"
			switch tc.operation {
			case "start":
				result, err = runner.StartRun(t.Context(), request)
			case "resume":
				result, err = runner.Resume(t.Context(), result.Run.ID, result.Run.Revision)
			case "approve", "reject":
				decision := plugin.ApprovalReject
				if tc.operation == "approve" {
					decision = plugin.ApprovalApprove
				}
				result, err = runner.ResolveApproval(t.Context(), result.Run.ID, result.Run.Revision, plugin.ApprovalResponse{Decision: decision})
			}
			latest, loadErr := rt.Load(t.Context(), request.ID)
			if loadErr != nil {
				t.Fatal(loadErr)
			}
			if err == nil || latest.Run.ErrorCode != tc.code || m.calls != tc.wantCalls || toolCalls != tc.wantTools {
				t.Fatalf("outcome=%s code=%s returned=%s error=%v model=%d tool=%d", latest.Run.Status, latest.Run.ErrorCode, result.Run.Status, err, m.calls, toolCalls)
			}
			if tc.code != "" && tc.fault != "model" && latest.Run.Status != kernel.RunStatusFailed {
				t.Fatalf("expected persisted failure: %+v", latest.Run)
			}
			_, lookup := rt.LookupChildExecution(t.Context(), request.ID)
			p, loadErr := rt.Load(t.Context(), parent.Run.ID)
			if loadErr != nil {
				t.Fatal(loadErr)
			}
			_, cancelErr := rt.Cancel(t.Context(), p.Run.ID, p.Run.Revision, "test original parent control")
			if tc.wantSettled {
				if !errors.Is(err, runtimecontext.ErrBudgetExceeded) || errors.Is(err, kernel.ErrChildExecutionBusy) || !errors.Is(lookup, kernel.ErrNotFound) || cancelErr != nil || store.marks != 1 || store.finishes != 1 {
					t.Fatalf("committed failure not settled: err=%v grant=%v cancel=%v mark=%d finish=%d", err, lookup, cancelErr, store.marks, store.finishes)
				}
			} else {
				if !errors.Is(err, kernel.ErrChildExecutionBusy) || lookup != nil || !errors.Is(cancelErr, kernel.ErrChildExecutionBusy) {
					t.Fatalf("unknown failure lost fence: err=%v grant=%v cancel=%v", err, lookup, cancelErr)
				}
				if tc.fault != "mark" && tc.fault != "mark-ack" && tc.fault != "finish" && (store.marks != 0 || store.finishes != 0) {
					t.Fatal("uncertain callback minted settlement")
				}
				if tc.fault == "mark" || tc.fault == "mark-ack" || tc.fault == "finish" {
					if !errors.Is(err, runtimecontext.ErrBudgetExceeded) || !errors.Is(err, errChildFailureFixture) {
						t.Fatalf("settlement error lost original cause: %v", err)
					}
					store.fault = ""
					recovery := runner.ReconcileChildPhysicalGrant(t.Context(), request.ID)
					if tc.fault == "mark" {
						if !errors.Is(recovery, kernel.ErrChildExecutionBusy) {
							t.Fatalf("missing marker invented on recovery: %v", recovery)
						}
					} else if recovery != nil {
						t.Fatalf("committed marker not recoverable: %v", recovery)
					}
				}
			}
		})
	}
}
