package agent

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/internal/callbackscope"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

type boundedTimelineFixture struct {
	events              []string
	failFinish, foreign bool
}

func (p *boundedTimelineFixture) ResolveToolTimeline(_ context.Context, inv plugin.ToolInvocation) (BoundedToolTimelineScope, error) {
	id := inv.Run.ID
	if p.foreign {
		id = "foreign"
	}
	return BoundedToolTimelineScope{RunID: id, TurnID: "turn", InvocationID: "inv", Active: true, SourceBinding: json.RawMessage(`{"original":"scope"}`)}, nil
}
func (p *boundedTimelineFixture) AppendToolTimeline(_ context.Context, _ BoundedToolTimelineScope, _ plugin.ToolInvocation, _ tools.ExecutionResult, status, parent string) (string, error) {
	p.events = append(p.events, status+":"+parent)
	if p.failFinish && status != "started" {
		return "", errors.New("actual append failed")
	}
	return "saved-" + status, nil
}
func TestBoundedToolTimelineNeedsActualStartedExecutorAndFinished(t *testing.T) {
	for _, mode := range []string{"complete", "finish-append-failure", "foreign-source"} {
		t.Run(mode, func(t *testing.T) {
			ports := &boundedTimelineFixture{failFinish: mode == "finish-append-failure", foreign: mode == "foreign-source"}
			adapter, err := NewBoundedToolTimelineAdapter(ports)
			if err != nil {
				t.Fatal(err)
			}
			scope := callbackscope.New(callbackscope.Binding{RunID: "run", GrantID: "grant", Operation: "resume"}, []any{adapter})
			runner := &Runner{toolTimeline: adapter}
			inv := plugin.ToolInvocation{Run: kernel.Run{ID: "run"}, Request: tools.ExecutionRequest{RunID: "run", Call: tools.Call{ID: "call"}}}
			called := 0
			business := errors.New("downstream business error")
			_, err = runner.invokeProjectedTool(callbackscope.Bind(t.Context(), scope), inv, func(context.Context) (tools.ExecutionResult, error) {
				called++
				if scope.Complete() {
					t.Fatal("executor still live")
				}
				return tools.ExecutionResult{}, business
			})
			if mode == "foreign-source" {
				if !errors.Is(err, ErrInvalidRequest) || called != 0 || len(ports.events) != 0 || scope.Complete() {
					t.Fatal("foreign source admitted")
				}
				return
			}
			if !errors.Is(err, business) || called != 1 || len(ports.events) != 2 || ports.events[0] != "started:" || ports.events[1] != "failed:saved-started" {
				t.Fatalf("actual lifecycle lost: %v %v", ports.events, err)
			}
			if scope.Complete() != (mode == "complete") {
				t.Fatal("Host append failure treated as own completion")
			}
		})
	}
}

func TestToolReceiptCannotBorrowForeignCallOrHostCompletion(t *testing.T) {
	scope := callbackscope.New(callbackscope.Binding{RunID: "run", GrantID: "grant", Operation: "resume"}, []any{inputReadStage})
	_, ticket := callbackscope.Enter(callbackscope.Bind(t.Context(), scope), "run", inputReadStage, "original input read")
	if ticket == nil {
		t.Fatal("input stage did not enter")
	}
	result, err := tools.RecoverableNoEffectResult("original-call", tools.NewRecoverableCallError("blocked", "not allowed", nil))
	if !tools.ValidRecoverableNoEffectReceipt(result, err, "original-call") {
		t.Fatal("fixture lacks exact no-effect receipt")
	}
	if scope.Complete() {
		t.Fatal("Tool outcome certified unrelated Host callback")
	}
	if terminal, _ := qualifyToolReturn(result, err, "foreign-call"); terminal {
		t.Fatal("foreign Tool source accepted")
	}
}
