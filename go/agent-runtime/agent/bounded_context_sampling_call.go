package agent

import (
	"context"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/internal/callbackscope"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

func (r *Runner) prepareSelectedContext(ctx context.Context, source kernel.Snapshot, request model.Request) (prepared model.Request, err error) {
	a := r.contextPreparation
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
	prepared, disposition, err := a.prepareSampling(callCtx, model.CloneRequest(request), cloneSampling(source))
	if disposition == samplingPrepared && (err != nil || !validPreparedModelRequest(request, prepared)) {
		disposition = samplingUnknown
		err = ErrInvalidRequest
	}
	if disposition == samplingUnknown && err == nil {
		err = ErrInvalidRequest
	}
	proved := false
	if disposition == samplingPrepared || disposition == samplingHardRejected {
		proved = callbackscope.Record(callCtx, a, "context.sampling.returned", struct {
			Source            kernel.Snapshot
			Request, Prepared model.Request
			Disposition       samplingDisposition
		}{source, request, prepared, disposition})
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
