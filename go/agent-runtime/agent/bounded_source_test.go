package agent

import (
	"context"
	"errors"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/internal/callbackscope"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

func TestBoundedBudgetDenialRequiresOriginalRequestAndInvocationNonce(t *testing.T) {
	request := model.Request{RunID: "run", InvocationID: "invocation", Model: "original"}
	scope := &modelDenialScope{marker: 1}
	ctx := callbackscope.BindModelCall(t.Context(), &modelCallScope{denial: scope})
	denied := modelPreDispatchDenied{cause: ErrInvalidRequest, scope: scope, original: model.CloneRequest(request), candidate: model.CloneRequest(request)}
	if !modelDeniedForRequest(ctx, request, denied) {
		t.Fatal("original private denial rejected")
	}
	normalized := model.CloneRequest(request)
	if !modelDeniedForRequest(ctx, normalized, denied) {
		t.Fatal("nil/empty normalized request rejected")
	}
	// A changed candidate is not the original frozen Provider request. Only
	// the Agent terminal's private proof of that original request can restore
	// its reservation, not the altered candidate by itself.
	mutated := denied
	mutated.candidate = model.CloneRequest(request)
	mutated.candidate.Model = "unapproved-model"
	if !modelDeniedForRequest(ctx, request, mutated) || modelDeniedForRequest(ctx, mutated.candidate, mutated) {
		t.Fatal("candidate drift was mistaken for original dispatch authority")
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
	if modelDeniedForRequest(callbackscope.BindModelCall(ctx, &modelCallScope{denial: &modelDenialScope{marker: 1}}), request, denied) {
		t.Fatal("old call nonce replayed")
	}
	if modelDeniedForRequest(ctx, request, errors.New(denied.Error())) {
		t.Fatal("public error string forged authority")
	}
}
func TestAdmissionBarrierOwnsOnlyChannelWaitAndReturnedProvider(t *testing.T) {
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	close(release)
	barrier := NewModelAdmissionBarrier(entered, release)
	scope := callbackscope.New(callbackscope.Binding{RunID: "run", GrantID: "grant", Operation: "resume"}, []any{barrier})
	runner := &Runner{modelAdmissionBarrier: barrier}
	denied := errors.New("downstream admission denied")
	calls := 0
	_, err := runner.invokeAdmittedModel(callbackscope.Bind(t.Context(), scope), model.Request{RunID: "run", InvocationID: "original"}, nil, func(context.Context, model.Request, model.StreamSink) (model.Response, error) {
		calls++
		if scope.Complete() {
			t.Fatal("live Provider completed")
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
