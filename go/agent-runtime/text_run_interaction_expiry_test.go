package agentruntime

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/domain"
)

const (
	interactionAppliedID = "interaction-applied"
	runAppliedID         = "run-applied"
)

var errInteractionExpiryTest = errors.New("expiry failed")

func TestExpireRunInteractionsOncePublishesAppliedExpirations(t *testing.T) {
	before := time.Date(2026, time.July, 23, 12, 0, 0, 0, time.UTC)
	expired := []domain.ExpiredInteraction{
		{InteractionID: interactionAppliedID, RunID: runAppliedID},
		{InteractionID: "interaction-unchanged", RunID: "run-unchanged"},
	}
	var expireCalls, publishedRuns, finishedRuns []string
	err := expireRunInteractionsOnce(t.Context(), before, interactionExpiryDependencies{
		list: func(_ context.Context, gotBefore time.Time, limit int) ([]domain.ExpiredInteraction, error) {
			if !gotBefore.Equal(before) || limit != 100 {
				t.Fatalf("list before=%v limit=%d, want %v and 100", gotBefore, limit, before)
			}
			return expired, nil
		},
		expire: func(_ context.Context, interactionID string) ([]domain.Event, bool, error) {
			expireCalls = append(expireCalls, interactionID)
			if interactionID == interactionAppliedID {
				return []domain.Event{{EventID: "event-expired", RunID: runAppliedID}}, true, nil
			}
			return nil, false, nil
		},
		publish: func(runID string, events []domain.Event) {
			if len(events) != 1 || events[0].EventID != "event-expired" {
				t.Fatalf("unexpected published events: %+v", events)
			}
			publishedRuns = append(publishedRuns, runID)
		},
		finish: func(runID string) {
			finishedRuns = append(finishedRuns, runID)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	assertStringSlice(t, expireCalls, []string{interactionAppliedID, "interaction-unchanged"})
	assertStringSlice(t, publishedRuns, []string{runAppliedID})
	assertStringSlice(t, finishedRuns, []string{runAppliedID})
}

func TestExpireRunInteractionsOnceStopsOnRepositoryErrors(t *testing.T) {
	err := expireRunInteractionsOnce(t.Context(), time.Now(), interactionExpiryDependencies{
		list: func(context.Context, time.Time, int) ([]domain.ExpiredInteraction, error) {
			return []domain.ExpiredInteraction{{InteractionID: "interaction-1", RunID: "run-1"}}, nil
		},
		expire: func(context.Context, string) ([]domain.Event, bool, error) {
			return nil, false, errInteractionExpiryTest
		},
		publish: func(string, []domain.Event) {
			t.Fatal("publish must not run after an expiry error")
		},
		finish: func(string) {
			t.Fatal("finish must not run after an expiry error")
		},
	})
	if !errors.Is(err, errInteractionExpiryTest) {
		t.Fatalf("error = %v, want %v", err, errInteractionExpiryTest)
	}
}

func assertStringSlice(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("values = %v, want %v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("values[%d] = %q, want %q", index, got[index], want[index])
		}
	}
}
