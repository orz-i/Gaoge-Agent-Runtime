package agent

import (
	"context"
	"errors"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/internal/callbackscope"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

// No-send classification does not grant ledger mutation authority. The
// original request and commit proof are checked at the native Store boundary.
func TestNativeModelDenialIsNotProviderError(t *testing.T) {
	denied := modelPreDispatchDenied{cause: ErrInvalidRequest}
	if !IsModelPreDispatchDenied(denied) || !errors.Is(denied, ErrInvalidRequest) {
		t.Fatal("original admission denial lost")
	}
	if IsModelPreDispatchDenied(errors.New(denied.Error())) {
		t.Fatal("provider error forged admission denial")
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
