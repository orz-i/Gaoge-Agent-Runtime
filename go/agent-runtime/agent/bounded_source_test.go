package agent

import (
	"context"
	"errors"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/internal/callbackscope"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
)

func TestBoundedBudgetDenialRequiresOriginalRequestAndInvocationNonce(t *testing.T) {
	request := model.Request{RunID: "run", InvocationID: "invocation", Model: "original"}
	scope := &modelDenialScope{marker: 1}
	ctx := context.WithValue(t.Context(), modelDenialKey{}, scope)
	denied := modelPreDispatchDenied{cause: ErrInvalidRequest, scope: scope, request: model.CloneRequest(request)}
	if !modelDeniedForRequest(ctx, request, denied) {
		t.Fatal("original private denial rejected")
	}
	for _, field := range []string{"run", "invocation", "model"} {
		candidate := model.CloneRequest(request)
		switch field {
		case "run":
			candidate.RunID = "other"
		case "invocation":
			candidate.InvocationID = "other"
		case "model":
			candidate.Model = "other"
		}
		if modelDeniedForRequest(ctx, candidate, denied) {
			t.Fatalf("foreign %s accepted", field)
		}
	}
	if modelDeniedForRequest(context.WithValue(ctx, modelDenialKey{}, &modelDenialScope{marker: 1}), request, denied) {
		t.Fatal("old call nonce replayed")
	}
	if modelDeniedForRequest(ctx, request, errors.New(denied.Error())) {
		t.Fatal("public error string forged authority")
	}
}
func TestAdmissionBarrierOwnsOnlyChannelWaitAndReturnedNext(t *testing.T) {
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	close(release)
	barrier := NewModelAdmissionBarrier(entered, release)
	scope := callbackscope.New(callbackscope.Binding{RunID: "run", GrantID: "grant", Operation: "resume"}, []any{barrier})
	chain, err := plugin.NewModelChain(barrier)
	if err != nil {
		t.Fatal(err)
	}
	denied := errors.New("downstream admission denied")
	calls := 0
	_, err = chain.Invoke(callbackscope.Bind(t.Context(), scope), model.Request{RunID: "run", InvocationID: "original"}, nil, func(context.Context, model.Request, model.StreamSink) (model.Response, error) {
		calls++
		if scope.Complete() {
			t.Fatal("live Next completed")
		}
		return model.Response{}, denied
	})
	if !errors.Is(err, denied) || calls != 1 || !scope.Complete() {
		t.Fatal("channel-only own completion not distinguished")
	}
	if IsModelPreDispatchDenied(err) {
		t.Fatal("barrier invented Provider no-send")
	}
	select {
	case <-entered:
	default:
		t.Fatal("actual barrier not entered")
	}
}
