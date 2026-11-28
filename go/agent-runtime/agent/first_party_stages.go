package agent

import (
	"context"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/internal/callbackscope"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

// Context and Budget are selected through their explicit, fixed stages.
func selectFirstPartyStages(d *Dependencies) error {
	if !validBoundedBudgetComposition(*d) || !validBoundedContextComposition(*d) {
		return ErrInvalidRequest
	}
	if summary := d.ContextSummary; summary != nil {
		if !validSummaryComposition(*d) {
			return ErrInvalidRequest
		}
		d.BoundedContext = summary.bounded
	}
	// A registered recovery Host is a persistence identity, not an opt-in mode.
	// A configured Context must prove its actual first-party store against it.
	if d.Runtime.HasManagedRecoveryHost() && d.BoundedContext != nil && !validManagedDependencies(*d) {
		return ErrInvalidRequest
	}
	return nil
}

func finishNativeStage(ticket *callbackscope.Ticket, err *error) {
	if failure := recover(); failure != nil {
		callbackscope.Aborted(ticket)
		panic(failure)
	}
	callbackscope.Returned(ticket, *err)
}

// The Runner now fixes this order: original Budget scope, Context hydration,
// generic Run chain/continuation, then Budget projection/settlement. Host middleware
// cannot supply a new Next for any of these first-party stages.
func (r *Runner) invokeFirstPartyRun(ctx context.Context, inv plugin.RunInvocation, originalBudget *budget.Ledger, terminal plugin.RunNext) (out kernel.Snapshot, err error) {
	b := r.boundedBudget
	if b == nil {
		return r.invokeContextRun(ctx, inv, terminal)
	}
	stageCtx, ticket := callbackscope.Enter(ctx, inv.RunID, b, inv)
	if ticket == nil {
		return kernel.Snapshot{}, ErrInvalidRequest
	}
	defer finishNativeStage(ticket, &err)
	admission, err := b.admitRun(stageCtx, inv, originalBudget)
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
	a := r.boundedContext
	if a == nil {
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
	a := r.boundedBudget
	if a == nil {
		return r.modelChain.Invoke(ctx, request, emit, terminal)
	}
	stageCtx, ticket := callbackscope.Enter(ctx, request.RunID, a, request)
	if ticket == nil {
		return model.Response{}, ErrInvalidRequest
	}
	defer finishNativeStage(ticket, &err)
	// Budget always encloses the generic chain and physical admission terminal.
	// Atomic composition rejects nonempty generic chains at construction.
	return a.Model(stageCtx, model.CloneRequest(request), emit, func(ctx context.Context, request model.Request, emit model.StreamSink) (model.Response, error) {
		return r.modelChain.Invoke(ctx, request, emit, terminal)
	})
}

func (r *Runner) invokeFirstPartyTool(ctx context.Context, inv plugin.ToolInvocation, terminal plugin.ToolNext) (out tools.ExecutionResult, err error) {
	a := r.boundedBudget
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
	return a.Tool(stageCtx, inv, func(ctx context.Context) (tools.ExecutionResult, error) {
		return r.toolChain.Invoke(ctx, inv, terminal)
	})
}
