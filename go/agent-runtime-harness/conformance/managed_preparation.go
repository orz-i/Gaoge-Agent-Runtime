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
	kernelconformance "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/conformance"
	runtimecontext "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/context"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

type preparationKernelPorts interface {
	noEntryKernelPorts
	kernel.ManagedRecoveryStore
	budget.LedgerStore
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
	renewal   time.Duration
	renewed   kernel.ExecutionLease
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
		// Force the real Store renewal after the previous safe point was
		// committed, before the next intent can commit. The original bearer
		// is unchanged; its persisted expiry now exceeds the historical one.
		if s.renewal > 0 {
			renewed, err := s.RenewExecution(ctx, kernel.ExecutionRenewRequest{Lease: l, LeaseDuration: s.renewal})
			if err != nil {
				return kernel.Snapshot{}, err
			}
			s.renewed = renewed
		}
		panic(preparationInterrupted{})
	}
	next, err := s.preparationKernelPorts.ApplyExecution(ctx, l, rev, m)
	if err == nil && hit {
		panic(preparationInterrupted{})
	}
	return next, err
}

// The positive recovery fixtures are not timeout tests. These two
// deterministic delays reproduce slow storage before physical dispatch and
// a successful response that would outlive the old one-second fixture.
const preparationSlowIO = 1250 * time.Millisecond

// ManagedFixtureExecutionPolicy covers real Store reads, admission and
// settlement inside a physical call. These matrices assert the injected
// state/receipt cut, not elapsed-time behavior. Keep one bounded attempt
// and the production one-second lease safety margin; encrypted Windows
// profiles need a larger window for the same operation.
func ManagedFixtureExecutionPolicy() (agent.ExecutionPolicy, time.Duration) {
	timeout, lease := 3*time.Second, 5*time.Second
	if runtime.GOOS == "windows" {
		timeout, lease = 6*time.Second, 9*time.Second
	}
	call := agent.CallPolicy{Timeout: timeout, MaxAttempts: 1, MaxConcurrency: 1}
	return agent.ExecutionPolicy{Model: call, Tool: call}, lease
}

type slowPreparationDispatch struct{ preparationKernelPorts }

func (s slowPreparationDispatch) ApplyExecution(ctx context.Context, l kernel.ExecutionLease, rev uint64, m kernel.StoreMutation) (kernel.Snapshot, error) {
	for _, e := range m.Events {
		if e.Type == "agent.model_invocation.dispatch_intent" {
			timer := time.NewTimer(preparationSlowIO)
			defer timer.Stop()
			select {
			case <-timer.C:
			case <-ctx.Done():
				return kernel.Snapshot{}, ctx.Err()
			}
			break
		}
	}
	return s.preparationKernelPorts.ApplyExecution(ctx, l, rev, m)
}

type preparationSummaryModel struct {
	store         kernel.Store
	inputs        *agent.MemoryRunInputs
	requests      []model.Request
	summaries     int
	businessDelay time.Duration
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
	} else if m.businessDelay > 0 {
		// Deliberately return a successful result even when the old call
		// context expired. Runtime must never replay that physical result.
		time.Sleep(m.businessDelay)
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

// AwaitManagedOriginalLeaseExpiry requires the Store itself to reject the
// original bearer and verifies that neither the Run nor the original transfer
// or Grant changed during the read-only wait. A historical point's expiry
// is not a takeover proof: the incumbent may have renewed the same lease.
func AwaitManagedOriginalLeaseExpiry(t *testing.T, store kernel.Store, source kernel.Snapshot, transfer kernel.ManagedTransfer, maxWait time.Duration) {
	t.Helper()
	ports, ok := store.(interface {
		kernel.ExecutionStore
		kernel.ChildExecutionStore
		kernel.ManagedRecoveryStore
	})
	if !ok || source.Run.Status != kernel.RunStatusRunning || transfer.SafePoint.Host.RunID != source.Run.ID ||
		!transfer.Valid() ||
		transfer.Selection.Mode != "" || transfer.SuccessorGrant.ID != "" {
		t.Fatal("missing original unselected managed frontier")
	}
	unchanged := func() {
		t.Helper()
		ctx := t.Context()
		s, err := store.Load(ctx, source.Run.ID)
		if err != nil || kernel.ManagedDigest(s) != kernel.ManagedDigest(source) {
			t.Fatalf("original source changed during expiry wait: %v", err)
		}
		g, err := ports.LookupChildExecution(ctx, source.Run.ID)
		if err != nil || g != transfer.SafePoint.OriginalGrant {
			t.Fatalf("original Grant changed during expiry wait: %v", err)
		}
		p, err := ports.LoadManagedRecovery(ctx, source.Run.ID)
		if err != nil || kernel.ManagedDigest(p) != kernel.ManagedDigest(transfer) {
			t.Fatalf("original transfer changed during expiry wait: %v", err)
		}
	}
	unchanged()
	kernelconformance.AwaitNativeExecutionExpiry(t, ports, transfer.SafePoint.OriginalLease, maxWait)
	unchanged()
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
				execution, leaseDuration := ManagedFixtureExecutionPolicy()
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
					Execution:    execution,
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
					if !after {
						cut.renewal = leaseDuration + 2*time.Second
					}
				}
				options.Runtime, err = kernel.New(kernel.Dependencies{Store: cut, ManagedRecoveryHost: h})
				if err != nil {
					t.Fatal(err)
				}
				options.Context.ManagedRecovery = true
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
				if err != nil || !ok || !point.Valid() || !kernel.ManagedSourceMatches(point.SafePoint, original) ||
					(wantInv == 1 && (proof.Phase != "prepare" || proof.InvocationID != "" || proof.PreparationContext == nil)) {
					t.Fatalf("missing preparation obligation: %v %+v", err, proof)
				}
				base, host = reopen()
				h, ok = host.(preparationHostPorts)
				if !ok {
					t.Fatal("missing reopened Host ports")
				}
				provider.store = base
				successorStore := base
				if boundary == "input" && after {
					successorStore = slowPreparationDispatch{base.(preparationKernelPorts)}
				}
				if boundary == "business" && !after {
					provider.businessDelay = preparationSlowIO
				}
				options.Runtime, err = kernel.New(kernel.Dependencies{Store: successorStore, ManagedRecoveryHost: h})
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
				if cut.renewal > 0 {
					old := point.SafePoint.OriginalLease
					if !point.SafePoint.OriginalGrant.MatchesExecution(cut.renewed) ||
						!cut.renewed.ExpiresAt.After(old.ExpiresAt.Add(time.Second)) {
						t.Fatalf("renewal fixture did not extend the original bearer: old=%+v renewed=%+v", old, cut.renewed)
					}
					t.Logf("historical expiry=%s renewed expiry=%s", old.ExpiresAt, cut.renewed.ExpiresAt)
				}
				AwaitManagedOriginalLeaseExpiry(t, base, original, point, 2*leaseDuration+5*time.Second)
				done, err := session.Resume(ctx, "second")
				if err != nil || done.Turn.Status != harness.TurnCompleted || provider.summaries != 1 || len(provider.requests) != 3 {
					transfer, e := options.Runtime.LoadManagedRecovery(ctx, root.ExecutionRefID)
					t.Fatalf("resume: %v %s calls=%d summary=%d transfer=%s selection=%s read=%v", err, done.Turn.Status, len(provider.requests), provider.summaries, transfer.Selection.Mode, transfer.Selection.Mode, e)
				}
				final, err := base.Load(ctx, root.ExecutionRefID)
				if err != nil {
					t.Fatal(err)
				}
				end, err := agent.ViewState(final)
				if err != nil {
					t.Fatal(err)
				}
				ledger, err := base.(budget.LedgerStore).LoadBudget(ctx, done.Turn.ID)
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
