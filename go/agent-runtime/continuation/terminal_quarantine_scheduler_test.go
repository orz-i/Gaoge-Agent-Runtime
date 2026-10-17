package continuation_test

import (
	"encoding/json"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/continuation"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

func TestSchedulerProtectsProducedJobsFromUnverifiedFinalDLQ(t *testing.T) {
	fixture := newSchedulerFixture(t)
	created := createRun(t, fixture.runtime, "producer-protected", "test.producer")
	if _, err := fixture.runtime.Apply(t.Context(), created.Run.ID, created.Run.Revision, kernel.Mutation{
		Status: kernel.RunStatusRunning, State: json.RawMessage(`{}`),
		Events: []kernel.EventDraft{{Type: "test.wakeup", Wakeup: true}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := fixture.scheduler.Project(t.Context()); err != nil {
		t.Fatal(err)
	}
	jobs := queuedJobs(t, fixture.delivery, 1)
	if jobs[0].Kind != continuation.JobKind || !jobs[0].Policy.RequireTerminalFence ||
		jobs[0].Policy.MaxAttempts != 12 {
		t.Fatalf("Scheduler emitted an unsafe auto-DLQ job: %+v", jobs[0])
	}
}
