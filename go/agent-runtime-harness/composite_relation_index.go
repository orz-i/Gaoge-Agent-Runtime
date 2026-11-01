package harness

import (
	"context"
	"errors"
	"fmt"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/runrelation"
)

// A composite Feature can own physical descendants without any Harness child
// Invocation. An empty relation index, a coincidentally present local Run or a
// matching registry pointer is not a physical execution-owner receipt.
type localCompositeExecutionOwner interface {
	OwnsKernelRuntime(*kernel.Runtime) bool
	RecordsRelationsTo(runrelation.Recorder) bool
}

// requireCompositeRelationIndex proves the original composite Invocation's
// execution route, Kernel residence and indexed child ownership BEFORE any
// local cancellation callback, terminal projection or descendant traversal.
// Host-defined Feature wrappers must attest BOTH facts; an absent optional
// method is never permission to report successful physical cleanup.
func (runner *Runner) requireCompositeRelationIndex(ctx context.Context, runID string) error {
	original, err := runner.store.GetInvocationByExecutionRefID(ctx, runID)
	if errors.Is(err, ErrNotFound) {
		// Raw Feature edges without a Harness Invocation have a separate
		// fixed-route/RunRelation proof in verifyLocalRelationRun.
		return nil
	}
	if err != nil {
		return err
	}
	var feature any
	switch original.ExecutionClass {
	case ExecutionTeam:
		feature = runner.teams
	case ExecutionGroupChat:
		feature = runner.groupChats
	case ExecutionWorkflow:
		feature = runner.workflows
	case ExecutionAgent, ExecutionApplication:
		return nil
	default:
		return ErrConflict
	}
	unverified := func() error {
		return fmt.Errorf("composite Run %q has no verified original local execution owner/child index: %w",
			runID, ErrRuntimeCancellationUnverified)
	}
	if runner.relationReader != nil {
		// An incoming raw Feature relation takes precedence over an
		// unrelated Harness Invocation using the same physical Run ID.
		// The actual raw route is independently checked before a callback.
		relation, readErr := runner.relationReader.GetByChild(ctx, runID)
		if readErr != nil && !errors.Is(readErr, runrelation.ErrNotFound) {
			return errors.Join(unverified(), readErr)
		}
		if readErr == nil && (relation.Kind == runrelation.KindTeamMember ||
			relation.Kind == runrelation.KindGroupChatSpeaker ||
			relation.Kind == runrelation.KindWorkflowEffect) {
			return nil
		}
	}
	// A known native or explicitly attested Host adapter owns both the
	// original Kernel and its indexed child graph. Do not add an extra
	// physical Store read here: it changes pre-Store cancellation no-effect
	// boundaries and can race the Kernel's own revision/CAS probe.
	owner, ok := feature.(localCompositeExecutionOwner)
	if runner.relationReader != nil && ok &&
		owner.OwnsKernelRuntime(runner.runtime) &&
		owner.RecordsRelationsTo(runner.relationReader) {
		return nil
	}
	_, loadErr := runner.runtime.Load(ctx, runID)
	if errors.Is(loadErr, kernel.ErrNotFound) {
		// A definitively rejected physical Start did not enter any
		// execution route. A failed or missing local Run alone does not
		// prove this: an acknowledged remote Start can have no local Run.
		items, itemErr := listAllItems(ctx, runner.store, original.TurnID)
		if itemErr != nil {
			return errors.Join(unverified(), itemErr)
		}
		if (original.Status == InvocationAccepted &&
			!acceptedChildPhysicalEffectPossible(items, original)) ||
			!compositeRunMayHaveDescendants(items, original) {
			return nil
		}
		return unverified()
	}
	if loadErr != nil {
		return errors.Join(unverified(), loadErr)
	}
	// An existing local Run still cannot stand in for an unverified
	// external route or a Feature that wrote to another registry.
	return unverified()
}
