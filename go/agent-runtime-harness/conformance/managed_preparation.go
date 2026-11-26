package conformance

import (
	"context"
	"errors"
	"runtime"
	"strings"
	"testing"
	"time"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	runtimecontext "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/context"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

type preparationKernelPorts interface {
	noEntryKernelPorts
	kernel.ManagedRecoveryStore
}
type preparationHostPorts interface {
	harness.Store
	kernel.ManagedRecoveryHostStore
	budget.LedgerStore
}

type preparationInterrupted struct{}
type preparationCut struct {
	preparationKernelPorts
	event     string
	remaining int
	after     bool
}

func (s *preparationCut) ApplyExecution(ctx context.Context, l kernel.ExecutionLease, rev uint64, m kernel.StoreMutation) (kernel.Snapshot, error) {
	hit := false
	for _, e := range m.Events {
		if e.Type == s.event {
			s.remaining--
			hit = s.remaining == 0
		}
	}
	if hit && !s.after {
		panic(preparationInterrupted{})
	}
	next, err := s.preparationKernelPorts.ApplyExecution(ctx, l, rev, m)
	if err == nil && hit {
		panic(preparationInterrupted{})
	}
	return next, err
}

type preparationSummaryModel struct {
	store     kernel.Store
	inputs    *agent.MemoryRunInputs
	requests  []model.Request
	summaries int
}

func (*preparationSummaryModel) Capabilities(context.Context, string) (model.Capabilities, error) {
	return model.Capabilities{StructuredJSONSchema: true}, nil
}
func (m *preparationSummaryModel) Generate(ctx context.Context, r model.Request) (model.Response, error) {
	m.requests = append(m.requests, model.CloneRequest(r))
	out := model.Response{Content: "answer", Usage: &model.Usage{InputTokens: 10, OutputTokens: 5}}
	if r.OutputContract != nil && r.OutputContract.Name == "working_state_summary" {
		m.summaries++
		run, err := m.store.Load(ctx, r.RunID)
		if err != nil {
			return out, err
		}
		if _, err = m.inputs.Submit(ctx, run.Run, "steer-once", "late summary steering"); err != nil {
			return out, err
		}
		out.Content = `{"goal":"retain verified evidence","constraints":["never invent facts"],"completed":[],"pending":["verify original evidence"],"decisions":[]}`
	}
	return out, nil
}
func preparationMessages(r model.Request) string {
	var b strings.Builder
	for _, m := range r.Messages {
		b.WriteString(model.TextValue(m.Content))
		b.WriteByte('\n')
	}
	return b.String()
}

// ManagedPreparationSummary exercises the exact same persisted preparation
// obligation across independently reopened native Kernel and Host Stores.
func ManagedPreparationSummary(t *testing.T, factory func(*testing.T) (kernel.Store, harness.Store, func() (kernel.Store, harness.Store))) {
	for _, boundary := range []string{"input", "business"} {
		for _, after := range []bool{false, true} {
			name := boundary + "/before"
			if after {
				name = boundary + "/after"
			}
			t.Run(name, func(t *testing.T) {
				ctx := t.Context()
				base, host, reopen := factory(t)
				ports, ok := base.(preparationKernelPorts)
				if !ok {
					t.Fatal("missing native Store ports")
				}
				h, ok := host.(preparationHostPorts)
				if !ok {
					t.Fatal("missing original Host ports")
				}
				inputs, err := agent.NewMemoryRunInputs("summary-frontier")
				if err != nil {
					t.Fatal(err)
				}
				provider := &preparationSummaryModel{store: base, inputs: inputs}
				rt, err := kernel.New(kernel.Dependencies{Store: base})
				if err != nil {
					t.Fatal(err)
				}
				leaseDuration := 3 * time.Second
				if runtime.GOOS == "windows" {
					leaseDuration = 8 * time.Second
				}
				firstText := "first prompt " + strings.Repeat("ORIGINAL_EVIDENCE_", 700)
				secondText := "second prompt " + strings.Repeat("CURRENT_EVIDENCE_", 700)
				options := harness.AgentSessionOptions{Runtime: rt, Store: h, Model: provider,
					Session: harness.SessionOptions{HostThread: harness.HostRef{Kind: "conversation", ID: "preparation-summary"}, Actor: kernel.ActorRef{TenantID: "tenant", ActorID: "operator"}, Thread: kernel.ThreadRef{Kind: "conversation", ID: "preparation-summary"},
						Config: harness.ConfigSnapshot{AgentSpec: harness.AgentSpec{Model: "fixture", Instructions: "answer accurately", MaxModelOutputTokens: 128}, ContextPolicy: runtimecontext.Policy{SoftLimitPercent: 10, MaxSerializedBytes: 100000, PreserveRecentTurns: 1, MinimumRecentTurns: 1, MaxCompactionTokens: 128}},
						Transcript: harness.TranscriptSourceFunc(func(_ context.Context, _ harness.HostRef, turn harness.HostRef, _ string) (*harness.ContextSeed, error) {
							entry := func(id, tid string, role model.Role, text string) runtimecontext.Entry {
								return runtimecontext.Entry{ID: "entry-" + id, SourceID: id, TurnID: tid, Message: model.Message{Role: role, Content: model.TextParts(text)}}
							}
							entries := []runtimecontext.Entry{entry("first/user", "first", model.RoleUser, firstText)}
							if turn.ID == "second" {
								entries = append(entries, entry("first/assistant", "first", model.RoleAssistant, "answer"), entry("second/user", "second", model.RoleUser, secondText))
							}
							path := make([]string, len(entries))
							for i, e := range entries {
								path[i] = e.SourceID
							}
							return &harness.ContextSeed{SourcePath: path, Entries: entries}, nil
						})},
					Context: harness.AgentSessionContextOptions{ReadArtifacts: true, ResolveWindow: func(context.Context, model.Request) (runtimecontext.ModelWindow, error) {
						return runtimecontext.ModelWindow{ContextTokens: 65536, ReservedOutputTokens: 128}, nil
					}},
					Execution:    agent.ExecutionPolicy{Model: agent.CallPolicy{Timeout: time.Second, MaxAttempts: 1, MaxConcurrency: 1}, Tool: agent.CallPolicy{Timeout: time.Second, MaxAttempts: 1, MaxConcurrency: 1}},
					RunExecution: &agent.RunExecution{OwnerID: "preparation-original", LeaseDuration: leaseDuration}}
				session, err := harness.OpenAgentSession(options)
				if err != nil {
					t.Fatal(err)
				}
				if done, e := session.Prompt(ctx, "first", firstText); e != nil || done.Turn.Status != harness.TurnCompleted {
					t.Fatalf("warmup: %v %s", e, done.Turn.Status)
				}
				cut := &preparationCut{preparationKernelPorts: ports, event: "agent.input_applied", remaining: 1, after: after}
				if boundary == "business" {
					cut.event = "agent.model_invocation.pending"
					cut.remaining = 2
				}
				options.Runtime, err = kernel.New(kernel.Dependencies{Store: cut, ManagedRecoveryHost: h})
				if err != nil {
					t.Fatal(err)
				}
				options.Context.SummaryNativeRecovery = true
				options.Session.Config.WorkingStateSummary = &agent.WorkingStateSummaryPolicy{MaxInputTokens: 60000, MaxSerializedBytes: 100000}
				options.Session.Config.SharedBudget = &budget.Limits{MaxLLMCalls: 2}
				options.Inputs = inputs
				session, err = harness.OpenAgentSession(options)
				if err != nil {
					t.Fatal(err)
				}
				func() {
					defer func() {
						if _, ok := recover().(preparationInterrupted); !ok {
							t.Fatal("missing cut")
						}
					}()
					_, _ = session.Prompt(ctx, "second", secondText)
				}()
				pending, err := session.Load(ctx, "second")
				if err != nil {
					t.Fatal(err)
				}
				root, ok := harness.TopLevelInvocation(pending)
				if !ok {
					t.Fatal("missing root")
				}
				original, err := base.Load(ctx, root.ExecutionRefID)
				if err != nil {
					t.Fatal(err)
				}
				view, err := agent.ViewState(original)
				if err != nil {
					t.Fatal(err)
				}
				wantInv, wantCursor := 1, uint64(1)
				if boundary == "input" && !after {
					wantCursor = 0
				}
				if boundary == "business" && after {
					wantInv = 2
				}
				if len(view.ModelInvocations) != wantInv || view.InputCursor != wantCursor || view.ModelInvocations[0].Status != agent.ModelInvocationConsumed || provider.summaries != 1 || len(provider.requests) != 2 {
					t.Fatalf("bad original frontier: %+v calls=%d", view, len(provider.requests))
				}
				point, err := options.Runtime.LoadManagedRecovery(ctx, root.ExecutionRefID)
				proof, ok := point.SafePoint.AgentObligations()
				if err != nil || !ok || point.State != kernel.ManagedAvailable || !kernel.ManagedSourceMatches(point.SafePoint, original) ||
					(wantInv == 1 && (proof.Phase != "prepare" || proof.InvocationID != "" || proof.PreparationContext == nil)) {
					t.Fatalf("missing preparation obligation: %v %+v", err, proof)
				}
				base, host = reopen()
				h, ok = host.(preparationHostPorts)
				if !ok {
					t.Fatal("missing reopened Host ports")
				}
				provider.store = base
				options.Runtime, err = kernel.New(kernel.Dependencies{Store: base, ManagedRecoveryHost: h})
				if err != nil {
					t.Fatal(err)
				}
				options.Store = h
				options.Session.Transcript = nil
				options.RunExecution = &agent.RunExecution{OwnerID: "preparation-successor", LeaseDuration: leaseDuration}
				session, err = harness.OpenAgentSession(options)
				if err != nil {
					t.Fatal(err)
				}
				if delay := time.Until(point.SafePoint.OriginalLease.ExpiresAt) + 100*time.Millisecond; delay > 0 {
					time.Sleep(delay)
				}
				done, err := session.Resume(ctx, "second")
				if err != nil || done.Turn.Status != harness.TurnCompleted || provider.summaries != 1 || len(provider.requests) != 3 {
					t.Fatalf("resume: %v %s calls=%d summary=%d", err, done.Turn.Status, len(provider.requests), provider.summaries)
				}
				final, err := base.Load(ctx, root.ExecutionRefID)
				if err != nil {
					t.Fatal(err)
				}
				end, err := agent.ViewState(final)
				if err != nil {
					t.Fatal(err)
				}
				ledger, err := h.LoadBudget(ctx, done.Turn.ID)
				if err != nil {
					t.Fatal(err)
				}
				if end.InputCursor != 1 || len(end.ModelInvocations) != 2 || end.ModelInvocations[0].ID != view.ModelInvocations[0].ID || (wantInv == 2 && end.ModelInvocations[1].ID != view.ModelInvocations[1].ID) ||
					strings.Contains(preparationMessages(provider.requests[1]), "late summary steering") || strings.Count(preparationMessages(provider.requests[2]), "late summary steering") != 1 ||
					ledger.View("").Usage.LLMCalls != 2 || !ledger.FinishedRuns[root.ExecutionRefID] {
					t.Fatal("original summary, input, intent or ledger lost")
				}
				if _, err = options.Runtime.LookupChildExecution(ctx, root.ExecutionRefID); !errors.Is(err, kernel.ErrNotFound) {
					t.Fatalf("grant not retired: %v", err)
				}
			})
		}
	}
}
