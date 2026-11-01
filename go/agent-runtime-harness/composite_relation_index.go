package harness

import (
	"context"
	"errors"
	"fmt"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/runrelation"
)

// Composite Features can start physical Runs without Harness child Invocations.
// An empty ListChildren result is not proof of no descendants when Harness
// has no index, or a built-in composite Feature records to a different index.
// Resolve the original Invocation identity, not a same-ID local Run's Kind:
// a foreign local twin cannot become the owner of a remote Handoff or raw
// Feature edge just because it happens to be a Team/Workflow Kernel Run.
func (runner *Runner) requireCompositeRelationIndex(ctx context.Context, runID string) error {
	original, err := runner.store.GetInvocationByExecutionRefID(ctx, runID)
	if errors.Is(err, ErrNotFound) {
		// Raw Feature children without a Harness Invocation keep their
		// separate RunRelation/route ownership proof.
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
		return ErrConflict // Unknown persisted execution class has no owner proof.
	}
	unverified := func() error {
		return fmt.Errorf("composite Run %q has no verified child relation index: %w",
			runID, ErrRuntimeCancellationUnverified)
	}
	if runner.relationReader == nil {
		// A definitively never-created Run (e.g. a proven no-effect
		// Feature Start) has no physical composite descendants. All
		// locally existing composite Runs require an index.
		if _, loadErr := runner.runtime.Load(ctx, runID); errors.Is(loadErr, kernel.ErrNotFound) {
			return nil
		} else if loadErr != nil {
			return loadErr
		}
		return unverified()
	}
	if recorder, ok := feature.(interface {
		RecordsRelationsTo(runrelation.Recorder) bool
	}); ok && !recorder.RecordsRelationsTo(runner.relationReader) {
		// A remote raw Feature child may collide with an unrelated Harness
		// composite Invocation in the same or another Turn. Its actual
		// incoming Team/GroupChat/Workflow edge wins over the local twin;
		// the separate route-proof path remains responsible for cleanup.
		relation, readErr := runner.relationReader.GetByChild(ctx, runID)
		if readErr == nil && (relation.Kind == runrelation.KindTeamMember ||
			relation.Kind == runrelation.KindGroupChatSpeaker ||
			relation.Kind == runrelation.KindWorkflowEffect) {
			return nil
		}
		if readErr != nil && !errors.Is(readErr, runrelation.ErrNotFound) {
			return errors.Join(unverified(), readErr)
		}
		// Built-in features attest the exact recorder instance. Other
		// Host-composed Feature adapters remain responsible for their
		// established RunRelation contract.
		return unverified()
	}
	return nil
}
