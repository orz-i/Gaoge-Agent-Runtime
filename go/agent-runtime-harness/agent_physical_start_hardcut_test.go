package harness_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

type physicalStartCaptureModel struct{ requests []model.Request }

func (m *physicalStartCaptureModel) Generate(_ context.Context, request model.Request) (model.Response, error) {
	m.requests = append(m.requests, model.CloneRequest(request))
	return model.Response{Content: "done"}, nil
}

// A resumed admission before any physical Agent Run uses the same frozen
// request construction as first admission. Neither path gets a second tool
// loop, a changed model binding, or an extra model call on exact replay.
func TestHardcutAgentPhysicalStartEqualsPreRunRecovery(t *testing.T) {
	var initialPhysicalRequest model.Request
	for _, loseInvocationItem := range []bool{false, true} {
		name := "first-admission"
		if loseInvocationItem {
			name = "recovery-before-run"
		}
		t.Run(name, func(t *testing.T) {
			runtime, err := kernel.New(kernel.Dependencies{Store: memory.NewStore(), Clock: fixedClock{}})
			if err != nil {
				t.Fatal(err)
			}
			modelClient := &physicalStartCaptureModel{}
			loop, err := agent.NewRunner(agent.Dependencies{Runtime: runtime, Model: modelClient})
			if err != nil {
				t.Fatal(err)
			}
			backing := harness.NewMemoryStore()
			makeRunner := func(store harness.Store) *harness.Runner {
				t.Helper()
				runner, e := harness.NewRunner(harness.Dependencies{
					Runtime: runtime, Agent: loop, Store: store, Clock: fixedClock{},
				})
				if e != nil {
					t.Fatal(e)
				}
				return runner
			}
			request := testStartRequest()
			request.RequestID = "stable-physical-start"
			request.Goal = "same objective"
			request.Config.Model = "selected-model"
			request.Config.ModelOptions = json.RawMessage(`{"temperature":0}`)
			request.CallAllowance = &agent.CallAllowance{LLMCalls: 1}
			if loseInvocationItem {
				flaky := &auditInvocationFaultOnce{Store: backing}
				_, err = makeRunner(flaky).StartAgentTurn(t.Context(), request)
				if err == nil || !flaky.failed || len(modelClient.requests) != 0 {
					t.Fatalf("injection must stop before physical dispatch: err=%v injected=%t model_calls=%d", err, flaky.failed, len(modelClient.requests))
				}
			}
			runner := makeRunner(backing)
			started, err := runner.StartAgentTurn(t.Context(), request)
			if err != nil || started.Turn.Status != harness.TurnCompleted || len(modelClient.requests) != 1 {
				t.Fatalf("start/recover: err=%v status=%s model_calls=%d", err, started.Turn.Status, len(modelClient.requests))
			}
			visible := modelClient.requests[0]
			if visible.RunID == "" || visible.Model != "selected-model" ||
				string(visible.ModelOptions) != string(request.Config.ModelOptions) {
				t.Fatalf("physical request lost frozen model identity or options: %+v", visible)
			}
			if !loseInvocationItem {
				initialPhysicalRequest = model.CloneRequest(visible)
			} else if !reflect.DeepEqual(visible, initialPhysicalRequest) {
				t.Fatalf("pre-Run recovery changed physical model request: first=%+v recovered=%+v", initialPhysicalRequest, visible)
			}
			replayed, err := runner.StartAgentTurn(t.Context(), request)
			if err != nil || replayed.Turn.ID != started.Turn.ID || len(modelClient.requests) != 1 {
				t.Fatalf("exact replay re-dispatched model: err=%v calls=%d", err, len(modelClient.requests))
			}
			changed := request
			changed.Goal = "different objective"
			_, err = runner.StartAgentTurn(t.Context(), changed)
			if !errors.Is(err, harness.ErrConflict) || len(modelClient.requests) != 1 {
				t.Fatalf("changed admitted objective was accepted: err=%v model_calls=%d", err, len(modelClient.requests))
			}
		})
	}
}
