package continuation_test

import (
	"context"
	"errors"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/continuation"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

var errUncommittedTerminal = errors.New("terminal transition not durably persisted")

// Simulates feature adapters that return an optimistic terminal Snapshot even
// though a failed write left the authoritative Run running.
type terminalOutcomeResumer struct {
	runtime   *kernel.Runtime
	persisted bool
	nilError  bool
	calls     int
}

func (resumer *terminalOutcomeResumer) Resume(ctx context.Context, id string, expected uint64) (kernel.Snapshot, error) {
	resumer.calls++
	resultErr := errUncommittedTerminal
	if resumer.nilError {
		resultErr = nil
	}
	if resumer.persisted {
		current, err := resumer.runtime.Load(ctx, id)
		if err != nil {
			return kernel.Snapshot{}, err
		}
		saved, err := resumer.runtime.Apply(ctx, id, expected, kernel.Mutation{
			Status: kernel.RunStatusFailed, State: current.State,
			ErrorCode: "test.feature_failure",
		})
		if err != nil {
			return kernel.Snapshot{}, err
		}
		return saved, resultErr
	}
	return kernel.Snapshot{Run: kernel.Run{
		ID: id, Revision: expected + 1, Status: kernel.RunStatusFailed,
	}}, resultErr
}

func (resumer *terminalOutcomeResumer) ResumeToolWait(ctx context.Context, id string, expected, _ uint64) (kernel.Snapshot, error) {
	return resumer.Resume(ctx, id, expected)
}

func TestDispatcherOnlyACKsAuthoritativelyPersistedTerminalOutcomes(t *testing.T) {
	for _, test := range []struct {
		name      string
		trigger   continuation.Trigger
		persisted bool
		nilError  bool
	}{
		{name: "self-wakeup uncommitted", trigger: continuation.TriggerSegmentYielded},
		{name: "self-wakeup committed", trigger: continuation.TriggerSegmentYielded, persisted: true},
		{name: "self-wakeup uncommitted nil error", trigger: continuation.TriggerSegmentYielded, nilError: true},
		{name: "self-wakeup committed nil error", trigger: continuation.TriggerSegmentYielded, persisted: true, nilError: true},
		{name: "child-terminal uncommitted", trigger: continuation.TriggerChildTerminal},
		{name: "child-terminal committed", trigger: continuation.TriggerChildTerminal, persisted: true},
		{name: "child-terminal uncommitted nil error", trigger: continuation.TriggerChildTerminal, nilError: true},
		{name: "child-terminal committed nil error", trigger: continuation.TriggerChildTerminal, persisted: true, nilError: true},
		{name: "tool-wait uncommitted", trigger: continuation.TriggerToolResultReady},
		{name: "tool-wait committed", trigger: continuation.TriggerToolResultReady, persisted: true},
		{name: "tool-wait uncommitted nil error", trigger: continuation.TriggerToolResultReady, nilError: true},
		{name: "tool-wait committed nil error", trigger: continuation.TriggerToolResultReady, persisted: true, nilError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			runtime := newRuntime(t)
			parent := createRun(t, runtime, "terminal-proof-parent", kernel.RunKind("test.outcome"))
			resumer := &terminalOutcomeResumer{runtime: runtime, persisted: test.persisted, nilError: test.nilError}
			dispatcher, err := continuation.NewDispatcher(runtime, continuation.RegisterResumer(parent.Run.Kind, resumer))
			if err != nil {
				t.Fatal(err)
			}
			payload := continuation.Payload{
				SchemaVersion: continuation.SchemaVersion, RunID: parent.Run.ID,
				ExpectedRevision: parent.Run.Revision, Trigger: test.trigger,
				SourceRunID: "source-run", SourceRevision: 2,
			}
			if test.trigger == continuation.TriggerToolResultReady {
				payload.WaitGeneration = 1
			}
			dispatched := dispatcher.Dispatch(t.Context(), payload)
			saved, err := runtime.Load(t.Context(), parent.Run.ID)
			if err != nil || resumer.calls != 1 {
				t.Fatalf("resume attempts=%d Store err=%v", resumer.calls, err)
			}
			if test.persisted {
				if dispatched != nil || saved.Run.Status != kernel.RunStatusFailed {
					t.Fatalf("durably failed parent should ACK: result=%v stored=%+v", dispatched, saved.Run)
				}
			} else {
				want := errUncommittedTerminal
				if test.nilError {
					want = kernel.ErrConflict
				}
				if !errors.Is(dispatched, want) || saved.Run.Status != kernel.RunStatusRunning {
					t.Fatalf("uncommitted terminal Snapshot incorrectly ACKed: result=%v stored=%+v", dispatched, saved.Run)
				}
			}
		})
	}
}

type erroneousConflictResumer struct{}

func (erroneousConflictResumer) Resume(context.Context, string, uint64) (kernel.Snapshot, error) {
	return kernel.Snapshot{}, kernel.ErrConflict
}

func TestDispatcherUnchangedRevisionCASConflictRemainsRetryable(t *testing.T) {
	runtime := newRuntime(t)
	run := createRun(t, runtime, "self-wakeup-conflict", kernel.RunKind("test.outcome"))
	dispatcher, err := continuation.NewDispatcher(runtime, continuation.RegisterResumer(run.Run.Kind, erroneousConflictResumer{}))
	if err != nil {
		t.Fatal(err)
	}
	err = dispatcher.Dispatch(t.Context(), continuation.Payload{
		RunID: run.Run.ID, ExpectedRevision: run.Run.Revision, Trigger: continuation.TriggerRunReady,
		SourceRunID: run.Run.ID, SourceRevision: run.Run.Revision,
	})
	if !errors.Is(err, kernel.ErrConflict) {
		t.Fatalf("unconsumed revision-bound wakeup should NACK: %v", err)
	}
}
