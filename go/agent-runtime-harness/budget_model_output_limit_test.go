package harness

import (
	"context"
	"strconv"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

type fixedOutputLimitMeter struct{}

func (fixedOutputLimitMeter) PlanTokenAdmission(context.Context, model.Request) (model.TokenAdmission, error) {
	return model.TokenAdmission{InputUpperBound: 100, MaxOutputTokens: 512}, nil
}

func TestSharedTokenReservationOnlyTightensFrozenOutputLimit(t *testing.T) {
	for _, ceiling := range []int64{0, 64, 1024} {
		t.Run(strconv.FormatInt(ceiling, 10), func(t *testing.T) {
			_, shared, request := hostedBudgetFixture(t, budget.Limits{MaxLLMCalls: 3, MaxTotalTokens: 4096})
			request.HostedTools, request.Tools = nil, nil
			request.MaxOutputTokens = ceiling
			shared.dependencies.Meter = fixedOutputLimitMeter{}
			ledger, err := shared.bindRun(t.Context(), request.RunID)
			if err != nil {
				t.Fatal(err)
			}
			prepared, reservation, err := shared.modelReservation(t.Context(), ledger, request)
			want := int64(512)
			if ceiling > 0 && ceiling < want {
				want = ceiling
			}
			if err != nil || prepared.MaxOutputTokens != want || reservation.Requested.OutputTokens != want {
				t.Fatalf("fresh reservation widened a frozen ceiling: request=%d reserved=%d want=%d err=%v", prepared.MaxOutputTokens, reservation.Requested.OutputTokens, want, err)
			}
			ledger, err = shared.coordinator().Reserve(t.Context(), ledger.ID, reservation, true)
			if err != nil {
				t.Fatal(err)
			}
			replayed, _, err := shared.modelReservation(t.Context(), ledger, request)
			if err != nil || replayed.MaxOutputTokens != want || request.MaxOutputTokens != ceiling {
				t.Fatalf("reservation replay changed the frozen request: got=%d original=%d err=%v", replayed.MaxOutputTokens, request.MaxOutputTokens, err)
			}
		})
	}
}
