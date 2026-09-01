package workflow

import (
	"context"
	"time"

	runtimebudget "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/observability"
)

func (runner *Runner) recordTelemetry(ctx context.Context, event observability.Event) {
	if runner == nil || runner.telemetry == nil {
		return
	}
	if event.ObservedAt.IsZero() {
		event.ObservedAt = runner.runtime.Now()
	}
	recordContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	runner.telemetry.Record(recordContext, event)
}

func (runner *Runner) recordRunTelemetry(
	ctx context.Context,
	snapshot kernel.Snapshot,
	state executionState,
	phase observability.Phase,
) {
	duration := time.Duration(0)
	if phase != observability.PhaseStarted && !snapshot.Run.CreatedAt.IsZero() && !snapshot.Run.UpdatedAt.Before(snapshot.Run.CreatedAt) {
		duration = snapshot.Run.UpdatedAt.Sub(snapshot.Run.CreatedAt)
	}
	runner.recordTelemetry(ctx, observability.Event{
		Scope: observability.ScopeRun, Phase: phase, RunID: snapshot.Run.ID, RunKind: RunKind,
		Revision: snapshot.Run.Revision, Status: string(snapshot.Run.Status), ErrorCode: snapshot.Run.ErrorCode,
		ObservedAt: runner.runtime.Now(), Duration: duration, Usage: RuntimeBudget(View(state)).Usage,
	})
}

func (runner *Runner) executeObservedEffect(
	ctx context.Context,
	request EffectRequest,
) (EffectResult, error) {
	return runner.executeObservedEffectFunc(ctx, request, runner.effects.Execute)
}

func (runner *Runner) executeObservedEffectFunc(
	ctx context.Context,
	request EffectRequest,
	execute func(context.Context, EffectRequest) (EffectResult, error),
) (EffectResult, error) {
	startedAt := runner.runtime.Now()
	runner.recordTelemetry(ctx, observability.Event{
		Scope: observability.ScopeWorkflowEffect, Phase: observability.PhaseStarted,
		RunID: request.RunID, RunKind: RunKind, OperationID: request.EffectID, Operation: request.Kind,
		Attempt: request.Attempt, Compensation: request.Compensation, ObservedAt: startedAt,
	})
	result, err := execute(ctx, request)
	endedAt := runner.runtime.Now()
	phase := observability.PhaseCompleted
	errorCode := result.ErrorCode
	if err != nil {
		phase = observability.PhaseFailed
		if errorCode == "" {
			errorCode = "executor_error"
		}
	}
	runner.recordTelemetry(ctx, observability.Event{
		Scope: observability.ScopeWorkflowEffect, Phase: phase,
		RunID: request.RunID, RunKind: RunKind, OperationID: request.EffectID, Operation: request.Kind,
		Attempt: request.Attempt, Compensation: request.Compensation, ChildRunID: result.ChildRunID,
		Status: string(result.Disposition), ErrorCode: errorCode,
		ObservedAt: endedAt, Duration: endedAt.Sub(startedAt),
		Usage: runtimebudget.Usage{CostUnits: result.CostUnits},
	})
	return result, err
}
