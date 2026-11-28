package conformance

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	runtimecontext "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/context"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

type nativeTransitionCase struct {
	name, event    string
	after, blocked bool
	models, tools  int
}

type nativeTransitionModel struct {
	mode     string
	requests []model.Request
}

func (m *nativeTransitionModel) Capabilities(context.Context, string) (model.Capabilities, error) {
	return model.Capabilities{StructuredJSONSchema: true}, nil
}

func (m *nativeTransitionModel) Generate(_ context.Context, r model.Request) (model.Response, error) {
	m.requests = append(m.requests, model.CloneRequest(r))
	n := len(m.requests)
	if n == 1 {
		switch m.mode {
		case "continue":
			return model.Response{Content: "original continuation", Continue: true}, nil
		case "correction":
			return model.Response{Content: "premature completion"}, nil
		case "structured":
			return model.Response{Content: `{"wrong":"original"}`, Usage: &model.Usage{InputTokens: 2, OutputTokens: 1}}, nil
		default:
			calls := []tools.Call{{ID: "original-tool-1", ToolKey: "fixture.lookup", Arguments: json.RawMessage(`{}`)}}
			if m.mode == "batch" || m.mode == "batch_final" {
				calls = append(calls, tools.Call{ID: "original-tool-2", ToolKey: "fixture.lookup", Arguments: json.RawMessage(`{}`)})
			}
			return model.Response{ToolCalls: calls}, nil
		}
	}
	if m.mode == "correction" && n == 2 {
		return model.Response{ToolCalls: []tools.Call{{ID: "required-tool", ToolKey: "fixture.lookup", Arguments: json.RawMessage(`{}`)}}}, nil
	}
	if m.mode == "structured" {
		return model.Response{Content: `{"answer":"complete"}`, Usage: &model.Usage{InputTokens: 2, OutputTokens: 1}}, nil
	}
	return model.Response{Content: "completed"}, nil
}

type transitionClock struct{}

func (transitionClock) Now() time.Time { return time.Now().UTC() }

// NativeTransitionCommit exercises the same reducers and original Store CAS
// on each backend. Before/after cuts distinguish uncommitted work from the
// exact committed next qualification. No new proof may cover a partial batch
// or an unresolved physical Tool dispatch.
func NativeTransitionCommit(t *testing.T, factory func(*testing.T) (kernel.Store, harness.Store, func() (kernel.Store, harness.Store))) {
	for _, c := range []nativeTransitionCase{
		{"continue/before", "agent.model_continued", false, false, 2, 0},
		{"continue/after", "agent.model_continued", true, false, 2, 0},
		{"tool/before", "tool.completed", false, true, 1, 1},
		{"tool/after", "tool.completed", true, false, 2, 1},
		{"batch/after", "tool.completed", true, true, 1, 1},
		{"batch_final/after", "tool.completed", true, false, 2, 2},
		{"correction/before", "agent.completion_corrected", false, false, 3, 1},
		{"correction/after", "agent.completion_corrected", true, false, 3, 1},
		{"structured/before", "agent.completion_corrected", false, false, 2, 0},
		{"structured/after", "agent.completion_corrected", true, false, 2, 0},
		{"tool_correction/before", "tool.correction_requested", false, true, 1, 1},
		{"tool_correction/after", "tool.correction_requested", true, false, 2, 1},
		{"allowance/before", "agent.allowance_auto_granted", false, false, 2, 1},
		{"allowance/after", "agent.allowance_auto_granted", true, false, 2, 1},
	} {
		t.Run(c.name, func(t *testing.T) { exerciseNativeTransitionCommit(t, c, factory) })
	}
}

func exerciseNativeTransitionCommit(t *testing.T, c nativeTransitionCase, factory func(*testing.T) (kernel.Store, harness.Store, func() (kernel.Store, harness.Store))) {
	t.Helper()
	ctx := t.Context()
	base, host, reopen := factory(t)
	h := host.(preparationHostPorts)
	cut := &preparationCut{preparationKernelPorts: base.(preparationKernelPorts), event: c.event, remaining: 1, after: c.after}
	mode, _, _ := strings.Cut(c.name, "/")
	if mode == "batch_final" {
		cut.remaining = 2
	}
	provider := &nativeTransitionModel{mode: mode}
	inputs, err := agent.NewMemoryRunInputs("native-transition-input")
	if err != nil {
		t.Fatal(err)
	}
	effects := 0
	execution, leaseDuration := ManagedFixtureExecutionPolicy()
	config := harness.ConfigSnapshot{AgentSpec: harness.AgentSpec{Model: "fixture", MaxModelOutputTokens: 128, Limits: agent.Limits{MaxLLMCalls: 4, MaxToolCalls: 4}},
		SharedBudget: &budget.Limits{MaxLLMCalls: 4, MaxToolCalls: 4}}
	if mode == "structured" {
		config.OutputContract = &model.OutputContract{Name: "answer", Schema: json.RawMessage(`{"type":"object","properties":{"answer":{"type":"string"}},"required":["answer"],"additionalProperties":false}`)}
	}
	var registrations []tools.Registration
	if mode != "continue" && mode != "structured" {
		config.ToolKeys = []string{"fixture.lookup"}
		config.ToolPolicies = []harness.ToolPolicySnapshot{{Key: "fixture.lookup", DefinitionVersion: "fixture-v1", ApprovalMode: "never", SideEffectLevel: "read"}}
		registrations = []tools.Registration{{Definition: tools.Definition{Key: "fixture.lookup", Name: "fixture lookup", InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false}`)},
			Handler: tools.HandlerFunc(func(_ context.Context, r tools.ExecutionRequest) (tools.ExecutionResult, error) {
				effects++
				if mode == "tool_correction" {
					return tools.RecoverableNoEffectResult(r.Call.ID, tools.NewRecoverableCallErrorAllowCompletion("fixture.no_match", "No original source found", tools.ErrInvalidCall))
				}
				return tools.ExecutionResult{Content: json.RawMessage(`{"original":"evidence"}`), Receipt: tools.Receipt{ExecutionID: r.Call.ID, Disposition: "read"}}, nil
			})}}
	}
	req := harness.StartRequest{HostThread: harness.HostRef{Kind: "conversation", ID: "native-transition"}, HostTurn: harness.HostRef{Kind: "turn", ID: "original"}, Actor: kernel.ActorRef{TenantID: "tenant", ActorID: "operator"}, Thread: kernel.ThreadRef{Kind: "conversation", ID: "native-transition"}, Goal: "original task", Config: config, Context: &harness.ContextSeed{SourcePath: []string{"original/user"}, Entries: []runtimecontext.Entry{{ID: "entry-original", SourceID: "original/user", TurnID: "original", Required: true, Message: model.Message{Role: model.RoleUser, Content: model.TextParts("original task")}}}}}
	if mode == "correction" {
		req.RequiredToolKeys = []string{"fixture.lookup"}
	}
	if mode == "allowance" {
		req.CallAllowance = &agent.CallAllowance{LLMCalls: 1}
		req.CallAllowance.Renewal = &agent.AutoAllowancePolicy{ModelStep: 1, MaxModelRenewals: 2, MaxNoProgressWindow: 2}
	}
	compose := func(k kernel.Store, h preparationHostPorts, owner string) (*harness.Runner, *kernel.Runtime) {
		t.Helper()
		rt, e := kernel.New(kernel.Dependencies{Store: k, ManagedRecoveryHost: h})
		if e != nil {
			t.Fatal(e)
		}
		manager := runtimecontext.NewManager(runtimecontext.Dependencies{})
		bounded, e := harness.NewBoundedContextAdapter(h, manager, func(context.Context, model.Request) (runtimecontext.ModelWindow, error) {
			return runtimecontext.ModelWindow{ContextTokens: 8192, ReservedOutputTokens: 128}, nil
		}, nil)
		if e != nil {
			t.Fatal(e)
		}
		authority, e := harness.NewBudgetAuthority(harness.BudgetAuthorityDependencies{Store: h, Runtime: rt, Clock: transitionClock{}})
		if e != nil {
			t.Fatal(e)
		}
		b, e := authority.Adapter()
		if e != nil {
			t.Fatal(e)
		}
		registry, e := tools.NewRegistry(registrations)
		if e != nil {
			t.Fatal(e)
		}
		frozen, e := harness.NewFrozenApprovalPolicy(h)
		if e != nil {
			t.Fatal(e)
		}
		approval, e := agent.NewBoundedToolApproval(func(ctx context.Context, r tools.ExecutionRequest) (plugin.ApprovalRequirement, error) {
			return frozen.Approval(ctx, plugin.ToolInvocation{Run: kernel.Run{ID: r.RunID}, Definition: tools.Definition{Key: r.Call.ToolKey}})
		})
		if e != nil {
			t.Fatal(e)
		}
		native, e := agent.NewRunner(agent.Dependencies{Runtime: rt, Model: provider, Catalog: registry, Executor: registry, BoundedContext: bounded, BoundedBudget: b, ModelOutcomeVerifier: authority, Inputs: inputs, ApprovalPolicies: []plugin.ApprovalPolicy{approval},
			Execution:    execution,
			RunExecution: &agent.RunExecution{OwnerID: owner, LeaseDuration: leaseDuration}})
		if e != nil {
			t.Fatal(e)
		}
		r, e := harness.NewRunner(harness.Dependencies{Runtime: rt, Agent: native, Store: h, Context: manager, Catalog: registry, Budget: authority})
		if e != nil {
			t.Fatal(e)
		}
		return r, rt
	}
	r, rt := compose(cut, h, "original")
	func() {
		defer func() {
			if _, ok := recover().(preparationInterrupted); !ok {
				t.Fatal("native transition cut missed")
			}
		}()
		got, startErr := r.StartAgentTurn(ctx, req)
		t.Logf("unexpected original return: %s %v", got.Turn.Status, startErr)
	}()
	sessionID, e := harness.SessionID(req.HostThread)
	if e != nil {
		t.Fatal(e)
	}
	turnID, e := harness.TurnID(sessionID, req.HostTurn)
	if e != nil {
		t.Fatal(e)
	}
	pending, e := r.Load(ctx, turnID)
	if e != nil {
		t.Fatal(e)
	}
	root, ok := harness.TopLevelInvocation(pending)
	if !ok {
		t.Fatal("missing root")
	}
	original, e := base.Load(ctx, root.ExecutionRefID)
	if e != nil {
		t.Fatal(e)
	}
	view, e := agent.ViewState(original)
	if e != nil || len(view.ModelInvocations) != 1 {
		t.Fatalf("original model source: %v", e)
	}
	transfer, e := rt.LoadManagedRecovery(ctx, root.ExecutionRefID)
	if e != nil {
		t.Fatal(e)
	}
	ledger, e := base.(budget.LedgerStore).LoadBudget(ctx, turnID)
	if e != nil {
		t.Fatal(e)
	}
	originalTools := 0
	switch mode {
	case "tool", "batch", "tool_correction", "allowance":
		originalTools = 1
	case "batch_final":
		originalTools = 2
	}
	if len(provider.requests) != 1 || effects != originalTools || ledger.View("").Usage.LLMCalls != 1 || ledger.View("").Usage.ToolCalls != originalTools || ledger.View("").Reserved != (budget.Usage{}) {
		t.Fatalf("original effects: calls=%d effects=%d ledger=%+v", len(provider.requests), effects, ledger.View(""))
	}
	if c.after && !c.blocked {
		p, ok := transfer.SafePoint.AgentObligations()
		if !ok || p.Phase != "prepare" || p.InvocationID != "" || p.PreparationContext == nil || p.PreparationTransitionDigest == "" || !transfer.Valid() || !kernel.ManagedSourceMatches(transfer.SafePoint, original) || view.ModelInvocations[0].Status != agent.ModelInvocationConsumed {
			t.Fatalf("transition committed without exact next preparation: %+v source=%s", p, kernel.ManagedDigest(transfer.SafePoint.Source))
		}
	}
	if c.blocked && kernel.ManagedSourceMatches(transfer.SafePoint, original) {
		t.Fatalf("unresolved Tool gained a point: %s", kernel.ManagedDigest(transfer.SafePoint.Source))
	}
	base, host = reopen()
	h = host.(preparationHostPorts)
	r, rt = compose(base, h, "successor")
	AwaitManagedOriginalLeaseExpiry(t, base, original, transfer, 2*leaseDuration+5*time.Second)
	var done harness.Snapshot
	for n := 0; n < 5; n++ {
		done, e = r.ResumeTurn(ctx, turnID)
		if e != nil || done.Turn.Status == harness.TurnCompleted {
			break
		}
	}
	if c.blocked {
		after, ae := base.Load(ctx, root.ExecutionRefID)
		g, ge := rt.LookupChildExecution(ctx, root.ExecutionRefID)
		if e == nil || (!errors.Is(e, agent.ErrRootOutcomeUnknown) && !errors.Is(e, harness.ErrExecutionOutcomeUnknown)) || ae != nil || ge != nil || g != transfer.SafePoint.OriginalGrant ||
			kernel.ManagedDigest(original) != kernel.ManagedDigest(after) || len(provider.requests) != 1 || effects != originalTools {
			t.Fatalf("unresolved original effect or batch advanced: %v", e)
		}
		return
	}
	if e != nil || done.Turn.Status != harness.TurnCompleted || len(provider.requests) != c.models || effects != c.tools {
		t.Fatalf("resume: %v %s calls=%d effects=%d", e, done.Turn.Status, len(provider.requests), effects)
	}
	final, e := base.Load(ctx, root.ExecutionRefID)
	if e != nil {
		t.Fatal(e)
	}
	end, e := agent.ViewState(final)
	if e != nil {
		t.Fatal(e)
	}
	ledger, e = base.(budget.LedgerStore).LoadBudget(ctx, turnID)
	if e != nil {
		t.Fatal(e)
	}
	if len(end.ModelInvocations) != c.models || end.ModelInvocations[0].ID != view.ModelInvocations[0].ID || ledger.View("").Usage.LLMCalls != c.models || ledger.View("").Usage.ToolCalls != c.tools || ledger.View("").Reserved != (budget.Usage{}) || !ledger.FinishedRuns[root.ExecutionRefID] {
		t.Fatal("original invocation, accounting or lifetime lost")
	}
	if mode == "continue" && strings.Count(preparationMessages(provider.requests[1]), "original continuation") != 1 {
		t.Fatal("continuation content lost or duplicated")
	}
	if mode == "tool" || mode == "allowance" || mode == "batch_final" {
		if strings.Count(preparationMessages(provider.requests[1]), `{"original":"evidence"}`) != originalTools {
			t.Fatal("original Tool result lost or duplicated")
		}
	}
	if mode == "allowance" && (end.CallAllowance == nil || end.CallAllowance.LLMCalls != 2 || end.AutoAllowance == nil || end.AutoAllowance.ModelRenewals != 1) {
		t.Fatalf("allowance renewed twice or lost: %+v %+v", end.CallAllowance, end.AutoAllowance)
	}
	if _, e = rt.LookupChildExecution(ctx, root.ExecutionRefID); !errors.Is(e, kernel.ErrNotFound) {
		t.Fatalf("grant not retired: %v", e)
	}
}
