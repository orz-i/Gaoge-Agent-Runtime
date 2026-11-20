package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	runtimecontext "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/context"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/internal/callbackscope"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

func samplingCloneFixture() SamplingPlan {
	raw := func() json.RawMessage { return json.RawMessage(`{ "z":9007199254740993, "a":1 }`) }
	now := time.Date(2026, 1, 2, 3, 4, 5, 6, time.FixedZone("original", 3600))
	ceiling := 2
	message := model.Message{Role: model.RoleUser, Content: model.TextParts("original"),
		CacheControl:   &model.CacheControl{Type: "ephemeral"},
		ToolResult:     &model.ToolResultMetadata{},
		ProviderReplay: &model.ProviderReplay{Data: raw()},
		ToolCalls:      []tools.Call{{ID: "call", ToolKey: "tool", Arguments: raw()}},
	}
	message.Content = append(message.Content, model.ContentPart{Kind: model.ContentImage, Source: &model.MediaSource{Kind: model.MediaSourceURL, URL: "https://example.invalid/original"}, CacheControl: &model.CacheControl{Type: "ephemeral"}})
	checkpoint := runtimecontext.Checkpoint{Window: runtimecontext.Window{Entries: []runtimecontext.Entry{{ID: "entry", Message: message}}, WorkingState: &runtimecontext.WorkingState{
		Version: 1, Source: runtimecontext.WorkingStateSource{CheckpointID: "source-checkpoint"}, Goal: "original goal",
		Constraints: []string{"constraint"}, Completed: []string{"completed"}, Pending: []string{"pending"},
		Decisions: []string{"decision"}, EvidenceRefs: []string{"evidence"},
		Derivation: &runtimecontext.WorkingStateDerivation{Kind: "model_summary", RunID: "original-run"},
	}},
		ArtifactIDs: []string{"artifact"}, Trace: runtimecontext.Trace{LastAssessment: &runtimecontext.Assessment{HardInputTokens: 100}}}
	return SamplingPlan{
		Source: kernel.Snapshot{Run: kernel.Run{ID: "run", DeadlineAt: &now, EndedAt: &now}, State: raw(),
			Checkpoint: &kernel.Checkpoint{Payload: raw(), Response: json.RawMessage{}, ResolvedAt: &now},
			Result:     &kernel.Result{Content: raw()}},
		Request: model.Request{RunID: "run", Model: "model", ModelOptions: raw(), HostedToolCallLimit: &ceiling,
			OutputContract: &model.OutputContract{Name: "output", Schema: raw()}, Messages: []model.Message{message},
			Tools: []tools.Definition{{Key: "tool", InputSchema: raw()}}, HostedTools: []model.HostedTool{{Key: "native", Target: raw()}}},
		Checkpoint: &checkpoint, Cursor: SamplingCursor{Checkpoint: checkpoint},
		Guidance: []model.Message{message}, Data: raw(),
		ModelWindow: runtimecontext.ModelWindow{ContextTokens: 1000, MaxContextTokens: 900, EffectivePercent: 90, ReservedOutputTokens: 100,
			TokenCountContext: runtimecontext.TokenCountContext{Protocol: "openai_responses", Model: "tokenizer-original"}},
	}
}

func TestSamplingTypedCopiesPreserveFactsAndIsolate(t *testing.T) {
	original := samplingCloneFixture()
	for _, mutate := range []struct {
		name   string
		change func(*SamplingPlan)
	}{
		{"state", func(p *SamplingPlan) { p.Source.State[0] = '!' }},
		{"deadline", func(p *SamplingPlan) { *p.Source.Run.DeadlineAt = time.Time{} }},
		{"ended", func(p *SamplingPlan) { *p.Source.Run.EndedAt = time.Time{} }},
		{"wait", func(p *SamplingPlan) {
			p.Source.Checkpoint.Payload[0] = '!'
			*p.Source.Checkpoint.ResolvedAt = time.Time{}
		}},
		{"result", func(p *SamplingPlan) { p.Source.Result.Content[0] = '!' }},
		{"options", func(p *SamplingPlan) { p.Request.ModelOptions[0] = '!' }},
		{"output", func(p *SamplingPlan) { p.Request.OutputContract.Schema[0] = '!' }},
		{"limit", func(p *SamplingPlan) { *p.Request.HostedToolCallLimit = 0 }},
		{"tool", func(p *SamplingPlan) { p.Request.Tools[0].InputSchema[0] = '!' }},
		{"hosted", func(p *SamplingPlan) { p.Request.HostedTools[0].Target[0] = '!' }},
		{"message", func(p *SamplingPlan) {
			p.Request.Messages[0].Content[0].Text = "changed"
			p.Request.Messages[0].CacheControl.Type = "changed"
		}},
		{"media", func(p *SamplingPlan) {
			p.Request.Messages[0].Content[1].Source.URL = "foreign"
			p.Request.Messages[0].Content[1].CacheControl.Type = "changed"
			p.Request.Messages[0].ToolResult.Name = "changed"
		}},
		{"call", func(p *SamplingPlan) { p.Request.Messages[0].ToolCalls[0].Arguments[0] = '!' }},
		{"replay", func(p *SamplingPlan) { p.Request.Messages[0].ProviderReplay.Data[0] = '!' }},
		{"checkpoint", func(p *SamplingPlan) {
			p.Checkpoint.Window.Entries[0].Message.ToolCalls[0].Arguments[0] = '!'
			p.Checkpoint.ArtifactIDs[0] = "changed"
			p.Checkpoint.Trace.LastAssessment.HardInputTokens = 0
		}},
		{"cursor", func(p *SamplingPlan) { p.Cursor.Checkpoint.Window.Entries[0].Message.ProviderReplay.Data[0] = '!' }},
		{"checkpoint working-state identity", func(p *SamplingPlan) {
			p.Checkpoint.Window.WorkingState.Version = 2
			p.Checkpoint.Window.WorkingState.Source.CheckpointID = "foreign"
			p.Checkpoint.Window.WorkingState.Goal = "foreign"
		}},
		{"checkpoint working-state constraints", func(p *SamplingPlan) { p.Checkpoint.Window.WorkingState.Constraints[0] = "foreign" }},
		{"checkpoint working-state completed", func(p *SamplingPlan) { p.Checkpoint.Window.WorkingState.Completed[0] = "foreign" }},
		{"checkpoint working-state pending", func(p *SamplingPlan) { p.Checkpoint.Window.WorkingState.Pending[0] = "foreign" }},
		{"checkpoint working-state decisions", func(p *SamplingPlan) { p.Checkpoint.Window.WorkingState.Decisions[0] = "foreign" }},
		{"checkpoint working-state evidence", func(p *SamplingPlan) { p.Checkpoint.Window.WorkingState.EvidenceRefs[0] = "foreign" }},
		{"cursor working-state identity", func(p *SamplingPlan) {
			p.Cursor.Checkpoint.Window.WorkingState.Version = 2
			p.Cursor.Checkpoint.Window.WorkingState.Source.CheckpointID = "foreign"
			p.Cursor.Checkpoint.Window.WorkingState.Goal = "foreign"
		}},
		{"cursor working-state constraints", func(p *SamplingPlan) { p.Cursor.Checkpoint.Window.WorkingState.Constraints[0] = "foreign" }},
		{"cursor working-state completed", func(p *SamplingPlan) { p.Cursor.Checkpoint.Window.WorkingState.Completed[0] = "foreign" }},
		{"cursor working-state pending", func(p *SamplingPlan) { p.Cursor.Checkpoint.Window.WorkingState.Pending[0] = "foreign" }},
		{"cursor working-state decisions", func(p *SamplingPlan) { p.Cursor.Checkpoint.Window.WorkingState.Decisions[0] = "foreign" }},
		{"cursor working-state evidence", func(p *SamplingPlan) { p.Cursor.Checkpoint.Window.WorkingState.EvidenceRefs[0] = "foreign" }},
		{"checkpoint working-state derivation", func(p *SamplingPlan) { p.Checkpoint.Window.WorkingState.Derivation.RunID = "foreign" }},
		{"cursor working-state derivation", func(p *SamplingPlan) { p.Cursor.Checkpoint.Window.WorkingState.Derivation.RunID = "foreign" }},
		{"guidance", func(p *SamplingPlan) { p.Guidance[0].Content[0].Text = "changed" }},
		{"data", func(p *SamplingPlan) { p.Data[0] = '!' }},
	} {
		t.Run(mutate.name, func(t *testing.T) {
			original := samplingCloneFixture()
			before := samplingCloneFixture()
			got := cloneSamplingPlan(original)
			if !reflect.DeepEqual(got, original) {
				t.Fatal("copy changed full original facts")
			}
			mutate.change(&got)
			if !reflect.DeepEqual(original, before) {
				t.Fatal("copy aliases original")
			}
		})
	}
	for _, empty := range []bool{false, true} {
		request := model.Request{}
		plan := SamplingPlan{}
		result := SamplingEvaluation{}
		if empty {
			request.Messages = []model.Message{{Content: []model.ContentPart{}, ToolCalls: []tools.Call{}, ProviderReplay: &model.ProviderReplay{Data: json.RawMessage{}}}}
			request.Tools = []tools.Definition{}
			request.HostedTools = []model.HostedTool{}
			request.ModelOptions = json.RawMessage{}
			request.OutputContract = &model.OutputContract{Schema: json.RawMessage{}}
			plan.Data = json.RawMessage{}
			plan.Guidance = []model.Message{}
			plan.Cursor.Checkpoint.ArtifactIDs = []string{}
			plan.Cursor.Checkpoint.Window.Entries = []runtimecontext.Entry{}
			plan.Cursor.Checkpoint.Window.WorkingState = &runtimecontext.WorkingState{
				Constraints: []string{}, Completed: []string{}, Pending: []string{}, Decisions: []string{}, EvidenceRefs: []string{},
			}
			result.Artifacts = []runtimecontext.Artifact{}
		}
		plan.Request = request
		result.Request = request
		if !reflect.DeepEqual(plan, cloneSamplingPlan(plan)) || !reflect.DeepEqual(result, cloneSamplingEvaluation(result)) {
			t.Fatalf("nil/empty representation changed (empty=%v)", empty)
		}
	}
	result := SamplingEvaluation{Request: original.Request, Artifacts: []runtimecontext.Artifact{{ContentJSON: json.RawMessage(`{ "n":9007199254740993 }`)}}}
	copied := cloneSamplingEvaluation(result)
	if !reflect.DeepEqual(copied, result) {
		t.Fatal("evaluation changed")
	}
	copied.Request.HostedTools[0].Target[0] = '!'
	copied.Artifacts[0].ContentJSON[0] = '!'
	if result.Request.HostedTools[0].Target[0] != '{' || result.Artifacts[0].ContentJSON[0] != '{' {
		t.Fatal("evaluation aliases port result")
	}
}

type samplingWorkingStateMutationPorts struct {
	samplingTestPorts
	retained SamplingPlan
}

func (p *samplingWorkingStateMutationPorts) PlanSampling(_ context.Context, r model.Request, source kernel.Snapshot) (SamplingPlan, error) {
	plan := *p.plan // Deliberately return retained pointers; Runtime must own its copies.
	plan.Source, plan.Request = source, r
	p.retained = plan
	return plan, nil
}

func (p *samplingWorkingStateMutationPorts) EvaluateSampling(_ context.Context, plan SamplingPlan, r model.Request, w runtimecontext.Window, _ bool) (SamplingEvaluation, error) {
	plan.Checkpoint.Window.WorkingState.Pending[0] = "mutated evaluation plan"
	plan.Cursor.Checkpoint.Window.WorkingState.Decisions[0] = "mutated evaluation cursor"
	w.WorkingState.EvidenceRefs[0] = "mutated evaluation window"
	// A port may also retain and later mutate its own PlanSampling return value.
	p.retained.Checkpoint.Window.WorkingState.Goal = "mutated retained result"
	p.retained.Cursor.Checkpoint.Window.WorkingState.Constraints[0] = "mutated retained cursor"
	return SamplingEvaluation{Request: r, Assessment: runtimecontext.Assessment{
		HardInputTokens: 100, SoftInputTokens: 80, AdjustedTokenEstimate: 10, HardTokenEstimate: 10, SerializedBytes: 10,
	}}, nil
}

func TestSamplingPreparationOwnsWorkingStateAcrossPortReturns(t *testing.T) {
	manager := runtimecontext.NewManager(runtimecontext.Dependencies{})
	open := runtimecontext.OpenRequest{ScopeID: "scope", StaticFingerprint: "static", SourcePath: []string{"source"}, Entries: []runtimecontext.Entry{{
		ID: "entry", SourceID: "source", Message: model.Message{Role: model.RoleUser, Content: model.TextParts("current request")},
	}}}
	previous, err := manager.Open(t.Context(), open)
	if err != nil {
		t.Fatal(err)
	}
	open.Previous, open.WorkingState = &previous, &runtimecontext.WorkingState{
		Version: 1, Source: runtimecontext.WorkingStateSource{CheckpointID: previous.ID, ContentHash: previous.ContentHash,
			CoveredThroughSourceID: previous.CoveredThroughSourceID, CoveredPathHash: previous.CoveredPathHash},
		Goal: "original goal", Constraints: []string{"constraint"}, Completed: []string{"completed"}, Pending: []string{"pending"},
		Decisions: []string{"decision"}, EvidenceRefs: []string{"evidence"},
	}
	previous, err = manager.Open(t.Context(), open)
	if err != nil {
		t.Fatal(err)
	}
	previous, err = manager.BindModelWindow(t.Context(), previous, "window")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)
	plan := SamplingPlan{Checkpoint: &previous, Owner: true, WindowFingerprint: "window", Cursor: SamplingCursor{
		Checkpoint: previous, ExpectedTurnCheckpointID: previous.ID, ExpectedHeadCheckpointID: previous.ID, UpdatedAt: now,
		Turn: SamplingTurn{ID: "turn", Revision: 1, ContextCheckpointID: previous.ID, ContextRef: samplingRef(previous)},
	}}
	ports := &samplingWorkingStateMutationPorts{samplingTestPorts: samplingTestPorts{plan: &plan}}
	adapter, err := NewSamplingBoundedContextAdapter(manager, ports)
	if err != nil {
		t.Fatal(err)
	}
	chain, err := plugin.NewRunChain(adapter)
	if err != nil {
		t.Fatal(err)
	}
	scope := callbackscope.New(callbackscope.Binding{RunID: "run", GrantID: "original"}, []any{adapter})
	runner := &Runner{contextPreparation: adapter, rootCallbacks: &rootCallbackState{}}
	request := model.CloneRequest(model.Request{RunID: "run", Messages: runtimecontext.Materialize(previous.Window)})
	source := kernel.Snapshot{Run: kernel.Run{ID: "run", Revision: 1}}
	_, err = chain.Invoke(callbackscope.Bind(t.Context(), scope), plugin.RunInvocation{RunID: "run", Operation: plugin.RunStart}, func(ctx context.Context) (kernel.Snapshot, error) {
		got, prepareErr := runner.prepareSelectedContext(ctx, source, request)
		if prepareErr == nil && !reflect.DeepEqual(got, request) {
			t.Fatal("port mutation changed the frozen model-visible WorkingState")
		}
		return source, prepareErr
	})
	if err != nil || !scope.Complete() || runner.rootCallbacks.unknown.Load() {
		t.Fatalf("port-owned WorkingState aliased the Runtime plan: %v", err)
	}
	if ports.retained.Checkpoint.Window.WorkingState.Goal != "mutated retained result" || len(ports.commits) != 0 {
		t.Fatal("test did not mutate its retained plan or Capture rebuilt the original checkpoint")
	}
}

type samplingExactCounter struct {
	calls int
	seen  runtimecontext.TokenCountRequest
}

func (c *samplingExactCounter) Count(_ context.Context, r runtimecontext.TokenCountRequest) (int64, error) {
	c.calls++
	c.seen = r
	return 17, nil
}

type samplingCountingPorts struct {
	samplingTestPorts
	manager    *runtimecontext.Manager
	seen       runtimecontext.ModelWindow
	assessment runtimecontext.Assessment
}

func (p *samplingCountingPorts) EvaluateSampling(ctx context.Context, plan SamplingPlan, r model.Request, _ runtimecontext.Window, _ bool) (SamplingEvaluation, error) {
	p.seen = plan.ModelWindow
	a, err := p.manager.AssessModelRequest(ctx, r, plan.ModelWindow, plan.Policy)
	p.assessment = a
	// The port may retain and mutate its input after assessing it. That must
	// not change the algorithm's original request or frozen plan.
	result := cloneSamplingRequest(r)
	plan.ModelWindow.TokenCountContext.Model = "foreign"
	r.Messages[0].Content[0].Text = "port-mutated"
	return SamplingEvaluation{Request: result, Assessment: a}, err
}
func TestSamplingPreparationPreservesActualTokenizerContext(t *testing.T) {
	counter := &samplingExactCounter{}
	manager := runtimecontext.NewManager(runtimecontext.Dependencies{Counter: counter})
	window := samplingCloneFixture().ModelWindow
	ports := &samplingCountingPorts{samplingTestPorts: samplingTestPorts{plan: &SamplingPlan{ModelWindow: window}}, manager: manager}
	adapter, err := NewSamplingBoundedContextAdapter(manager, ports)
	if err != nil {
		t.Fatal(err)
	}
	chain, err := plugin.NewRunChain(adapter)
	if err != nil {
		t.Fatal(err)
	}
	scope := callbackscope.New(callbackscope.Binding{RunID: "run", GrantID: "original"}, []any{adapter})
	runner := &Runner{contextPreparation: adapter, rootCallbacks: &rootCallbackState{}}
	request := model.CloneRequest(model.Request{RunID: "run", Model: "model", Messages: []model.Message{{Role: model.RoleUser, Content: model.TextParts("original")}}})
	expectedPayload, err := model.MarshalRequestForInputCount(request)
	if err != nil {
		t.Fatal(err)
	}
	source := kernel.Snapshot{Run: kernel.Run{ID: "run", Revision: 1}}
	_, err = chain.Invoke(callbackscope.Bind(t.Context(), scope), plugin.RunInvocation{RunID: "run", Operation: plugin.RunStart}, func(ctx context.Context) (kernel.Snapshot, error) {
		result, e := runner.prepareSelectedContext(ctx, source, request)
		if e == nil && !reflect.DeepEqual(result, request) {
			t.Fatal("prepared request changed")
		}
		return source, e
	})
	if err != nil || !scope.Complete() || runner.rootCallbacks.unknown.Load() {
		t.Fatalf("preparation did not complete: %v", err)
	}
	if counter.calls != 1 || counter.seen.Context != window.TokenCountContext || !bytes.Equal(counter.seen.Payload, expectedPayload) {
		t.Fatalf("actual exact counter not called with original input: calls=%d context=%+v", counter.calls, counter.seen.Context)
	}
	if ports.seen != window || ports.plan.ModelWindow != window || ports.assessment.TokenCountSource != runtimecontext.CountExact ||
		ports.assessment.HardTokenEstimate != 17 || ports.assessment.HardInputTokens != 710 {
		t.Fatalf("lost full model-window/counter facts: %+v %+v", ports.seen, ports.assessment)
	}
}
