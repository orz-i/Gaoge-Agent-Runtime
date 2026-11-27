package agent

import (
	"context"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/internal/callbackscope"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

// A complete concrete Budget owner has the historical exact order
// Budget -> Context (if selected), plus one Model and one Tool adapter.
// Partial/opaque chains retain their actual ordering and qualifications.
func completeLegacyBudget(d Dependencies, ctx *BoundedContextAdapter) *BoundedBudgetAdapter {
	b := selectedSamplingBudget(d)
	if b == nil || selectedToolBudget(d) != b || len(d.ModelMiddleware) != 1 || len(d.ToolMiddleware) != 1 {
		return nil
	}
	if ctx == nil && len(d.RunMiddleware) == 1 && d.RunMiddleware[0] == b ||
		ctx != nil && len(d.RunMiddleware) == 2 && d.RunMiddleware[0] == b && d.RunMiddleware[1] == ctx {
		return b
	}
	return nil
}

func contextOnlyRun(d Dependencies, a *BoundedContextAdapter) bool {
	return len(d.RunMiddleware) == 0 || len(d.RunMiddleware) == 1 && d.RunMiddleware[0] == a
}

// No generic participant registry or chain synthesis. The standard
// composition and its exact legacy spelling select the SAME concrete stages.
func selectFirstPartyStages(d *Dependencies) (*BoundedContextAdapter, *BoundedBudgetAdapter, error) {
	if !validBoundedBudgetComposition(*d) || !validBoundedContextComposition(*d) {
		return nil, nil, ErrInvalidRequest
	}
	if summary := d.ContextSummary; summary != nil {
		if !validSummaryComposition(*d) {
			return nil, nil, ErrInvalidRequest
		}
		d.BoundedContext = summary.bounded
	}
	b := d.BoundedBudget
	if b == nil {
		b = completeLegacyBudget(*d, d.BoundedContext)
		if b != nil {
			d.BoundedBudget = b
			d.RunMiddleware, d.ModelMiddleware, d.ToolMiddleware = nil, nil, nil
		}
	}
	var a *BoundedContextAdapter
	if d.BoundedContext != nil && contextOnlyRun(*d, d.BoundedContext) {
		a = d.BoundedContext
		d.RunMiddleware = nil
	}
	if d.ManagedRecovery && (a == nil || !validManagedDependencies(*d)) {
		return nil, nil, ErrInvalidRequest
	}
	return a, b, nil
}

func finishNativeStage(ticket *callbackscope.Ticket, err *error) {
	if failure := recover(); failure != nil {
		callbackscope.Aborted(ticket)
		panic(failure)
	}
	callbackscope.Returned(ticket, *err)
}

// The Runner now fixes this order: original Budget scope, Context hydration,
// actual Run continuation, then Budget projection/settlement. Host middleware
// cannot supply a new Next for any of these first-party stages.
func (r *Runner) invokeFirstPartyRun(ctx context.Context, inv plugin.RunInvocation, terminal plugin.RunNext) (out kernel.Snapshot, err error) {
	b := r.nativeBudget
	if b == nil {
		return r.invokeContextRun(ctx, inv, terminal)
	}
	stageCtx, ticket := callbackscope.Enter(ctx, inv.RunID, b, inv)
	if ticket == nil {
		return kernel.Snapshot{}, ErrInvalidRequest
	}
	defer finishNativeStage(ticket, &err)
	admission, err := b.admitRun(stageCtx, inv)
	if err != nil {
		return kernel.Snapshot{}, err
	}
	if admission.projectionErr == nil && admission.ledger.ID != "" {
		callbackscope.Operation(stageCtx, b, "managed.budget.admitted", admission.ledger)
	}
	out, err = r.invokeContextRun(stageCtx, inv, terminal)
	return b.finishRun(stageCtx, inv, admission, out, err, r.atomicBudget() == b)
}

func (r *Runner) invokeContextRun(ctx context.Context, inv plugin.RunInvocation, terminal plugin.RunNext) (out kernel.Snapshot, err error) {
	a := r.nativeContext
	if a == nil {
		if r.nativeBudget != nil {
			return terminal(ctx) // complete native topology has no Host Run chain
		}
		return r.runChain.Invoke(ctx, inv, terminal)
	}
	stageCtx, ticket := callbackscope.Enter(ctx, inv.RunID, a, inv)
	if ticket == nil {
		return kernel.Snapshot{}, ErrInvalidRequest
	}
	defer finishNativeStage(ticket, &err)
	hydrated, source, err := a.hydrate(stageCtx, inv)
	if err != nil {
		return kernel.Snapshot{}, err
	}
	// Revalidate the exact source used by the newly enabled preparation
	// owner. The pre-PARK read cannot authorize a later substituted Context.
	if frame, _ := callbackscope.RunCall(stageCtx).(*nativeRunAdmission); frame != nil && frame.managedObligations != nil && frame.managedObligations.Phase == "prepare" {
		snapshot, e := r.runtime.Load(stageCtx, inv.RunID)
		if e != nil {
			return kernel.Snapshot{}, e
		}
		state, e := decodeState(snapshot.State)
		if e != nil {
			return kernel.Snapshot{}, e
		}
		current, e := r.readManagedPreparationContext(stageCtx, frame.managedHost, frame.managedObligations.PreparationContext, state)
		if e != nil {
			return kernel.Snapshot{}, e
		}
		if source.Checkpoint == nil || source.Checkpoint.ID != current.CheckpointID || kernel.ManagedDigest(source.Checkpoint) != current.CheckpointDigest {
			return kernel.Snapshot{}, ErrInvalidRequest
		}
	}
	out, err = terminal(callbackscope.Inherit(hydrated, stageCtx))
	a.finishHydration(stageCtx, source)
	return out, err
}

func (r *Runner) invokeFirstPartyModel(ctx context.Context, request model.Request, emit model.StreamSink, terminal plugin.ModelNext) (out model.Response, err error) {
	a := r.nativeBudget
	if a == nil {
		return r.modelChain.Invoke(ctx, request, emit, terminal)
	}
	stageCtx, ticket := callbackscope.Enter(ctx, request.RunID, a, request)
	if ticket == nil {
		return model.Response{}, ErrInvalidRequest
	}
	defer finishNativeStage(ticket, &err)
	// The concrete Budget operation is the only selected physical owner.
	// It calls the existing provider admission terminal synchronously.
	return a.Model(stageCtx, model.CloneRequest(request), emit, terminal)
}

func (r *Runner) invokeFirstPartyTool(ctx context.Context, inv plugin.ToolInvocation, terminal plugin.ToolNext) (out tools.ExecutionResult, err error) {
	a := r.nativeBudget
	if a == nil {
		return r.toolChain.Invoke(ctx, inv, terminal)
	}
	stageCtx, ticket := callbackscope.EnterTool(ctx, inv.Run.ID, a, inv)
	if ticket == nil {
		return tools.ExecutionResult{}, ErrInvalidRequest
	}
	defer finishNativeStage(ticket, &err)
	// Keep the exact original Tool authority and budget receipt ticket.
	inv.Definition = tools.CloneDefinition(inv.Definition)
	inv.Request = tools.CloneExecutionRequest(inv.Request)
	return a.Tool(stageCtx, inv, terminal)
}
