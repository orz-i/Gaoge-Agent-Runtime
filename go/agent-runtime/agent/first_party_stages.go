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

// Private continuations connect only the fixed Runtime stages. They are not
// registration points and never cross a Host extension boundary.
type runContinuation func(context.Context) (kernel.Snapshot, error)
type modelCall func(context.Context, model.Request, model.StreamSink) (model.Response, error)
type toolCall func(context.Context) (tools.ExecutionResult, error)

// Original Budget scope, Context hydration, native operation, then settlement.
func (r *Runner) invokeFirstPartyRun(ctx context.Context, inv plugin.RunInvocation, originalBudget *budget.Ledger, terminal runContinuation) (out kernel.Snapshot, err error) {
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

func (r *Runner) invokeContextRun(ctx context.Context, inv plugin.RunInvocation, terminal runContinuation) (out kernel.Snapshot, err error) {
	a := r.boundedContext
	if a == nil {
		return terminal(ctx)
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

func (r *Runner) invokeFirstPartyModel(ctx context.Context, request model.Request, emit model.StreamSink, terminal modelCall) (out model.Response, err error) {
	a := r.boundedBudget
	if a == nil {
		return r.invokeAdmittedModel(ctx, request, emit, terminal)
	}
	stageCtx, ticket := callbackscope.Enter(ctx, request.RunID, a, request)
	if ticket == nil {
		return model.Response{}, ErrInvalidRequest
	}
	defer finishNativeStage(ticket, &err)
	return a.Model(stageCtx, model.CloneRequest(request), emit, func(ctx context.Context, request model.Request, emit model.StreamSink) (model.Response, error) {
		return r.invokeAdmittedModel(ctx, request, emit, terminal)
	})
}

func (r *Runner) invokeAdmittedModel(ctx context.Context, request model.Request, emit model.StreamSink, terminal modelCall) (out model.Response, err error) {
	b := r.modelAdmissionBarrier
	if b == nil {
		return terminal(ctx, request, emit)
	}
	stageCtx, ticket := callbackscope.Enter(ctx, request.RunID, b, request)
	defer finishNativeStage(ticket, &err)
	if err = b.wait(stageCtx); err != nil {
		return model.Response{}, err
	}
	out, err = terminal(stageCtx, request, emit)
	callbackscope.Record(stageCtx, b, "model.admission_barrier.returned", request)
	return out, err
}

func (r *Runner) invokeFirstPartyTool(ctx context.Context, inv plugin.ToolInvocation, terminal toolCall) (out tools.ExecutionResult, err error) {
	a := r.boundedBudget
	if a == nil {
		return r.invokeProjectedTool(ctx, inv, terminal)
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
		return r.invokeProjectedTool(ctx, inv, terminal)
	})
}

func (r *Runner) invokeProjectedTool(ctx context.Context, inv plugin.ToolInvocation, terminal toolCall) (out tools.ExecutionResult, err error) {
	a := r.toolTimeline
	if a == nil {
		return terminal(ctx)
	}
	stageCtx, ticket := callbackscope.EnterTool(ctx, inv.Run.ID, a, inv)
	defer finishNativeStage(ticket, &err)
	scope, started, err := a.start(stageCtx, inv)
	if err != nil {
		return tools.ExecutionResult{}, err
	}
	out, err = terminal(stageCtx)
	return a.finish(stageCtx, scope, started, inv, out, err)
}
