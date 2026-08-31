package observability_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/observability"
)

func TestEventWireShapeIsContentSafeByConstruction(t *testing.T) {
	t.Parallel()
	encoded, err := json.Marshal(observability.Event{
		Scope: observability.ScopeModelInvocation, Phase: observability.PhaseCompleted,
		RunID: "run-1", OperationID: "inv-1", Model: "model-1", ObservedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"prompt", "completion", "arguments", "result", "message", "data", "delta"} {
		if strings.Contains(strings.ToLower(string(encoded)), forbidden) {
			t.Fatalf("observability event exposed forbidden content field %q: %s", forbidden, encoded)
		}
	}
}

func TestNewSetRejectsInvalidAndDuplicateRecorders(t *testing.T) {
	t.Parallel()
	if _, err := observability.NewSet(observability.RecorderFunc{}); !errors.Is(err, observability.ErrInvalidRecorder) {
		t.Fatalf("invalid recorder err = %v", err)
	}
	if _, err := observability.NewSet(
		observability.RecorderFunc{RecorderName: "same"},
		observability.RecorderFunc{RecorderName: " same "},
	); !errors.Is(err, observability.ErrDuplicateName) {
		t.Fatalf("duplicate recorder err = %v", err)
	}
}

func TestSetPreservesOrderAndContainsRecorderPanics(t *testing.T) {
	t.Parallel()
	order := make([]string, 0)
	set, err := observability.NewSet(
		observability.RecorderFunc{RecorderName: "first", RecordFunc: func(context.Context, observability.Event) {
			order = append(order, "first")
			panic("contained")
		}},
		observability.RecorderFunc{RecorderName: "second", RecordFunc: func(context.Context, observability.Event) {
			order = append(order, "second")
		}},
	)
	if err != nil {
		t.Fatal(err)
	}
	set.Record(t.Context(), observability.Event{
		Scope: observability.ScopeRun, Phase: observability.PhaseStarted, RunID: "run-1",
	})
	if len(order) != 2 || order[0] != "first" || order[1] != "second" {
		t.Fatalf("order = %v", order)
	}
}
