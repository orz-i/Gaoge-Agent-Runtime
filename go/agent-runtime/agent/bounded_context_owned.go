package agent

import (
	"context"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/internal/callbackscope"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

func validBoundedContextComposition(d Dependencies) bool {
	a := d.BoundedContext
	if a == nil {
		return true
	}
	if a.manager == nil || a.ports == nil || d.PrepareModelRequest != nil || d.ManagedContextRecovery != nil || d.ContextSummary != nil || d.SummaryNativeRecovery {
		return false
	}
	count := 0
	for _, layer := range d.RunMiddleware {
		if contextLayer, ok := layer.(*BoundedContextAdapter); ok {
			if contextLayer != a {
				return false
			}
			count++
		}
	}
	return count == 1
}

// An ordinary owned Run gets a separate concrete preparation ticket. This
// neither claims managed suspension nor grants trust to generic Host callbacks.
func (r *Runner) prepareBoundedContext(ctx context.Context, source kernel.Snapshot, request model.Request) (model.Request, error) {
	a := r.boundedContext
	callCtx, ticket := callbackscope.EnterPreparation(ctx, request.RunID, a, struct {
		Source  kernel.Snapshot
		Request model.Request
	}{source, request})
	if ticket == nil {
		r.noteRootCallbackFailure(ErrInvalidRequest)
		return model.Request{}, ErrInvalidRequest
	}
	returned := false
	defer func() {
		if !returned {
			callbackscope.Aborted(ticket)
		}
	}()
	rejected := false
	prepared, err := a.prepare(callCtx, model.CloneRequest(request), &rejected)
	proved := false
	if err == nil && !validPreparedModelRequest(request, prepared) {
		err = ErrInvalidRequest
	}
	if err == nil || rejected {
		proved = callbackscope.Record(callCtx, a, "context.preparation.returned", struct {
			Source            kernel.Snapshot
			Request, Prepared model.Request
			Rejected          bool
		}{source, request, prepared, rejected})
		if !proved {
			err = ErrInvalidRequest
		}
	}
	callbackscope.Returned(ticket, err)
	returned = true
	if !proved {
		r.noteRootCallbackFailure(err)
	}
	return model.CloneRequest(prepared), err
}
