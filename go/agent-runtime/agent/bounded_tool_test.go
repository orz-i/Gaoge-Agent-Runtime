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
func TestBoundedToolTimelineNeedsActualStartedNextAndFinished(t *testing.T) {
	for _, mode := range []string{"complete", "post-next-failure", "foreign-source"} {
		t.Run(mode, func(t *testing.T) {
			ports := &boundedTimelineFixture{failFinish: mode == "post-next-failure", foreign: mode == "foreign-source"}
			adapter, err := NewBoundedToolTimelineAdapter(ports)
			if err != nil {
				t.Fatal(err)
			}
			scope := callbackscope.New(callbackscope.Binding{RunID: "run", GrantID: "grant", Operation: "resume"}, []any{adapter})
			chain, err := plugin.NewToolChain(adapter)
			if err != nil {
				t.Fatal(err)
			}
			inv := plugin.ToolInvocation{Run: kernel.Run{ID: "run"}, Request: tools.ExecutionRequest{RunID: "run", Call: tools.Call{ID: "call"}}}
			called := 0
			business := errors.New("downstream business error")
			_, err = chain.Invoke(callbackscope.Bind(t.Context(), scope), inv, func(context.Context) (tools.ExecutionResult, error) {
				called++
				if scope.Complete() {
					t.Fatal("Next still live")
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

type opaqueToolReceiptLayer struct{}

func (opaqueToolReceiptLayer) Name() string { return "opaque_tool_receipt_layer" }
func (opaqueToolReceiptLayer) Tool(ctx context.Context, _ plugin.ToolInvocation, next plugin.ToolNext) (tools.ExecutionResult, error) {
	return next(ctx)
}
func TestOrdinaryToolLayerCannotBorrowOriginalNoEffectReceipt(t *testing.T) {
	scope := callbackscope.New(callbackscope.Binding{RunID: "run", GrantID: "grant", Operation: "resume"}, nil)
	chain, err := plugin.NewToolChain(opaqueToolReceiptLayer{})
	if err != nil {
		t.Fatal(err)
	}
	inv := plugin.ToolInvocation{Run: kernel.Run{ID: "run"}, Request: tools.ExecutionRequest{RunID: "run", Call: tools.Call{ID: "original-call"}}}
	result, err := chain.Invoke(callbackscope.Bind(t.Context(), scope), inv, func(context.Context) (tools.ExecutionResult, error) {
		return tools.RecoverableNoEffectResult("original-call", tools.NewRecoverableCallError("blocked", "not allowed", nil))
	})
	if !tools.ValidRecoverableNoEffectReceipt(result, err, "original-call") {
		t.Fatal("fixture lacks exact no-effect receipt")
	}
	if scope.Complete() {
		t.Fatal("Tool outcome certified opaque Host callback")
	}
	if validBoundedToolReceipt(result, err, "foreign-call") {
		t.Fatal("foreign Tool source accepted")
	}
}
