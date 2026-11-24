package agent

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	runtimecontext "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/context"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

var errSummaryCoreCrash = errors.New("summary fault injection")

const summaryCoreDocument = `{"goal":"Complete the requested report","constraints":["Preserve the contract"],"completed":["Read the original source"],"pending":["Answer the current request"],"decisions":["Use recorded evidence"]}`

type summaryCoreModel struct {
	mu       sync.Mutex
	requests []model.Request
	response *model.Response
}

func (*summaryCoreModel) ProviderName() string { return "summary-test-provider" }
func (*summaryCoreModel) BindingFingerprintForRequest(model.Request) string {
	return strings.Repeat("a", 64)
}
func (*summaryCoreModel) Capabilities(context.Context, string) (model.Capabilities, error) {
	return model.Capabilities{StructuredJSONSchema: true}, nil
}
func (p *summaryCoreModel) Generate(_ context.Context, request model.Request) (model.Response, error) {
	p.mu.Lock()
	p.requests = append(p.requests, model.CloneRequest(request))
	p.mu.Unlock()
	result := model.Response{Content: "business answer", ResponseID: "response_" + request.InvocationID,
		Usage: &model.Usage{InputTokens: 7, OutputTokens: 3, CacheReadTokens: 1, ReasoningTokens: 2}}
	if request.OutputContract != nil && request.OutputContract.Name == "working_state_summary" {
		result.Content = summaryCoreDocument
		if p.response != nil {
			result = model.CloneResponse(*p.response)
			result.ResponseID = "response_" + request.InvocationID
			if result.Usage == nil {
				result.Usage = &model.Usage{InputTokens: 7, OutputTokens: 3}
			}
		}
	}
	return result, nil
}
func (p *summaryCoreModel) calls() []model.Request {
	p.mu.Lock()
	defer p.mu.Unlock()
	result := make([]model.Request, len(p.requests))
	for index := range p.requests {
		result[index] = model.CloneRequest(p.requests[index])
	}
	return result
}

type summaryCorePorts struct {
	manager      *runtimecontext.Manager
	source       runtimecontext.Checkpoint
	current      runtimecontext.Checkpoint
	bound        runtimecontext.Checkpoint
	turn         SummaryTurn
	root         ContextSummaryRoot
	policy       runtimecontext.Policy
	window       runtimecontext.ModelWindow
	artifacts    map[string]runtimecontext.Artifact
	explicit     bool
	reader       bool
	owner        bool
	commitCut    string
	project      bool
	beforePut    func()
	priorRun     *kernel.Snapshot
	continuation string
}

func (p *summaryCorePorts) Hydrate(ctx context.Context, inv plugin.RunInvocation) (context.Context, BoundedContextHydration, error) {
	p.bound = runtimecontext.CloneCheckpoint(p.current)
	return ctx, BoundedContextHydration{RunID: inv.RunID, Checkpoint: &p.bound, SourceBinding: json.RawMessage(`{"owner":true}`)}, nil
}
func (p *summaryCorePorts) ResolveWindow(ctx context.Context, request model.Request) (runtimecontext.ModelWindow, error) {
	return p.window, nil
}
func (p *summaryCorePorts) Plan(_ context.Context, request model.Request) (BoundedContextPlan, error) {
	request.PreviousResponseID = p.continuation
	runtimeMessages := model.CloneMessages(request.Messages)
	request.Messages = runtimecontext.Materialize(p.bound.Window)
	for index, message := range runtimeMessages {
		if message.Role == model.RoleUser {
			request.Messages = append(request.Messages, runtimeMessages[index+1:]...)
			break
		}
	}
	cp := runtimecontext.CloneCheckpoint(p.bound)
	return BoundedContextPlan{Request: request, Checkpoint: &cp, ModelWindow: p.window, Policy: p.policy,
		Owner: p.owner, ArtifactReader: p.reader, TurnID: p.turn.ID, TurnRevision: p.turn.Revision, SourceBinding: json.RawMessage(`{"owner":true}`)}, nil
}
func (p *summaryCorePorts) PutArtifact(_ context.Context, artifact runtimecontext.Artifact) (runtimecontext.Artifact, error) {
	if p.beforePut != nil {
		p.beforePut()
		p.beforePut = nil
	}
	p.artifacts[artifact.ID] = runtimecontext.CloneArtifacts([]runtimecontext.Artifact{artifact})[0]
	return artifact, nil
}
func (p *summaryCorePorts) CommitCheckpoint(_ context.Context, plan BoundedContextPlan, target runtimecontext.Checkpoint) (runtimecontext.Checkpoint, error) {
	if plan.Checkpoint.ID != p.current.ID || plan.TurnRevision != p.turn.Revision {
		return runtimecontext.Checkpoint{}, kernel.ErrConflict
	}
	p.current = runtimecontext.CloneCheckpoint(target)
	p.turn.ContextCheckpointID, p.turn.ContextRef = target.ID, summaryRef(target)
	p.turn.Revision++
	return target, nil
}
func (p *summaryCorePorts) BindCheckpoint(_ context.Context, previous string, target runtimecontext.Checkpoint) error {
	if p.bound.ID != previous {
		return kernel.ErrConflict
	}
	p.bound = runtimecontext.CloneCheckpoint(target)
	return nil
}
func (p *summaryCorePorts) PlanSummary(_ context.Context, request model.Request, _ kernel.Snapshot) (ContextSummaryPlan, error) {
	request.PreviousResponseID = p.continuation
	request.Messages = runtimecontext.Materialize(p.current.Window)
	return ContextSummaryPlan{Request: request, Cursor: SummaryCursor{Turn: p.turn, Checkpoint: runtimecontext.CloneCheckpoint(p.current),
		ExpectedTurnCheckpointID: p.current.ID, ExpectedHeadCheckpointID: p.current.ID, UpdatedAt: p.turn.UpdatedAt.Add(time.Second)},
		ModelWindow: p.window, Policy: p.policy, Owner: p.owner, ArtifactReader: p.reader, ExplicitWorkingState: p.explicit, Root: p.root, PriorSummaryRun: p.priorRun}, nil
}
func (p *summaryCorePorts) ReadSummary(_ context.Context, binding ContextSummaryBinding) (ContextSummaryRead, error) {
	if p.project {
		p.turn.Status = "running"
		p.turn.Revision++
		p.turn.UpdatedAt = binding.Cursor.UpdatedAt.Add(time.Second)
		p.project = false
	}
	return ContextSummaryRead{SourceCheckpoint: runtimecontext.CloneCheckpoint(p.source), Root: p.root,
		Cursor: SummaryCursor{Turn: p.turn, Checkpoint: runtimecontext.CloneCheckpoint(p.current), ExpectedTurnCheckpointID: p.current.ID,
			ExpectedHeadCheckpointID: binding.Cursor.ExpectedHeadCheckpointID, UpdatedAt: binding.Cursor.UpdatedAt}}, nil
}
func (p *summaryCorePorts) CommitSummary(_ context.Context, cursor SummaryCursor, target runtimecontext.Checkpoint) (SummaryCommitReceipt, error) {
	if p.commitCut == "before" {
		p.commitCut = ""
		return SummaryCommitReceipt{}, errSummaryCoreCrash
	}
	if cursor.Turn.Revision != p.turn.Revision || cursor.Checkpoint.ID != p.current.ID || cursor.ExpectedTurnCheckpointID != p.current.ID {
		return SummaryCommitReceipt{}, kernel.ErrConflict
	}
	p.current = runtimecontext.CloneCheckpoint(target)
	p.turn.ContextCheckpointID, p.turn.ContextRef = target.ID, summaryRef(target)
	p.turn.Revision++
	p.turn.UpdatedAt = cursor.UpdatedAt
	if p.commitCut == "after" {
		p.commitCut = ""
		return SummaryCommitReceipt{}, errSummaryCoreCrash
	}
	return SummaryCommitReceipt{Turn: p.turn, Checkpoint: target, ExpectedTurnCheckpointID: cursor.ExpectedTurnCheckpointID, ExpectedHeadCheckpointID: cursor.ExpectedHeadCheckpointID}, nil
}
func (p *summaryCorePorts) BindSummary(_ context.Context, cursor SummaryCursor, target runtimecontext.Checkpoint) (SummaryBindingReceipt, error) {
	if !sameBoundedValue(p.bound, cursor.Checkpoint) {
		return SummaryBindingReceipt{}, kernel.ErrConflict
	}
	previous := runtimecontext.CloneCheckpoint(p.bound)
	p.bound = runtimecontext.CloneCheckpoint(target)
	return SummaryBindingReceipt{TurnID: p.turn.ID, Access: "owner", Previous: previous, Current: p.bound}, nil
}

type summaryCoreFaultStore struct {
	*memory.Store
	event string
	after bool
	fired bool
}

func (s *summaryCoreFaultStore) Apply(ctx context.Context, id string, revision uint64, mutation kernel.StoreMutation) (kernel.Snapshot, error) {
	if !s.fired {
		for _, event := range mutation.Events {
			if event.Type == s.event {
				s.fired = true
				if s.after {
					if _, err := s.Store.Apply(ctx, id, revision, mutation); err != nil {
						return kernel.Snapshot{}, err
					}
				}
				return kernel.Snapshot{}, errSummaryCoreCrash
			}
		}
	}
	return s.Store.Apply(ctx, id, revision, mutation)
}

func (s *summaryCoreFaultStore) ApplyExecution(ctx context.Context, lease kernel.ExecutionLease, revision uint64, mutation kernel.StoreMutation) (kernel.Snapshot, error) {
	if !s.fired {
		for _, event := range mutation.Events {
			if event.Type == s.event {
				s.fired = true
				if s.after {
					if _, err := s.Store.ApplyExecution(ctx, lease, revision, mutation); err != nil {
						return kernel.Snapshot{}, err
					}
				}
				return kernel.Snapshot{}, errSummaryCoreCrash
			}
		}
	}
	return s.Store.ApplyExecution(ctx, lease, revision, mutation)
}

func newSummaryCoreFixture(t *testing.T, store kernel.Store) (*Runner, *kernel.Runtime, *summaryCorePorts, *summaryCoreModel, *modelRetryTestClock, StartRequest) {
	t.Helper()
	clock := &modelRetryTestClock{now: time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)}
	if store == nil {
		store = memory.NewStore()
	}
	rt, err := kernel.New(kernel.Dependencies{Store: store, Clock: clock})
	if err != nil {
		t.Fatal(err)
	}
	manager := runtimecontext.NewManager(runtimecontext.Dependencies{})
	entries := []runtimecontext.Entry{
		{ID: "old-user", SourceID: "old-user", Message: model.Message{Role: model.RoleUser, Content: model.TextParts(strings.Repeat("Original contract and exact evidence. ", 250))}},
		{ID: "old-answer", SourceID: "old-answer", Message: model.Message{Role: model.RoleAssistant, Content: model.TextParts("read the contract")}},
		{ID: "current-user", SourceID: "current-user", Required: true, Message: model.Message{Role: model.RoleUser, Content: model.TextParts("answer the current request")}},
	}
	cp, err := manager.Open(t.Context(), runtimecontext.OpenRequest{ScopeID: "session", StaticFingerprint: "static", Instructions: "Preserve the contract.",
		SourcePath: []string{"old-user", "old-answer", "current-user"}, Entries: entries})
	if err != nil {
		t.Fatal(err)
	}
	ports := &summaryCorePorts{manager: manager, source: cp, current: cp, bound: cp, reader: true, owner: true,
		root:   ContextSummaryRoot{InvocationID: "root", InputHash: strings.Repeat("b", 64), Attempt: 1},
		policy: runtimecontext.Policy{SoftLimitPercent: 10, PreserveRecentTurns: 1, MaxCompactionTokens: 512},
		window: runtimecontext.ModelWindow{ContextTokens: 30000, ReservedOutputTokens: 512}, artifacts: map[string]runtimecontext.Artifact{}}
	ports.turn = SummaryTurn{ID: "turn", SessionID: "session", ConfigSnapshotID: "config", ContextCheckpointID: cp.ID, ContextRef: summaryRef(cp),
		Status: "accepted", Revision: 2, CreatedAt: clock.Now(), UpdatedAt: clock.Now()}
	provider := &summaryCoreModel{}
	runner := summaryCoreRunner(t, rt, ports, provider, clock, nil)
	request := StartRequest{ID: "summary-run", Actor: kernel.ActorRef{TenantID: "tenant", ActorID: "owner"}, Thread: kernel.ThreadRef{Kind: "thread", ID: "thread"},
		RequestID: "summary-request", Goal: "answer the current request", Model: "fixture", MaxModelOutputTokens: 512,
		WorkingStateSummary: &WorkingStateSummaryPolicy{MaxInputTokens: 25000, MaxSerializedBytes: 50000}}
	return runner, rt, ports, provider, clock, request
}

func summaryCoreRunner(t *testing.T, rt *kernel.Runtime, ports *summaryCorePorts, provider model.Client, clock kernel.Clock, observers []plugin.Observer) *Runner {
	t.Helper()
	bounded, err := NewBoundedContextAdapter(ports.manager, ports)
	if err != nil {
		t.Fatal(err)
	}
	summary, err := NewContextSummaryAdapter(bounded, ports)
	if err != nil {
		t.Fatal(err)
	}
	runner, err := NewRunner(Dependencies{Runtime: rt, Model: provider, Clock: clock, RunMiddleware: []plugin.RunMiddleware{bounded}, ContextSummary: summary, Observers: observers})
	if err != nil {
		t.Fatal(err)
	}
	return runner
}

func TestContextSummaryNativeSuccessHasOneReceiptAndOneBusinessCall(t *testing.T) {
	runner, rt, ports, provider, _, request := newSummaryCoreFixture(t, nil)
	snapshot, err := runner.StartRun(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	view, err := ViewState(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Run.Status != kernel.RunStatusCompleted || len(provider.calls()) != 2 || len(view.ModelInvocations) != 2 || view.Budget.Usage.LLMCalls != 2 ||
		view.Budget.Usage.TotalTokens != 20 || view.Budget.Usage.OutputTokens != 6 || view.Budget.Usage.ReasoningTokens != 4 {
		t.Fatalf("summary did not use the original billable lifecycle: status=%s usage=%+v invocations=%d calls=%d policy=%+v", snapshot.Run.Status, view.Budget.Usage, len(view.ModelInvocations), len(provider.calls()), view.WorkingStateSummary)
	}
	if view.ModelInvocations[0].Purpose != ModelInvocationContextSummary || view.ModelInvocations[0].Status != ModelInvocationConsumed ||
		view.ModelInvocations[1].Purpose != "" || !summaryToolFree(provider.calls()[0]) || provider.calls()[0].MaxOutputTokens != 512 || provider.calls()[1].MaxOutputTokens != 512 {
		t.Fatal("summary or business lost its purpose/tool/cap boundary")
	}
	if len(view.Messages) != 2 || model.TextValue(view.Messages[1].Content) != "business answer" || ports.current.Window.WorkingState == nil ||
		!VerifyWorkingStateSummaryOrigin(snapshot, ports.current.Window.WorkingState, "session", request.Actor, request.Thread) {
		t.Fatal("summary became a business response or lost verifiable origin")
	}
	for _, artifact := range ports.artifacts {
		if strings.Contains(string(artifact.ContentJSON), "Derivation") || strings.Contains(string(artifact.ContentJSON), "working_state_summary") {
			t.Fatal("summary receipt was archived as original evidence")
		}
	}
	if len(ports.artifacts) == 0 || !strings.Contains(model.TextValue(provider.calls()[1].Messages[len(provider.calls()[1].Messages)-1].Content), "current request") {
		t.Fatal("summary dropped exact evidence or the actual current request")
	}
	loaded, err := rt.Load(t.Context(), request.ID)
	if err != nil || !reflect.DeepEqual(loaded.State, snapshot.State) {
		t.Fatal("native summary result was not durable")
	}
}

func TestContextSummaryKnownInvalidIsChargedAndUsesOriginalFallback(t *testing.T) {
	for name, response := range map[string]model.Response{
		"invalid JSON":  {Content: "not JSON"},
		"case alias":    {Content: strings.Replace(summaryCoreDocument, `"goal"`, `"Goal"`, 1)},
		"duplicate":     {Content: strings.Replace(summaryCoreDocument, `"goal":`, `"goal":"first","goal":`, 1)},
		"forged source": {Content: strings.Replace(summaryCoreDocument, `"goal":`, `"source":"forged","goal":`, 1)},
		"refused":       {Refused: true, Content: summaryCoreDocument},
		"continue":      {Continue: true, Content: summaryCoreDocument},
		"local tool":    {Content: summaryCoreDocument, ToolCalls: []tools.Call{{ID: "forbidden", ToolKey: "forbidden", Arguments: json.RawMessage(`{}`)}}},
		"hosted tool":   {Content: summaryCoreDocument, HostedToolCalls: []model.HostedToolCall{{ID: "forbidden", ToolKey: "forbidden", Status: "completed"}}},
	} {
		t.Run(name, func(t *testing.T) {
			runner, _, ports, provider, _, request := newSummaryCoreFixture(t, nil)
			provider.response = &response
			snapshot, err := runner.StartRun(t.Context(), request)
			if err != nil {
				t.Fatal(err)
			}
			view, err := ViewState(snapshot)
			if err != nil || view.Budget.Usage.LLMCalls != 2 || len(provider.calls()) != 2 || ports.current.Window.WorkingState != nil || view.ModelInvocations[0].SummaryTargetHash != "" {
				t.Fatalf("known invalid summary was installed, retried or unbilled: err=%v usage=%+v calls=%d", err, view.Budget.Usage, len(provider.calls()))
			}
		})
	}
}

type summaryCoreStreamingModel struct{ *summaryCoreModel }

func (p summaryCoreStreamingModel) GenerateStream(ctx context.Context, request model.Request, emit func(model.StreamEvent) error) (model.Response, error) {
	response, err := p.Generate(ctx, request)
	if err == nil {
		err = emit(model.StreamEvent{Delta: response.Content, ResponseID: response.ResponseID, Usage: model.CloneUsage(response.Usage)})
	}
	return response, err
}

type summaryCoreObserver struct{ deltas []string }

func (*summaryCoreObserver) Name() string { return "summary-core-observer" }
func (o *summaryCoreObserver) Observe(_ context.Context, event plugin.Event) {
	if event.Type == EventModelDelta {
		o.deltas = append(o.deltas, event.Delta)
	}
}

func TestContextSummaryStreamingOnlyPublishesBusinessContent(t *testing.T) {
	_, rt, ports, provider, clock, request := newSummaryCoreFixture(t, nil)
	observer := &summaryCoreObserver{}
	runner := summaryCoreRunner(t, rt, ports, summaryCoreStreamingModel{provider}, clock, []plugin.Observer{observer})
	snapshot, err := runner.StartRun(t.Context(), request)
	if err != nil || snapshot.Run.Status != kernel.RunStatusCompleted || !reflect.DeepEqual(observer.deltas, []string{"business answer"}) {
		t.Fatalf("internal JSON leaked into business streaming: err=%v deltas=%q", err, observer.deltas)
	}
	view, err := ViewState(snapshot)
	if err != nil || view.Budget.Usage.LLMCalls != 2 || view.Budget.Usage.TotalTokens != 20 || view.ModelInvocations[0].ProviderResponseID == view.ModelInvocations[1].ProviderResponseID {
		t.Fatal("stream suppression lost original receipt identity or usage")
	}
}

func TestContextSummaryProjectionBeforeAndAfterContextCommit(t *testing.T) {
	for _, order := range []string{"before", "after"} {
		t.Run(order, func(t *testing.T) {
			runner, rt, ports, provider, clock, request := newSummaryCoreFixture(t, nil)
			ports.commitCut = "after"
			ports.project = order == "before"
			if _, err := runner.StartRun(t.Context(), request); !errors.Is(err, errSummaryCoreCrash) {
				t.Fatalf("did not interrupt Context ACK: %v", err)
			}
			if order == "after" {
				ports.project = true
			}
			pending, err := rt.Load(t.Context(), request.ID)
			if err != nil {
				t.Fatal(err)
			}
			clock.Advance(3 * time.Minute)
			restarted := summaryCoreRunner(t, rt, ports, provider, clock, nil)
			completed, err := restarted.Resume(t.Context(), pending.Run.ID, pending.Run.Revision)
			if err != nil || completed.Run.Status != kernel.RunStatusCompleted || len(provider.calls()) != 2 {
				t.Fatalf("legitimate original projection blocked recovery: err=%v calls=%d", err, len(provider.calls()))
			}
		})
	}
}

func TestContextSummaryUnknownProviderReceiptNeverResends(t *testing.T) {
	fault := &summaryCoreFaultStore{Store: memory.NewStore(), event: "agent.model_invocation.completed"}
	runner, rt, ports, provider, clock, request := newSummaryCoreFixture(t, fault)
	if _, err := runner.StartRun(t.Context(), request); !errors.Is(err, ErrModelOutcomeUnknown) || len(provider.calls()) != 1 {
		t.Fatalf("missing physical receipt was treated as fallback: err=%v calls=%d", err, len(provider.calls()))
	}
	pending, err := rt.Load(t.Context(), request.ID)
	if err != nil {
		t.Fatal(err)
	}
	originalState := append(json.RawMessage(nil), pending.State...)
	clock.Advance(3 * time.Minute)
	restarted := summaryCoreRunner(t, rt, ports, provider, clock, nil)
	_, err = restarted.Resume(t.Context(), pending.Run.ID, pending.Run.Revision)
	current, loadErr := rt.Load(t.Context(), request.ID)
	if !errors.Is(err, ErrModelOutcomeUnknown) || loadErr != nil || len(provider.calls()) != 1 || !reflect.DeepEqual(current.State, originalState) || ports.current.Window.WorkingState != nil {
		t.Fatalf("unknown summary was redispatched or rewritten: err=%v calls=%d", err, len(provider.calls()))
	}
}

func TestContextSummaryKnownUsageExhaustionStopsBeforeBusiness(t *testing.T) {
	runner, _, ports, provider, _, request := newSummaryCoreFixture(t, nil)
	request.Limits.MaxOutputTokens = 2
	snapshot, err := runner.StartRun(t.Context(), request)
	view, viewErr := ViewState(snapshot)
	if !errors.Is(err, ErrBudgetExceeded) || viewErr != nil || len(provider.calls()) != 1 || view.Budget.Usage.LLMCalls != 1 || view.Budget.Usage.OutputTokens != 3 || ports.current.Window.WorkingState != nil {
		t.Fatalf("summary budget exhaustion dispatched business or lost usage: err=%v viewErr=%v calls=%d usage=%+v", err, viewErr, len(provider.calls()), view.Budget.Usage)
	}
}

func TestContextSummaryEligibilityAndProtectedTailRequireNoExtraCall(t *testing.T) {
	for _, name := range []string{"disabled", "one call", "one allowance", "input limit", "bytes limit", "read only", "no reader", "explicit host", "protected tail"} {
		t.Run(name, func(t *testing.T) {
			runner, _, ports, provider, _, request := newSummaryCoreFixture(t, nil)
			switch name {
			case "disabled":
				request.WorkingStateSummary = nil
			case "one call":
				request.Limits.MaxLLMCalls = 1
			case "one allowance":
				request.CallAllowance = &CallAllowance{LLMCalls: 1}
			case "input limit":
				request.WorkingStateSummary.MaxInputTokens = 1
			case "bytes limit":
				request.WorkingStateSummary.MaxSerializedBytes = 1
			case "read only":
				ports.owner = false
			case "no reader":
				ports.reader = false
			case "explicit host":
				ports.explicit = true
			case "protected tail":
				window := runtimecontext.CloneWindow(ports.source.Window)
				request.Goal = strings.Repeat("protected current input ", 2000)
				window.Entries[len(window.Entries)-1].Message.Content = model.TextParts(request.Goal)
				cp, err := ports.manager.Rollover(t.Context(), runtimecontext.RolloverRequest{Previous: ports.source, Window: window, Reason: "fixture"})
				if err != nil {
					t.Fatal(err)
				}
				ports.source, ports.current, ports.bound = cp, cp, cp
				ports.turn.ContextCheckpointID, ports.turn.ContextRef = cp.ID, summaryRef(cp)
			}
			_, _ = runner.StartRun(t.Context(), request)
			for _, call := range provider.calls() {
				if call.OutputContract != nil && call.OutputContract.Name == "working_state_summary" {
					t.Fatal("ineligible source spent an automatic summary call")
				}
			}
		})
	}
}

func TestContextSummaryNextTurnUpdatesOnlyVerifiedAutomaticState(t *testing.T) {
	for _, corruptOrigin := range []bool{false, true} {
		t.Run(map[bool]string{false: "verified V2", true: "foreign origin skips"}[corruptOrigin], func(t *testing.T) {
			runner, rt, ports, provider, clock, request := newSummaryCoreFixture(t, nil)
			first, err := runner.StartRun(t.Context(), request)
			if err != nil {
				t.Fatal(err)
			}
			priorState := runtimecontext.CloneWorkingState(ports.current.Window.WorkingState)
			if priorState == nil || priorState.Version != 1 {
				t.Fatal("first automatic state absent")
			}
			request.ID, request.RequestID, request.Goal = "summary-next", "summary-next-request", "answer the second request"
			nextSource, err := ports.manager.Open(t.Context(), runtimecontext.OpenRequest{
				ScopeID: ports.current.ScopeID, StaticFingerprint: ports.current.StaticFingerprint, Instructions: ports.current.Window.Instructions,
				SourceDelta: true, Previous: &ports.current, SourcePath: []string{"first-business", "second-user"}, Entries: []runtimecontext.Entry{
					{ID: "first-business", SourceID: "first-business", Message: model.Message{Role: model.RoleAssistant, Content: model.TextParts(strings.Repeat("New exact historical evidence. ", 250))}},
					{ID: "second-user", SourceID: "second-user", Required: true, Message: model.Message{Role: model.RoleUser, Content: model.TextParts(request.Goal)}},
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			ports.source, ports.current, ports.bound, ports.priorRun = nextSource, nextSource, nextSource, &first
			ports.turn.ID, ports.turn.ConfigSnapshotID, ports.turn.Revision = "turn-next", "config-next", 2
			ports.turn.ContextCheckpointID, ports.turn.ContextRef = nextSource.ID, summaryRef(nextSource)
			ports.turn.Status, ports.turn.UpdatedAt = "accepted", clock.Now()
			ports.root.InvocationID, ports.root.InputHash = "root-next", strings.Repeat("c", 64)
			if corruptOrigin {
				copy := cloneSummarySnapshot(first)
				copy.Run.Actor.ActorID = "foreign"
				ports.priorRun = &copy
			}
			nextRunner := summaryCoreRunner(t, rt, ports, provider, clock, nil)
			second, err := nextRunner.StartRun(t.Context(), request)
			if err != nil {
				t.Fatal(err)
			}
			wantVersion, wantCalls := 2, 4
			if corruptOrigin {
				wantVersion, wantCalls = 1, 3
			}
			if ports.current.Window.WorkingState.Version != wantVersion || len(provider.calls()) != wantCalls {
				t.Fatalf("automatic origin did not govern update: version=%d calls=%d", ports.current.Window.WorkingState.Version, len(provider.calls()))
			}
			if !corruptOrigin && (!VerifyWorkingStateSummaryOrigin(second, ports.current.Window.WorkingState, "session", request.Actor, request.Thread) ||
				len(ports.current.Window.WorkingState.EvidenceRefs) <= len(priorState.EvidenceRefs)) {
				t.Fatal("automatic V2 lost native provenance or old evidence")
			}
		})
	}
}

func TestContextSummaryContextAndNativeConsumeACKLossRecoverWithoutNewSummary(t *testing.T) {
	for _, cut := range []string{"before-context", "after-context", "before-consume", "after-consume", "before-pending", "after-pending", "completed"} {
		t.Run(cut, func(t *testing.T) {
			backing := memory.NewStore()
			fault := &summaryCoreFaultStore{Store: backing}
			runner, rt, ports, provider, clock, request := newSummaryCoreFixture(t, fault)
			switch cut {
			case "before-context":
				ports.commitCut = "before"
			case "after-context":
				ports.commitCut = "after"
			case "before-consume":
				fault.event = EventContextSummaryConsumed
			case "after-consume":
				fault.event, fault.after = EventContextSummaryConsumed, true
			case "before-pending":
				fault.event = "agent.model_invocation.pending"
			case "after-pending":
				fault.event, fault.after = "agent.model_invocation.pending", true
			case "completed":
				fault.event, fault.after = "agent.model_invocation.completed", true
			}
			_, err := runner.StartRun(t.Context(), request)
			if !errors.Is(err, errSummaryCoreCrash) {
				t.Fatalf("fault did not stop original execution: %v", err)
			}
			pending, err := rt.Load(t.Context(), request.ID)
			if err != nil {
				t.Fatal(err)
			}
			targetBeforeResume := ports.current.ID
			clock.Advance(3 * time.Minute)
			restarted := summaryCoreRunner(t, rt, ports, provider, clock, nil)
			completed, err := restarted.Resume(t.Context(), pending.Run.ID, pending.Run.Revision)
			if err != nil {
				t.Fatal(err)
			}
			view, err := ViewState(completed)
			if err != nil || completed.Run.Status != kernel.RunStatusCompleted || len(provider.calls()) != 2 || len(view.ModelInvocations) != 2 || view.Budget.Usage.LLMCalls != 2 || view.Budget.Usage.TotalTokens != 20 {
				t.Fatalf("recovery changed the logical summary or usage: err=%v calls=%d view=%+v", err, len(provider.calls()), view.Budget.Usage)
			}
			if !runtimecontext.ValidCheckpoint(ports.current) || ports.current.ParentCheckpointID != ports.source.ID ||
				(cut == "after-context" && ports.current.ID != targetBeforeResume) {
				t.Fatalf("recovery used an unexpected summary target: %s", ports.current.ID)
			}
			legacy := cloneModelInvocation(view.ModelInvocations[0])
			legacy.ContextSummary.RecipeVersion = "working-state-summary-v2"
			if validContextSummaryInvocation(legacy) {
				t.Fatal("accepted a v2 summary recipe")
			}
		})
	}
}

// This exact-counter fixture has an input projection whose tokenizer treats
// different identity text differently. Equal JSON byte lengths are insufficient.
type summaryIdentityCounter struct {
	requests     []model.Request
	afterSummary func()
}

func (counter *summaryIdentityCounter) Count(_ context.Context, input runtimecontext.TokenCountRequest) (int64, error) {
	var request model.Request
	if err := json.Unmarshal(input.Payload, &request); err != nil {
		return 0, err
	}
	if request.OutputContract == nil || request.OutputContract.Name != "working_state_summary" {
		return 10000, nil
	}
	counter.requests = append(counter.requests, model.CloneRequest(request))
	count := int64(1000)
	for _, character := range strings.TrimPrefix(request.InvocationID, "modelinv_") {
		if character != '0' {
			count++
		}
	}
	if counter.afterSummary != nil {
		counter.afterSummary()
	}
	return count, nil
}

func TestContextSummaryExactCounterUsesFinalInvocationIdentity(t *testing.T) {
	for _, limit := range []int64{1000, 1032} {
		t.Run(map[bool]string{true: "below actual count", false: "fits actual count"}[limit == 1000], func(t *testing.T) {
			_, rt, ports, provider, clock, request := newSummaryCoreFixture(t, nil)
			counter := &summaryIdentityCounter{}
			ports.manager = runtimecontext.NewManager(runtimecontext.Dependencies{Counter: counter})
			ports.window.TokenCountContext = runtimecontext.TokenCountContext{Protocol: "fixture", Model: "fixture"}
			request.WorkingStateSummary.MaxInputTokens = limit
			runner := summaryCoreRunner(t, rt, ports, provider, clock, nil)
			if _, err := runner.StartRun(t.Context(), request); err != nil {
				t.Fatal(err)
			}
			if len(counter.requests) != 1 || counter.requests[0].InvocationID == "" ||
				counter.requests[0].InvocationID == "modelinv_"+strings.Repeat("0", 32) {
				t.Fatal("summary counter did not receive the final non-placeholder identity")
			}
			calls := provider.calls()
			if limit == 1000 {
				if len(calls) != 1 || calls[0].OutputContract != nil {
					t.Fatal("summary above its exact input ceiling was dispatched")
				}
				return
			}
			if len(calls) != 2 || !reflect.DeepEqual(calls[0], counter.requests[0]) {
				t.Fatal("physical summary did not match the exact counted request")
			}
		})
	}
}

type summaryChangingBindingModel struct {
	*summaryCoreModel
	binding string
}

func (provider *summaryChangingBindingModel) BindingFingerprintForRequest(model.Request) string {
	return provider.binding
}

func TestContextSummaryCountedIdentityCannotChangeBeforeIntent(t *testing.T) {
	_, rt, ports, recorded, clock, request := newSummaryCoreFixture(t, nil)
	provider := &summaryChangingBindingModel{summaryCoreModel: recorded, binding: strings.Repeat("a", 64)}
	counter := &summaryIdentityCounter{afterSummary: func() { provider.binding = strings.Repeat("b", 64) }}
	ports.manager = runtimecontext.NewManager(runtimecontext.Dependencies{Counter: counter})
	ports.window.TokenCountContext = runtimecontext.TokenCountContext{Protocol: "fixture", Model: "fixture"}
	runner := summaryCoreRunner(t, rt, ports, provider, clock, nil)
	if _, err := runner.StartRun(t.Context(), request); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("changed Provider binding replaced the counted summary identity: %v", err)
	}
	if len(counter.requests) != 1 || len(recorded.calls()) != 0 {
		t.Fatal("summary identity changed after its final input measurement")
	}
	snapshot, err := rt.Load(t.Context(), request.ID)
	if err != nil {
		t.Fatal(err)
	}
	view, err := ViewState(snapshot)
	if err != nil || len(view.ModelInvocations) != 0 {
		t.Fatal("changed counted identity left a durable summary intent")
	}
}

func TestContextSummaryInputBoundsRetainRawOptionNumberBytes(t *testing.T) {
	options := json.RawMessage(`{"temperature":0.1` + strings.Repeat("0", 300) + `}`)
	runner, _, ports, provider, _, request := newSummaryCoreFixture(t, nil)
	request.ModelOptions = options
	if _, err := runner.StartRun(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	actual := provider.calls()[0]
	normalized := model.CloneRequest(actual)
	normalized.ModelOptions = json.RawMessage(`{"temperature":0.1}`)
	baseline, err := ports.manager.AssessModelRequest(t.Context(), normalized, ports.window, ports.policy)
	if err != nil {
		t.Fatal(err)
	}
	full, err := ports.manager.AssessModelRequest(t.Context(), actual, ports.window, ports.policy)
	if err != nil || full.SerializedBytes <= baseline.SerializedBytes || full.HardTokenEstimate <= baseline.HardTokenEstimate {
		t.Fatal("fixture did not preserve raw number spelling in the full input bound")
	}
	for _, dimension := range []string{"serialized bytes", "fallback token upper bound"} {
		t.Run(dimension, func(t *testing.T) {
			runner, _, _, provider, _, request := newSummaryCoreFixture(t, nil)
			request.ModelOptions = options
			if dimension == "serialized bytes" {
				request.WorkingStateSummary.MaxSerializedBytes = baseline.SerializedBytes
			} else {
				request.WorkingStateSummary.MaxInputTokens = baseline.HardTokenEstimate
			}
			if _, err := runner.StartRun(t.Context(), request); err != nil {
				t.Fatal(err)
			}
			calls := provider.calls()
			if len(calls) != 1 || calls[0].OutputContract != nil {
				t.Fatal("raw option bytes escaped the independent summary input limit")
			}
		})
	}
}

func TestContextSummaryActiveInputBytesAreCheckedOnRead(t *testing.T) {
	for _, event := range []string{"agent.model_invocation.pending", "agent.model_invocation.completed"} {
		t.Run(event, func(t *testing.T) {
			fault := &summaryCoreFaultStore{Store: memory.NewStore(), event: event, after: true}
			runner, rt, _, _, _, request := newSummaryCoreFixture(t, fault)
			if _, err := runner.StartRun(t.Context(), request); !errors.Is(err, errSummaryCoreCrash) {
				t.Fatalf("fixture missed the active summary boundary: %v", err)
			}
			snapshot, err := rt.Load(t.Context(), request.ID)
			if err != nil {
				t.Fatal(err)
			}
			state, err := decodeState(snapshot.State)
			if err != nil {
				t.Fatal(err)
			}
			invocation := &state.ModelInvocations[0]
			raw, err := model.MarshalRequestForInputCount(invocation.Request)
			if err != nil {
				t.Fatal(err)
			}
			state.WorkingStateSummary.MaxSerializedBytes = int64(len(raw) - 1)
			invocation.ContextSummary.Policy = *state.WorkingStateSummary
			sourceRun := snapshot.Run
			sourceRun.Revision = invocation.SourceRevision
			input := model.CloneRequest(invocation.Request)
			input.InvocationID = ""
			derived, deriveErr := newInvocation(sourceRun, input, nil, invocation.Provider, invocation.BindingFingerprint,
				invocation.Purpose, invocation.ContextSummary, invocation.CreatedAt)
			if deriveErr != nil {
				t.Fatal(deriveErr)
			}
			invocation.ID, invocation.RequestHash = derived.ID, derived.RequestHash
			invocation.Request.InvocationID = derived.ID
			if !validContextSummaryInvocation(*invocation) {
				t.Fatal("fixture damaged the invocation apart from its real input size")
			}
			snapshot.State, err = encodeState(state)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = ViewState(snapshot); !errors.Is(err, ErrInvalidRequest) {
				t.Fatalf("reader accepted an active summary over its frozen byte limit: %v", err)
			}
		})
	}
}

type summaryJSONBusinessModel struct{ *summaryCoreModel }

func (provider summaryJSONBusinessModel) Generate(ctx context.Context, request model.Request) (model.Response, error) {
	response, err := provider.summaryCoreModel.Generate(ctx, request)
	if request.OutputContract == nil || request.OutputContract.Name != "working_state_summary" {
		response.Content = `{"answer":"business answer"}`
	}
	return response, err
}

func TestContextSummaryDoesNotInstallTargetMissingBusinessIdentityBudget(t *testing.T) {
	// Business schema framing is deliberately larger than the independent
	// summary schema, so the business byte limit is the tighter target check.
	schema, err := json.Marshal(map[string]any{"type": "object", "properties": map[string]any{
		"answer": map[string]any{"type": "string", "description": strings.Repeat("s", 14000)},
	}, "required": []string{"answer"}, "additionalProperties": false})
	if err != nil {
		t.Fatal(err)
	}
	run := func(limit int64) (*summaryCorePorts, *summaryCoreModel, kernel.Snapshot, error) {
		_, rt, ports, recorded, clock, request := newSummaryCoreFixture(t, nil)
		ports.policy.MaxSerializedBytes = limit
		request.OutputContract = &model.OutputContract{Name: "business", Schema: schema}
		runner := summaryCoreRunner(t, rt, ports, summaryJSONBusinessModel{recorded}, clock, nil)
		snapshot, runErr := runner.StartRun(t.Context(), request)
		return ports, recorded, snapshot, runErr
	}
	ports, recorded, _, err := run(0)
	if err != nil || len(recorded.calls()) != 2 || ports.current.Window.WorkingState == nil {
		t.Fatalf("fixture missed the unconstrained successful target: %v", err)
	}
	withoutIdentity := model.CloneRequest(recorded.calls()[1])
	withoutIdentity.InvocationID = ""
	without, err := ports.manager.AssessModelRequest(t.Context(), withoutIdentity, ports.window, ports.policy)
	if err != nil {
		t.Fatal(err)
	}
	limited, _, snapshot, err := run(without.SerializedBytes)
	if err != nil && !errors.Is(err, runtimecontext.ErrBudgetExceeded) {
		t.Fatal(err)
	}
	if limited.current.Window.WorkingState != nil {
		t.Fatal("summary installed state whose prospective business frame omitted its identity budget")
	}
	view, viewErr := ViewState(snapshot)
	if viewErr != nil {
		t.Fatal(viewErr)
	}
	for _, invocation := range view.ModelInvocations {
		if invocation.Purpose == ModelInvocationContextSummary && invocation.SummaryTargetHash != "" {
			t.Fatal("rejected oversized target retained a successful installation proof")
		}
	}
}

func TestContextSummaryPreservesArchivedToolArgumentNumbers(t *testing.T) {
	for _, number := range []string{"9007199254740993", "1e1000", "0.10000000000000001", "1.00"} {
		t.Run(number, func(t *testing.T) {
			arguments := `{"n":` + number + `}`
			_, rt, ports, provider, clock, request := newSummaryCoreFixture(t, nil)
			summaryHistoryToolSource(t, ports, arguments)
			runner := summaryCoreRunner(t, rt, ports, provider, clock, nil)
			completed, err := runner.StartRun(t.Context(), request)
			if err != nil || completed.Run.Status != kernel.RunStatusCompleted ||
				len(provider.calls()) != 2 || ports.current.Window.WorkingState == nil || len(ports.artifacts) != 1 {
				t.Fatalf("lossless history did not install summary state: status=%s err=%v", completed.Run.Status, err)
			}
			for _, artifact := range ports.artifacts {
				if artifact.Encoding != runtimecontext.ArtifactEncodingLosslessJSON ||
					!strings.Contains(string(artifact.ContentJSON), `"arguments":`+arguments) {
					t.Fatalf("summary archive changed Tool arguments: %s", artifact.ContentJSON)
				}
			}
		})
	}
}

func summaryHistoryToolSource(t *testing.T, ports *summaryCorePorts, arguments string) {
	t.Helper()
	entries := runtimecontext.CloneWindow(ports.source.Window).Entries
	entries[1].Message.ToolCalls = []tools.Call{{ID: "historical", ToolKey: "historic", Arguments: json.RawMessage(arguments)}}
	entries = append(entries[:2], append([]runtimecontext.Entry{{
		ID: "old-tool", SourceID: "old-tool", Message: model.Message{Role: model.RoleTool, ToolCallID: "historical", Content: model.TextParts(`{"n":9007199254740993}`)},
	}}, entries[2:]...)...)
	source, err := ports.manager.Open(t.Context(), runtimecontext.OpenRequest{ScopeID: ports.source.ScopeID,
		StaticFingerprint: ports.source.StaticFingerprint, Instructions: ports.source.Window.Instructions,
		SourcePath: []string{"old-user", "old-answer", "old-tool", "current-user"}, Entries: entries})
	if err != nil {
		t.Fatal(err)
	}
	ports.source, ports.current, ports.bound = source, source, source
	ports.turn.ContextCheckpointID, ports.turn.ContextRef = source.ID, summaryRef(source)
}

func TestContextSummaryCompletedReceiptRejectsChangedFrozenSource(t *testing.T) {
	fault := &summaryCoreFaultStore{Store: memory.NewStore(), event: "agent.model_invocation.completed", after: true}
	_, rt, ports, provider, clock, request := newSummaryCoreFixture(t, fault)
	summaryHistoryToolSource(t, ports, `{"n":9007199254740992}`)
	runner := summaryCoreRunner(t, rt, ports, provider, clock, nil)
	if _, err := runner.StartRun(t.Context(), request); !errors.Is(err, errSummaryCoreCrash) {
		t.Fatalf("fixture missed the completed summary receipt: %v", err)
	}
	before, err := rt.Load(t.Context(), request.ID)
	if err != nil {
		t.Fatal(err)
	}
	// Replace one number while retaining the frozen receipt and Checkpoint ID.
	unsafe := runtimecontext.CloneCheckpoint(ports.source)
	unsafe.Window.Entries[1].Message.ToolCalls[0].Arguments = json.RawMessage(`{"n":9007199254740993}`)
	if runtimecontext.ValidCheckpoint(unsafe) {
		t.Fatal("changed number passed Checkpoint validation")
	}
	ports.source, ports.current, ports.bound = unsafe, unsafe, unsafe
	clock.Advance(3 * time.Minute)
	restarted := summaryCoreRunner(t, rt, ports, provider, clock, nil)
	if _, err = restarted.Resume(t.Context(), before.Run.ID, before.Run.Revision); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("completed summary accepted a changed original source: %v", err)
	}
	after, err := rt.Load(t.Context(), request.ID)
	if err != nil {
		t.Fatal(err)
	}
	view, err := ViewState(after)
	if err != nil || len(provider.calls()) != 1 || len(ports.artifacts) != 0 || ports.current.Window.WorkingState != nil ||
		!reflect.DeepEqual(after.State, before.State) || len(view.ModelInvocations) != 1 || view.ModelInvocations[0].Status != ModelInvocationCompleted || view.Budget.Usage.LLMCalls != 0 {
		t.Fatalf("rejected source consumed a receipt, installed state or ran business: viewErr=%v calls=%d", err, len(provider.calls()))
	}
}
