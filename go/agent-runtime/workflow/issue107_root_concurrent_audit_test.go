package workflow_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/workflow"
)

type auditRootSecondEffectBarrier struct {
	mu            sync.Mutex
	calls         map[string]int
	secondEntered chan struct{}
	secondRelease chan struct{}
}

func (e *auditRootSecondEffectBarrier) Execute(_ context.Context, request workflow.EffectRequest) (workflow.EffectResult, error) {
	e.mu.Lock()
	e.calls[request.Kind]++
	e.mu.Unlock()
	if request.Kind == "second.effect" {
		e.secondEntered <- struct{}{}
		<-e.secondRelease
	}
	return workflow.EffectResult{
		Disposition: workflow.DispositionCompleted,
		ReceiptID:   "receipt_" + request.EffectID,
		Output:      json.RawMessage(`{"ok":true}`),
	}, nil
}
func (e *auditRootSecondEffectBarrier) count(kind string) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.calls[kind]
}

// The public Workflow.Runner.Resume has no native same-Run execution lease
// for ROOT Workflow Runs: withChildPhysicalGrant returns physical() directly
// when ParentRunID is empty. Two queue workers legitimately sharing the
// expected Run revision can therefore enter the same persisted pending
// Effect and both send the physical request. The later Kernel CAS conflict
// only prevents duplicate result writes, not duplicate external effects.
func TestAuditIssue107RootWorkflowConcurrentResumeDoubleDispatchesExternalEffect(t *testing.T) {
	rt := newWorkflowRuntime(t)
	barrier := &auditRootSecondEffectBarrier{
		calls: map[string]int{}, secondEntered: make(chan struct{}, 2), secondRelease: make(chan struct{}),
	}
	runner := newWorkflowRunner(t, rt, barrier)
	definition := compileWorkflow(t, []workflow.Node{
		effectNode("first", "first.effect"),
		effectNode("second", "second.effect"),
		returnNode(json.RawMessage(`{"done":true}`)),
	}, workflow.Limits{MaxActivationsPerSegment: 8})
	request := workflowRequest(definition)
	request.ID = "root-dual-worker"
	root, err := runner.StartRun(t.Context(), request)
	if !errors.Is(err, workflow.ErrSegmentYielded) || root.Run.Status != kernel.RunStatusRunning ||
		barrier.count("first.effect") != 1 || barrier.count("second.effect") != 0 {
		t.Fatalf("fixture failed: root=%s err=%v counts=%v %v", root.Run.Status, err, barrier.count("first.effect"), barrier.count("second.effect"))
	}
	type result struct {
		snapshot kernel.Snapshot
		err      error
	}
	done := make(chan result, 2)
	for i := 0; i < 2; i++ {
		go func() {
			s, e := runner.Resume(context.Background(), root.Run.ID, root.Run.Revision)
			done <- result{s, e}
		}()
	}
	select {
	case <-barrier.secondEntered:
	case <-time.After(4 * time.Second):
		close(barrier.secondRelease)
		t.Fatal("original pending Effect never began")
	}
	// The first physical callback is deliberately held. Another worker
	// given the identical Run revision MUST fail Store admission without
	// reaching the external Effect, rather than waiting for its late CAS.
	var rejected result
	select {
	case rejected = <-done:
	case <-time.After(4 * time.Second):
		close(barrier.secondRelease)
		t.Fatal("second Root Resume did not receive an immediate physical admission verdict")
	}
	if !errors.Is(rejected.err, kernel.ErrChildExecutionBusy) {
		close(barrier.secondRelease)
		t.Fatalf("racing Root Resume unexpectedly entered the original physical effect: err=%v", rejected.err)
	}
	if count := barrier.count("second.effect"); count != 1 {
		close(barrier.secondRelease)
		t.Fatalf("duplicate Root physical Effect entered before first receipt: calls=%d", count)
	}
	close(barrier.secondRelease)
	var accepted result
	select {
	case accepted = <-done:
	case <-time.After(4 * time.Second):
		t.Fatal("original Root Effect did not finish")
	}
	if accepted.err != nil || accepted.snapshot.Run.Status != kernel.RunStatusCompleted ||
		barrier.count("second.effect") != 1 {
		t.Fatalf("original Root effect was dropped or duplicated: accepted=%v status=%s physicalCalls=%d",
			accepted.err, accepted.snapshot.Run.Status, barrier.count("second.effect"))
	}
}
