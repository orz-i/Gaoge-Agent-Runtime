package agentruntime

import (
	"context"
	"errors"
	"testing"
	"time"

	model "github.com/orz-i/Gaoge/sdk/go/agent-runtime/domain"
)

const (
	valueRunSuspended92A35259 = "run.suspended"
)

func TestReconcileTextRunsOnceSuspendsOnlyRunsWithoutActiveLease(t *testing.T) {
	active := model.Run{RunID: "run_active", CurrentStepID: "step_active", Status: model.RunStatusRunning}
	stale := model.Run{RunID: "run_stale", CurrentStepID: "step_stale", Status: model.RunStatusRunning}
	var suspendedRunID string
	var suspendedEvents []model.Event
	err := reconcileTextRunsOnce(t.Context(), time.Now(), textRunReconciliationDependencies{
		list: func(context.Context, time.Time) ([]model.Run, error) {
			return []model.Run{active, stale}, nil
		},
		leaseState: func(_ context.Context, runID string) (GenerationLeaseState, error) {
			if runID == active.RunID {
				return GenerationLeaseActive, nil
			}
			return GenerationLeaseExpired, nil
		},
		suspend: func(run model.Run, events []model.Event) (bool, error) {
			suspendedRunID = run.RunID
			suspendedEvents = events
			return true, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if suspendedRunID != stale.RunID {
		t.Fatalf("suspended run = %q, want %q", suspendedRunID, stale.RunID)
	}
	if len(suspendedEvents) != 2 || suspendedEvents[0].EventType != "step.suspended" || suspendedEvents[1].EventType != valueRunSuspended92A35259 {
		t.Fatalf("unexpected suspension events: %#v", suspendedEvents)
	}
	if suspendedEvents[0].StepID != stale.CurrentStepID || suspendedEvents[1].StepID != stale.CurrentStepID {
		t.Fatalf("suspension events lost current step: %#v", suspendedEvents)
	}
}

func TestReconcileTextRunsOnceReturnsListAndAppendFailures(t *testing.T) {
	wantListErr := errCategory6EF1809EF6
	if err := reconcileTextRunsOnce(t.Context(), time.Now(), textRunReconciliationDependencies{
		list:       func(context.Context, time.Time) ([]model.Run, error) { return nil, wantListErr },
		leaseState: func(context.Context, string) (GenerationLeaseState, error) { return GenerationLeaseExpired, nil },
		suspend:    func(model.Run, []model.Event) (bool, error) { return true, nil },
	}); !errors.Is(err, wantListErr) {
		t.Fatalf("list error = %v, want %v", err, wantListErr)
	}

	wantAppendErr := errCategory0675656AB0
	err := reconcileTextRunsOnce(t.Context(), time.Now(), textRunReconciliationDependencies{
		list: func(context.Context, time.Time) ([]model.Run, error) {
			return []model.Run{{RunID: "run_stale", CurrentStepID: "step_stale"}}, nil
		},
		leaseState: func(context.Context, string) (GenerationLeaseState, error) { return GenerationLeaseExpired, nil },
		suspend:    func(model.Run, []model.Event) (bool, error) { return false, wantAppendErr },
	})
	if !errors.Is(err, wantAppendErr) {
		t.Fatalf("append error = %v, want %v", err, wantAppendErr)
	}
}

func TestReconcileTextRunsOnceSkipsUnknownLease(t *testing.T) {
	leaseErr := errCategoryDE3F9751FB
	warned := false
	suspended := false
	err := reconcileTextRunsOnce(t.Context(), time.Now(), textRunReconciliationDependencies{
		list: func(context.Context, time.Time) ([]model.Run, error) {
			return []model.Run{{RunID: "run_active", Status: model.RunStatusRunning}}, nil
		},
		leaseState: func(context.Context, string) (GenerationLeaseState, error) {
			return GenerationLeaseUnknown, leaseErr
		},
		warn: func(_ string, state GenerationLeaseState, err error) {
			warned = state == GenerationLeaseUnknown && errors.Is(err, leaseErr)
		},
		suspend: func(model.Run, []model.Event) (bool, error) {
			suspended = true
			return true, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !warned || suspended {
		t.Fatalf("unknown lease must warn and skip: warned=%v suspended=%v", warned, suspended)
	}
}

func TestReconcileTextRunsOnceKeepsLocallyActiveRunWhenStoreFails(t *testing.T) {
	leaseErr := errCategoryDE3F9751FB
	warned := false
	suspended := false
	err := reconcileTextRunsOnce(t.Context(), time.Now(), textRunReconciliationDependencies{
		list: func(context.Context, time.Time) ([]model.Run, error) {
			return []model.Run{{RunID: "run_locally_active", Status: model.RunStatusRunning}}, nil
		},
		leaseState: func(context.Context, string) (GenerationLeaseState, error) {
			return GenerationLeaseActive, leaseErr
		},
		warn: func(_ string, state GenerationLeaseState, err error) {
			warned = state == GenerationLeaseActive && errors.Is(err, leaseErr)
		},
		suspend: func(model.Run, []model.Event) (bool, error) {
			suspended = true
			return true, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !warned || suspended {
		t.Fatalf("local active evidence must prevent suspension during store failure: warned=%v suspended=%v", warned, suspended)
	}
}

var (
	errCategory0675656AB0 = errors.New("append failed")
	errCategory6EF1809EF6 = errors.New("list failed")
	errCategoryDE3F9751FB = errors.New("redis unavailable")
)
