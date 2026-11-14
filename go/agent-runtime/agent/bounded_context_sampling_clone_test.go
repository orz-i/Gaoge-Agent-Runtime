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
	checkpoint := runtimecontext.Checkpoint{Window: runtimecontext.Window{Entries: []runtimecontext.Entry{{ID: "entry", Message: message}}},
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
		{"guidance", func(p *SamplingPlan) { p.Guidance[0].Content[0].Text = "changed" }},
		{"data", func(p *SamplingPlan) { p.Data[0] = '!' }},
	} {
		t.Run(mutate.name, func(t *testing.T) {
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
