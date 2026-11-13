package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

type issue107FrozenStartTestModel struct{}

func (issue107FrozenStartTestModel) Generate(context.Context, model.Request) (model.Response, error) {
	return model.Response{Content: "done"}, nil
}

func TestIssue107FrozenAgentStartRequestIdentityAndLimits(t *testing.T) {
	runtime, err := kernel.New(kernel.Dependencies{Store: memory.NewStore()})
	if err != nil {
		t.Fatal(err)
	}
	runner, err := NewRunner(Dependencies{Runtime: runtime, Model: issue107FrozenStartTestModel{}})
	if err != nil {
		t.Fatal(err)
	}
	request := StartRequest{
		ID: "issue107-frozen", RequestID: "original-effect", Goal: "precisely bounded task",
		Model: "host-model", ModelOptions: json.RawMessage(`{"temperature":0.1}`),
		Actor:  kernel.ActorRef{TenantID: "tenant", ActorID: "actor"},
		Thread: kernel.ThreadRef{Kind: "conversation", ID: "thread"},
		Limits: Limits{MaxLLMCalls: 2, MaxToolCalls: 3},
	}
	child, err := runner.PrepareRun(t.Context(), request)
	if err != nil || !runner.VerifyFrozenStartRequest(child, request) {
		t.Fatalf("original immutable admission was rejected: %+v %v", child.Run, err)
	}
	for name, change := range map[string]func(*StartRequest){
		"widen LLM bound":            func(r *StartRequest) { r.Limits.MaxLLMCalls = 8 },
		"widen Tool bound":           func(r *StartRequest) { r.Limits.MaxToolCalls = 20 },
		"change effective model":     func(r *StartRequest) { r.Model = "foreign-model" },
		"change model options":       func(r *StartRequest) { r.ModelOptions = json.RawMessage(`{"temperature":0.2}`) },
		"change requested tool keys": func(r *StartRequest) { r.ToolKeys = []string{"foreign-tool"} },
		"change effect ID":           func(r *StartRequest) { r.RequestID = "other-effect" },
		"change run ID":              func(r *StartRequest) { r.ID = "other-run" },
		"change tenant":              func(r *StartRequest) { r.Actor.TenantID = "foreign" },
		"change thread":              func(r *StartRequest) { r.Thread.ID = "other" },
		"change goal":                func(r *StartRequest) { r.Goal = "another goal" },
	} {
		t.Run(name, func(t *testing.T) {
			forged := request
			change(&forged)
			if runner.VerifyFrozenStartRequest(child, forged) {
				t.Fatal("same-ID or lookalike AgentTask passed a changed frozen admission")
			}
		})
	}
	state, err := decodeState(child.State)
	if err != nil {
		t.Fatal(err)
	}
	state.Budget.Limits.MaxLLMCalls = 6
	changed := child
	changed.State, err = encodeState(state)
	if err != nil {
		t.Fatal(err)
	}
	if runner.VerifyFrozenStartRequest(changed, request) {
		t.Fatal("same-ID child with widened persisted LLM limit was accepted")
	}
	state.Budget.Limits = childBudgetLimits(t, child)
	state.Messages[0].Content = model.TextParts("replaced task body")
	changed.State, err = encodeState(state)
	if err != nil {
		t.Fatal(err)
	}
	if runner.VerifyFrozenStartRequest(changed, request) {
		t.Fatal("same-ID child with rewritten initial transcript was accepted")
	}
	duplicate := request
	duplicate.ID = "issue107-duplicate-options"
	duplicate.ModelOptions = json.RawMessage(`{"temperature":0.1,"temperature":0.9}`)
	dupChild, dupErr := runner.PrepareRun(t.Context(), duplicate)
	if dupErr == nil && runner.VerifyFrozenStartRequest(dupChild, duplicate) {
		t.Fatal("ambiguous duplicate option keys became trusted frozen Host policy")
	}
}

func childBudgetLimits(t *testing.T, snapshot kernel.Snapshot) Limits {
	t.Helper()
	state, err := decodeState(snapshot.State)
	if err != nil {
		t.Fatal(err)
	}
	return state.Budget.Limits
}
