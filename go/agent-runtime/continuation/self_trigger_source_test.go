package continuation_test

import (
	"errors"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/continuation"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

// A continuation candidate is not permission to execute an unrelated Run.
// The projector always emits RunID/Revision as self-trigger source; reject
// forged or misrouted queue payloads before invoking any Feature Resumer.
func TestSelfTriggerCannotBorrowAnotherRunOrRevision(t *testing.T) {
	for _, test := range []struct {
		name           string
		trigger        continuation.Trigger
		sourceID       string
		sourceRevision uint64
	}{
		{name: "wrong source run", trigger: continuation.TriggerRunReady, sourceID: "another-run", sourceRevision: 1},
		{name: "wrong source revision", trigger: continuation.TriggerRunReady, sourceID: "scope-candidate", sourceRevision: 2},
		{name: "wrong tool deadline origin", trigger: continuation.TriggerToolWaitDeadline, sourceID: "another-run", sourceRevision: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			rt := newRuntime(t)
			run := createRun(t, rt, "scope-candidate", kernel.RunKind("test.scope"))
			resumer := &recordingResumer{snapshot: run}
			dispatcher, err := continuation.NewDispatcher(rt, continuation.RegisterResumer(run.Run.Kind, resumer))
			if err != nil {
				t.Fatal(err)
			}
			payload := continuation.Payload{
				SchemaVersion: continuation.SchemaVersion, RunID: run.Run.ID,
				ExpectedRevision: run.Run.Revision, Trigger: test.trigger,
				SourceRunID: test.sourceID, SourceRevision: test.sourceRevision,
			}
			if test.trigger == continuation.TriggerToolWaitDeadline {
				payload.WaitGeneration = 1
			}
			if err = dispatcher.Dispatch(t.Context(), payload); !errors.Is(err, continuation.ErrInvalidInput) {
				t.Fatalf("unrelated wakeup not refused: %v", err)
			}
			if resumer.calls != 0 {
				t.Fatal("invalid recovery payload reached feature execution")
			}
		})
	}
}
