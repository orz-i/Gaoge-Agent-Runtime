package agent

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	runtimecontext "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/context"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

type finalCandidateCounter struct {
	requests []model.Request
}

func (c *finalCandidateCounter) Count(_ context.Context, req runtimecontext.TokenCountRequest) (int64, error) {
	var request model.Request
	if err := json.Unmarshal(req.Payload, &request); err != nil {
		return 0, err
	}
	c.requests = append(c.requests, request)
	// An exact count must be allowed to depend on the final request identity.
	if request.InvocationID == "" {
		return 1, nil
	}
	return 30, nil
}

func ownedCandidateRunner(t *testing.T, rt *kernel.Runtime, ports *summaryCorePorts, provider model.Client, clock kernel.Clock) *Runner {
	t.Helper()
	bounded, err := NewBoundedContextAdapter(ports.manager, ports)
	if err != nil {
		t.Fatal(err)
	}
	runner, err := NewRunner(Dependencies{Runtime: rt, Model: provider, Clock: clock, BoundedContext: bounded})
	if err != nil {
		t.Fatal(err)
	}
	return runner
}

// A request that fits before InvocationID is assigned must not enter the
// durable model lifecycle when its actual canonical request is too large.
func TestCanonicalBusinessCandidateUsesFinalIdentityAtEveryHardBoundary(t *testing.T) {
	for _, mode := range []string{"estimated_bytes", "estimated_tokens", "exact_bytes", "exact_tokens", "exact_fits"} {
		t.Run(mode, func(t *testing.T) {
			_, rt, ports, provider, clock, start := newSummaryCoreFixture(t, nil)
			start.WorkingStateSummary = nil
			ports.reader = false
			ports.policy.SoftLimitPercent = 80
			ports.window.ContextTokens = 100000
			ports.window.TokenCountContext = runtimecontext.TokenCountContext{Protocol: "fixture", Model: "fixture"}
			deps := runtimecontext.Dependencies{Accounting: func(_ context.Context, _ model.Request, window runtimecontext.ModelWindow) (runtimecontext.AccountingProjection, error) {
				return runtimecontext.AccountingProjection{SystemPrompt: strings.Repeat("provider", 100), ReservedOutputTokens: window.ReservedOutputTokens}, nil
			}}
			var counter finalCandidateCounter
			if strings.HasPrefix(mode, "exact") {
				deps.Counter = &counter
			}
			ports.manager = runtimecontext.NewManager(deps)
			request := model.Request{RunID: start.ID, Model: start.Model, MaxOutputTokens: start.MaxModelOutputTokens, Messages: runtimecontext.Materialize(ports.source.Window)}
			withoutID, err := ports.manager.AssessModelRequest(t.Context(), request, ports.window, ports.policy)
			if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "estimated_bytes", "exact_bytes":
				ports.policy.MaxSerializedBytes = withoutID.SerializedBytes
			case "estimated_tokens":
				ports.policy.MaxInputTokens = withoutID.HardTokenEstimate
			case "exact_tokens":
				ports.policy.MaxInputTokens = 29
			case "exact_fits":
				ports.policy.MaxInputTokens = 30
			}
			counter.requests = nil
			runner := ownedCandidateRunner(t, rt, ports, provider, clock)
			snapshot, err := runner.StartRun(t.Context(), start)
			if strings.HasPrefix(mode, "exact") {
				if len(counter.requests) != 1 || !strings.HasPrefix(counter.requests[0].InvocationID, "modelinv_") {
					t.Fatalf("counter did not receive actual candidate identity: calls=%d", len(counter.requests))
				}
			}
			view, viewErr := ViewState(snapshot)
			if viewErr != nil {
				t.Fatal(viewErr)
			}
			if mode == "exact_fits" {
				calls := provider.calls()
				if err != nil || snapshot.Run.Status != kernel.RunStatusCompleted || len(calls) != 1 ||
					len(view.ModelInvocations) != 1 || view.ModelInvocations[0].ID != counter.requests[0].InvocationID {
					t.Fatalf("admitted candidate was not the durable intent: err=%v calls=%d", err, len(calls))
				}
				counted, _ := model.MarshalRequestForInputCount(counter.requests[0])
				dispatched, _ := model.MarshalRequestForInputCount(calls[0])
				if string(counted) != string(dispatched) {
					t.Fatal("counter and provider observed different requests")
				}
				return
			}
			if !errors.Is(err, runtimecontext.ErrBudgetExceeded) || errors.Is(err, ErrRootOutcomeUnknown) ||
				snapshot.Run.Status != kernel.RunStatusFailed || len(view.ModelInvocations) != 0 || len(provider.calls()) != 0 ||
				ports.current.ID != ports.source.ID || len(ports.artifacts) != 0 {
				t.Fatalf("over-budget final identity escaped concrete pre-intent rejection: err=%v status=%s", err, snapshot.Run.Status)
			}
		})
	}
}

// Even with an archive reader, a candidate whose fully identified request
// exceeds the hard limit must not roll the Context or write an artifact.
func TestCanonicalCompactionSelectsOnlyMeasuredFinalInvocation(t *testing.T) {
	for _, fit := range []bool{false, true} {
		name := "over_budget"
		if fit {
			name = "fits"
		}
		t.Run(name, func(t *testing.T) {
			_, rt, ports, provider, clock, start := newSummaryCoreFixture(t, nil)
			start.WorkingStateSummary = nil
			ports.policy.SoftLimitPercent = 80
			ports.policy.PreserveRecentTurns = 1
			ports.window.ContextTokens = 100000
			ports.manager = summaryAccountingFixture("private provider prompt", nil)
			original := model.Request{RunID: start.ID, Model: start.Model, MaxOutputTokens: start.MaxModelOutputTokens, Messages: runtimecontext.Materialize(ports.source.Window)}
			compact, err := ports.manager.CompactPortable(runtimecontext.PortableCompactionRequest{Previous: ports.source, RunID: original.RunID, Messages: original.Messages, Policy: runtimecontext.NormalizePolicy(ports.policy), OmitExtract: true})
			if err != nil {
				t.Fatal(err)
			}
			candidate := model.CloneRequest(original)
			candidate.Messages = runtimecontext.Materialize(compact.Window)
			beforeID, err := ports.manager.AssessModelRequest(t.Context(), candidate, ports.window, ports.policy)
			if err != nil {
				t.Fatal(err)
			}
			candidate.InvocationID = "modelinv_" + strings.Repeat("0", 32)
			afterID, err := ports.manager.AssessModelRequest(t.Context(), candidate, ports.window, ports.policy)
			if err != nil {
				t.Fatal(err)
			}
			ports.policy.MaxSerializedBytes = beforeID.SerializedBytes
			if fit {
				ports.policy.MaxSerializedBytes = afterID.SerializedBytes
			}
			runner := ownedCandidateRunner(t, rt, ports, provider, clock)
			snapshot, err := runner.StartRun(t.Context(), start)
			if !fit {
				if !errors.Is(err, runtimecontext.ErrBudgetExceeded) || errors.Is(err, ErrRootOutcomeUnknown) ||
					len(provider.calls()) != 0 || len(ports.artifacts) != 0 || ports.current.ID != ports.source.ID {
					t.Fatalf("over-budget compaction wrote Context or dispatched: err=%v artifacts=%d", err, len(ports.artifacts))
				}
				return
			}
			calls := provider.calls()
			if err != nil || snapshot.Run.Status != kernel.RunStatusCompleted || len(calls) != 1 || len(ports.artifacts) != 1 ||
				ports.current.Generation <= ports.source.Generation || ports.current.Trace.LastAssessment == nil {
				t.Fatalf("admitted final candidate did not complete: %v calls=%d artifacts=%d", err, len(calls), len(ports.artifacts))
			}
			actual, err := ports.manager.AssessModelRequest(t.Context(), calls[0], ports.window, ports.policy)
			if err != nil || !reflect.DeepEqual(*ports.current.Trace.LastAssessment, actual) || !runtimecontext.WithinHardBudget(actual, ports.policy) {
				t.Fatalf("persisted candidate assessment does not match the dispatched request: %v", err)
			}
		})
	}
}
