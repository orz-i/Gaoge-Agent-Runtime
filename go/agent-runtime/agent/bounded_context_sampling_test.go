package agent

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	runtimecontext "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/context"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/internal/callbackscope"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
)

type samplingTestPorts struct {
	fail     string
	calls    int
	hard     bool
	artifact bool
	plan     *SamplingPlan
	commits  []SamplingCursor
	binds    []SamplingCursor
}

func (p *samplingTestPorts) Hydrate(ctx context.Context, i plugin.RunInvocation) (context.Context, BoundedContextHydration, error) {
	return ctx, BoundedContextHydration{RunID: i.RunID, SourceBinding: json.RawMessage(`{"original":true}`)}, nil
}
func (p *samplingTestPorts) PlanSampling(_ context.Context, r model.Request, s kernel.Snapshot) (SamplingPlan, error) {
	p.calls++
	if p.fail == "plan" {
		return SamplingPlan{}, runtimecontext.ErrBudgetExceeded
	}
	plan := SamplingPlan{Source: s, Request: r}
	if p.plan != nil {
		plan = cloneSamplingPlan(*p.plan)
		plan.Source = s
		plan.Request = r
	}
	if p.fail == "source" {
		plan.Source.Run.Revision++
	}
	return plan, nil
}
func (p *samplingTestPorts) EvaluateSampling(_ context.Context, _ SamplingPlan, r model.Request, _ runtimecontext.Window, native bool) (SamplingEvaluation, error) {
	p.calls++
	if p.fail == "evaluate" {
		return SamplingEvaluation{}, runtimecontext.ErrBudgetExceeded
	}
	assessment := runtimecontext.Assessment{HardInputTokens: 100, SoftInputTokens: 80, AdjustedTokenEstimate: 10, HardTokenEstimate: 10, SerializedBytes: 10}
	if p.hard {
		assessment.AdjustedTokenEstimate = 200
		assessment.HardTokenEstimate = 200
	}
	result := SamplingEvaluation{Request: r, Assessment: assessment}
	if native && p.artifact {
		artifact, err := runtimecontext.NewArtifact(runtimecontext.ArtifactToolResult, "scope", 1, "source", "evidence", nil)
		if err != nil {
			return SamplingEvaluation{}, err
		}
		result.Artifacts = []runtimecontext.Artifact{artifact}
	}
	return result, nil
}
func (p *samplingTestPorts) PutArtifact(_ context.Context, a runtimecontext.Artifact) (runtimecontext.Artifact, error) {
	p.calls++
	if p.fail == "put" {
		return runtimecontext.Artifact{}, runtimecontext.ErrBudgetExceeded
	}
	if p.fail == "put-receipt" {
		a.Content = "foreign"
	}
	return a, nil
}
func (p *samplingTestPorts) CommitSampling(_ context.Context, c SamplingCursor, next runtimecontext.Checkpoint) (SamplingCommitReceipt, error) {
	p.calls++
	p.commits = append(p.commits, cloneSamplingCursor(c))
	if p.fail == "commit" {
		return SamplingCommitReceipt{}, runtimecontext.ErrBudgetExceeded
	}
	turn := c.Turn
	turn.ContextCheckpointID = next.ID
	turn.ContextRef = samplingRef(next)
	turn.Revision++
	turn.UpdatedAt = c.UpdatedAt
	if p.fail == "turn-receipt" {
		turn.ConfigSnapshotID = "foreign"
	}
	return SamplingCommitReceipt{Turn: turn, Checkpoint: next, ExpectedTurnCheckpointID: c.ExpectedTurnCheckpointID, ExpectedHeadCheckpointID: c.ExpectedHeadCheckpointID}, nil
}
func (p *samplingTestPorts) BindSampling(_ context.Context, c SamplingCursor, next runtimecontext.Checkpoint) (SamplingBindingReceipt, error) {
	p.calls++
	p.binds = append(p.binds, cloneSamplingCursor(c))
	if p.fail == "bind" {
		return SamplingBindingReceipt{}, runtimecontext.ErrBudgetExceeded
	}
	receipt := SamplingBindingReceipt{TurnID: c.Turn.ID, Access: "owner", Previous: c.Checkpoint, Current: next}
	if p.fail == "bind-receipt" {
		receipt.Previous.ID = "foreign"
	}
	return receipt, nil
}

type samplingOpaqueTail struct{}

func (samplingOpaqueTail) Name() string { return "opaque" }
func (samplingOpaqueTail) Run(ctx context.Context, i plugin.RunInvocation, next plugin.RunNext) (kernel.Snapshot, error) {
	return next(ctx)
}

func TestSamplingPreparationActualCallRejectVersusHostError(t *testing.T) {
	for _, tc := range []struct {
		name, fail                                       string
		hard, artifact, opaque, oldUnknown, wantComplete bool
	}{
		{name: "prepared", wantComplete: true},
		{name: "finite-hard-rejection", hard: true, wantComplete: true},
		{name: "port-plan-budget", fail: "plan", hard: true},
		{name: "port-evaluate-budget", fail: "evaluate", hard: true},
		{name: "port-put-budget", fail: "put", hard: true, artifact: true},
		{name: "wrong-artifact-receipt", fail: "put-receipt", hard: true, artifact: true},
		{name: "foreign-source", fail: "source", hard: true},
		{name: "opaque-returned-error", hard: true, opaque: true},
		{name: "existing-unknown", hard: true, oldUnknown: true, wantComplete: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ports := &samplingTestPorts{fail: tc.fail, hard: tc.hard, artifact: tc.artifact}
			adapter, err := NewSamplingBoundedContextAdapter(runtimecontext.NewManager(runtimecontext.Dependencies{}), ports)
			if err != nil {
				t.Fatal(err)
			}
			layers := []plugin.RunMiddleware{adapter}
			if tc.opaque {
				layers = append(layers, samplingOpaqueTail{})
			}
			chain, err := plugin.NewRunChain(layers...)
			if err != nil {
				t.Fatal(err)
			}
			lifecycle := callbackscope.New(callbackscope.Binding{RunID: "run", GrantID: "original"}, []any{adapter})
			runner := &Runner{contextPreparation: adapter, rootCallbacks: &rootCallbackState{}}
			runner.rootCallbacks.unknown.Store(tc.oldUnknown)
			source := kernel.Snapshot{Run: kernel.Run{ID: "run", Revision: 1}}
			_, err = chain.Invoke(callbackscope.Bind(t.Context(), lifecycle), plugin.RunInvocation{RunID: "run", Operation: plugin.RunStart}, func(ctx context.Context) (kernel.Snapshot, error) {
				request := model.CloneRequest(model.Request{RunID: "run", Model: "model", Messages: []model.Message{{Role: model.RoleUser, Content: model.TextParts("hello")}}})
				prepared, e := runner.prepareSelectedContext(ctx, source, request)
				if e == nil && !reflect.DeepEqual(prepared, request) {
					t.Fatal("lost original prepared request")
				}
				if lifecycle.Complete() {
					t.Fatal("preparation receipt completed live Run")
				}
				if _, ok := callbackscope.ManagedSuspension(ctx, adapter, prepared); ok {
					t.Fatal("sampling minted managed suspension")
				}
				return source, e
			})
			if tc.hard && tc.fail == "" && !errors.Is(err, runtimecontext.ErrBudgetExceeded) {
				t.Fatalf("lost actual hard rejection: %v", err)
			}
			if lifecycle.Complete() != tc.wantComplete {
				t.Fatalf("complete=%v want=%v err=%v", lifecycle.Complete(), tc.wantComplete, err)
			}
			wantUnknown := tc.oldUnknown || tc.fail != ""
			if runner.rootCallbacks.unknown.Load() != wantUnknown {
				t.Fatalf("unknown=%v want=%v", runner.rootCallbacks.unknown.Load(), wantUnknown)
			}
		})
	}
}
func TestSamplingPreparationRequiresActualSelectedRun(t *testing.T) {
	for _, mode := range []string{"missing", "foreign", "closed", "unhydrated", "returned"} {
		t.Run(mode, func(t *testing.T) {
			ports := &samplingTestPorts{}
			adapter, err := NewSamplingBoundedContextAdapter(runtimecontext.NewManager(runtimecontext.Dependencies{}), ports)
			if err != nil {
				t.Fatal(err)
			}
			runner := &Runner{contextPreparation: adapter, rootCallbacks: &rootCallbackState{}}
			ctx := t.Context()
			if mode != "missing" {
				runID := "run"
				if mode == "foreign" {
					runID = "foreign"
				}
				lifecycle := callbackscope.New(callbackscope.Binding{RunID: runID}, []any{adapter})
				ctx = callbackscope.Bind(ctx, lifecycle)
				var ticket *callbackscope.Ticket
				ctx, ticket = callbackscope.Enter(ctx, runID, adapter, "original")
				if mode != "unhydrated" {
					callbackscope.Operation(ctx, adapter, "managed.hydrated", "original")
				}
				if mode == "closed" {
					lifecycle.Close()
				}
				if mode == "returned" {
					callbackscope.Returned(ticket, nil)
				}
			}
			_, err = runner.prepareSelectedContext(ctx, kernel.Snapshot{Run: kernel.Run{ID: "run"}}, model.Request{RunID: "run"})
			if !errors.Is(err, ErrInvalidRequest) || ports.calls != 0 || !runner.rootCallbacks.unknown.Load() {
				t.Fatalf("unproved call entered ports: %v calls=%d", err, ports.calls)
			}
		})
	}
}

func TestSamplingFrozenCheckpointReceipts(t *testing.T) {
	for _, failure := range []string{"", "commit", "turn-receipt", "bind", "bind-receipt"} {
		t.Run(failure, func(t *testing.T) {
			manager := runtimecontext.NewManager(runtimecontext.Dependencies{})
			cp, err := manager.Open(t.Context(), runtimecontext.OpenRequest{ScopeID: "session", StaticFingerprint: "static", SourcePath: []string{"m1"}, Entries: []runtimecontext.Entry{
				{ID: "entry", TurnID: "turn", SourceID: "m1", Message: model.Message{Role: model.RoleUser, Content: model.TextParts("hello")}},
			}})
			if err != nil {
				t.Fatal(err)
			}
			stamp := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
			turn := SamplingTurn{ID: "turn", SessionID: "session", ConfigSnapshotID: "config", ContextCheckpointID: cp.ID, ContextRef: samplingRef(cp), Status: "running", Revision: 17, CreatedAt: stamp, UpdatedAt: stamp}
			plan := SamplingPlan{Checkpoint: &cp, Owner: true, WindowFingerprint: "new-window", Cursor: SamplingCursor{Turn: turn, Checkpoint: cp, ExpectedTurnCheckpointID: cp.ID, ExpectedHeadCheckpointID: "different-head-expectation", UpdatedAt: stamp.Add(time.Second)}}
			baseline := cloneSamplingPlan(plan)
			ports := &samplingTestPorts{fail: failure, plan: &plan}
			adapter, err := NewSamplingBoundedContextAdapter(manager, ports)
			if err != nil {
				t.Fatal(err)
			}
			runner := &Runner{contextPreparation: adapter, rootCallbacks: &rootCallbackState{}}
			lifecycle := callbackscope.New(callbackscope.Binding{RunID: "run"}, []any{adapter})
			chain, err := plugin.NewRunChain(adapter)
			if err != nil {
				t.Fatal(err)
			}
			request := model.Request{RunID: "run", Model: "model", Messages: runtimecontext.Materialize(cp.Window)}
			_, err = chain.Invoke(callbackscope.Bind(t.Context(), lifecycle), plugin.RunInvocation{RunID: "run"}, func(ctx context.Context) (kernel.Snapshot, error) {
				_, e := runner.prepareSelectedContext(ctx, kernel.Snapshot{Run: kernel.Run{ID: "run", Revision: 4}}, request)
				return kernel.Snapshot{}, e
			})
			if failure == "" {
				if err != nil || !lifecycle.Complete() || runner.rootCallbacks.unknown.Load() {
					t.Fatalf("known sequence failed: %v", err)
				}
			} else {
				if err == nil || lifecycle.Complete() || !runner.rootCallbacks.unknown.Load() {
					t.Fatalf("unproved checkpoint completed: %v", err)
				}
			}
			if len(ports.commits) == 0 || !sameBoundedValue(ports.commits[0], baseline.Cursor) {
				t.Fatal("commit did not retain independent frozen expected tuple")
			}
			if !sameBoundedValue(plan, baseline) {
				t.Fatal("ports mutated original plan through alias")
			}
			if failure == "commit" || failure == "turn-receipt" {
				if len(ports.binds) != 0 {
					t.Fatal("invalid commit reached process bind")
				}
			}
			if len(ports.binds) > 0 && !sameBoundedValue(ports.binds[0], baseline.Cursor) {
				t.Fatal("binding did not use same original cursor")
			}
		})
	}
}
