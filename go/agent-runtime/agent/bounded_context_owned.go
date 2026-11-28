package agent

import (
	"context"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/internal/callbackscope"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

func validBoundedContextComposition(d Dependencies) bool {
	a := d.BoundedContext
	return a == nil || a.manager != nil && a.ports != nil && d.PrepareModelRequest == nil &&
		len(d.RunMiddleware) == 0 && (d.ContextSummary == nil || d.ContextSummary.bounded == a)
}

// A private, source-bound preparation rejection is not a provider
// classification. Host callbacks cannot certify shared ledger exhaustion.
type provenCandidateRejection struct {
	runID    string
	revision uint64
	code     string
	cause    error
}

func (e provenCandidateRejection) Error() string { return e.cause.Error() }
func (e provenCandidateRejection) Unwrap() error { return e.cause }

// An ordinary owned Run gets a separate concrete preparation ticket. This
// neither claims managed suspension nor grants trust to generic Host callbacks.
func (r *Runner) prepareBoundedContext(ctx context.Context, source kernel.Snapshot, request model.Request) (model.Request, error) {
	a := r.boundedContext
	// A managed transfer must retain the exact original hydrated frame's
	// preparation evidence. The actual preparation and sampling selection
	// remain the same concrete operation as the ordinary native path.
	if _, bound := r.managedOriginalDispatch(ctx, source.Run.ID); r.managedRecovery && (bound || r.managedObligationSource != nil) {
		prepared, err := a.prepareManaged(ctx, model.CloneRequest(request))
		r.noteRootCallbackFailure(err)
		if err == nil && !validPreparedModelRequest(request, prepared) {
			err = ErrInvalidRequest
		}
		return model.CloneRequest(prepared), err
	}
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
	var rejected candidateRejection
	prepared, err := a.prepare(callCtx, model.CloneRequest(request), &rejected)
	proved := false
	if err == nil && !validPreparedModelRequest(request, prepared) {
		err = ErrInvalidRequest
	}
	if err == nil || rejected.cause != nil {
		proved = callbackscope.Record(callCtx, a, "context.preparation.returned", struct {
			Source            kernel.Snapshot
			Request, Prepared model.Request
			Rejection         candidateRejection
		}{source, request, prepared, rejected})
		if !proved {
			err = ErrInvalidRequest
		} else if rejected.cause != nil {
			// Only the concrete no-dispatch decision can enter the terminal failure path.
			if rejected.Code == "context.input_budget_exceeded" {
				err = model.NewClassifiedError(rejected.cause, rejected.Code)
			} else {
				err = provenCandidateRejection{runID: source.Run.ID, revision: source.Run.Revision,
					code: rejected.Code, cause: rejected.cause}
			}
		}
	}
	callbackscope.Returned(ticket, err)
	returned = true
	if !proved {
		r.noteRootCallbackFailure(err)
	}
	return model.CloneRequest(prepared), err
}
