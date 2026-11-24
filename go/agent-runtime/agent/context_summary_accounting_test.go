package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	runtimecontext "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/context"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

func summaryAccountingFixture(prompt string, called *int) *runtimecontext.Manager {
	return runtimecontext.NewManager(runtimecontext.Dependencies{Accounting: func(_ context.Context, r model.Request, w runtimecontext.ModelWindow) (runtimecontext.AccountingProjection, error) {
		if called != nil {
			(*called)++
		}
		return runtimecontext.AccountingProjection{ModelOptions: json.RawMessage(`{"provider_private_option":"PRIVATE_DEFAULT"}`),
			SystemPrompt: prompt, ReservedOutputTokens: w.ReservedOutputTokens}, nil
	}})
}

func summaryLargeBusinessContract(t *testing.T) *model.OutputContract {
	t.Helper()
	schema, err := json.Marshal(map[string]any{"type": "object", "properties": map[string]any{
		"answer": map[string]any{"type": "string", "description": strings.Repeat("s", 14000)},
	}, "required": []string{"answer"}, "additionalProperties": false})
	if err != nil {
		t.Fatal(err)
	}
	return &model.OutputContract{Name: "business", Schema: schema}
}

func TestCanonicalSummaryPlanningDoesNotBuyImpossibleTarget(t *testing.T) {
	_, rt, ports, recorded, clock, request := newSummaryCoreFixture(t, nil)
	ports.policy.MaxSerializedBytes = 18500
	ports.window.ContextTokens = 60000
	ports.manager = summaryAccountingFixture(strings.Repeat("p", 5000), nil)
	request.OutputContract = summaryLargeBusinessContract(t)
	runner := summaryCoreRunner(t, rt, ports, summaryJSONBusinessModel{recorded}, clock, nil)
	snapshot, err := runner.StartRun(t.Context(), request)
	if err != nil && !errors.Is(err, runtimecontext.ErrBudgetExceeded) {
		t.Fatal(err)
	}
	if len(recorded.calls()) != 0 || ports.current.ID != ports.source.ID || len(ports.artifacts) != 0 {
		t.Fatalf("impossible canonical target bought a summary or changed Context: calls=%d checkpoint=%s artifacts=%d", len(recorded.calls()), ports.current.ID, len(ports.artifacts))
	}
	view, viewErr := ViewState(snapshot)
	if viewErr != nil || len(view.ModelInvocations) != 0 {
		t.Fatalf("impossible target created an intent: %v", viewErr)
	}
}

func TestCanonicalSummaryFinalTargetRejectedBeforeCommit(t *testing.T) {
	const prompt = "PROVIDER_PRIVATE_SYSTEM"
	run := func(limit int64) (*summaryCorePorts, *summaryCoreModel, kernel.Snapshot, error) {
		_, rt, ports, recorded, clock, request := newSummaryCoreFixture(t, nil)
		ports.policy.MaxSerializedBytes = limit
		ports.window.ContextTokens = 60000
		ports.manager = summaryAccountingFixture(strings.Repeat(prompt, 200), nil)
		request.OutputContract = summaryLargeBusinessContract(t)
		raw, err := json.Marshal(map[string]any{"goal": strings.Repeat("goal_", 260), "constraints": []string{"preserve the source"},
			"completed": []string{"read the source"}, "pending": []string{"answer the user"}, "decisions": []string{"use recorded facts"}})
		if err != nil {
			t.Fatal(err)
		}
		recorded.response = &model.Response{Content: string(raw), Usage: &model.Usage{InputTokens: 7, OutputTokens: 3}}
		runner := summaryCoreRunner(t, rt, ports, summaryJSONBusinessModel{recorded}, clock, nil)
		snapshot, runErr := runner.StartRun(t.Context(), request)
		return ports, recorded, snapshot, runErr
	}
	ports, recorded, snapshot, err := run(0)
	if err != nil || len(recorded.calls()) != 2 || ports.current.Window.WorkingState == nil {
		t.Fatalf("unconstrained fixture did not install summary: %v calls=%d", err, len(recorded.calls()))
	}
	view, err := ViewState(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	frame := view.ModelInvocations[0].ContextSummary.BusinessAccounting
	final, err := frame.AssessModelRequest(recorded.calls()[1], ports.window, ports.policy)
	if err != nil {
		t.Fatal(err)
	}
	// The result itself is over budget, but the smaller minimum target and
	// original independent summary request fit. Only ordinary compacted
	// history may be installed as the fallback.
	limited, recorded, snapshot, err := run(final.SerializedBytes - 1)
	if err != nil || len(recorded.calls()) != 2 || snapshot.Run.Status != kernel.RunStatusCompleted {
		t.Fatalf("safe fallback failed: %v calls=%d status=%s limit=%d", err, len(recorded.calls()), snapshot.Run.Status, final.SerializedBytes-1)
	}
	view, err = ViewState(snapshot)
	if err != nil || limited.current.Window.WorkingState != nil || len(view.ModelInvocations) != 2 ||
		view.ModelInvocations[0].Purpose != ModelInvocationContextSummary || view.ModelInvocations[0].SummaryTargetHash != "" || view.Budget.Usage.LLMCalls != 2 {
		t.Fatalf("oversized summary target was installed or its known receipt was lost: %v", err)
	}
	for _, artifact := range limited.artifacts {
		if strings.Contains(string(artifact.ContentJSON), prompt) {
			t.Fatal("private provider prompt leaked into archive")
		}
	}
	if strings.Contains(string(snapshot.State), prompt) || strings.Contains(string(snapshot.State), "PRIVATE_DEFAULT") {
		t.Fatal("private accounting material leaked into durable Run")
	}
}

func TestCanonicalSummaryRecoveryReusesOriginalAccounting(t *testing.T) {
	for _, cut := range []string{"before", "after"} {
		t.Run(cut, func(t *testing.T) {
			_, rt, ports, provider, clock, request := newSummaryCoreFixture(t, nil)
			projectionCalls := 0
			ports.manager = summaryAccountingFixture(strings.Repeat("PRIVATE_ORIGINAL", 100), &projectionCalls)
			ports.commitCut = cut
			runner := summaryCoreRunner(t, rt, ports, provider, clock, nil)
			if _, err := runner.StartRun(t.Context(), request); !errors.Is(err, errSummaryCoreCrash) {
				t.Fatalf("did not reach Context commit: %v", err)
			}
			if projectionCalls != 2 || len(provider.calls()) != 1 {
				t.Fatalf("summary planning repeated accounting: %d calls=%d", projectionCalls, len(provider.calls()))
			}
			pending, err := rt.Load(t.Context(), request.ID)
			if err != nil {
				t.Fatal(err)
			}
			view, err := ViewState(pending)
			if err != nil {
				t.Fatal(err)
			}
			frozen := view.ModelInvocations[0].ContextSummary.BusinessAccounting
			checkpointBeforeResume := ports.current.ID
			clock.Advance(3 * time.Minute)
			projectionCalls = 0
			ports.manager = summaryAccountingFixture(strings.Repeat("PRIVATE_RESTARTED", 50), &projectionCalls)
			restarted := summaryCoreRunner(t, rt, ports, provider, clock, nil)
			completed, err := restarted.Resume(t.Context(), pending.Run.ID, pending.Run.Revision)
			if err != nil || completed.Run.Status != kernel.RunStatusCompleted {
				t.Fatalf("recovery changed target or stranded execution: %v", err)
			}
			view, err = ViewState(completed)
			if err != nil || len(provider.calls()) != 2 || projectionCalls != 1 || view.Budget.Usage.LLMCalls != 2 ||
				view.ModelInvocations[0].ContextSummary.BusinessAccounting != frozen ||
				view.ModelInvocations[0].SummaryTargetHash == "" || ports.current.Window.WorkingState == nil ||
				(cut == "after" && checkpointBeforeResume != ports.current.ID) {
				t.Fatalf("recovery reprojected a frozen target or repeated effects: err=%v projections=%d calls=%d", err, projectionCalls, len(provider.calls()))
			}
			if strings.Contains(string(completed.State), "PRIVATE_ORIGINAL") || strings.Contains(string(completed.State), "PRIVATE_RESTARTED") ||
				strings.Contains(string(completed.State), "PRIVATE_DEFAULT") {
				t.Fatal("provider-private accounting entered durable state")
			}
		})
	}
}

func TestCanonicalSummaryRecoveryReadChecksProjectedByteLimit(t *testing.T) {
	fault := &summaryCoreFaultStore{Store: memory.NewStore(), event: "agent.model_invocation.pending", after: true}
	_, rt, ports, provider, clock, request := newSummaryCoreFixture(t, fault)
	ports.manager = summaryAccountingFixture(strings.Repeat("p", 1000), nil)
	runner := summaryCoreRunner(t, rt, ports, provider, clock, nil)
	if _, err := runner.StartRun(t.Context(), request); !errors.Is(err, errSummaryCoreCrash) {
		t.Fatalf("did not reach durable summary intent: %v", err)
	}
	snapshot, err := rt.Load(t.Context(), request.ID)
	if err != nil {
		t.Fatal(err)
	}
	state, err := decodeState(snapshot.State)
	if err != nil {
		t.Fatal(err)
	}
	invocation := &state.ModelInvocations[0]
	raw, err := model.MarshalRequestForInputCount(invocation.Request)
	if err != nil {
		t.Fatal(err)
	}
	size, err := invocation.ContextSummary.SummaryAccounting.SerializedBytes(invocation.Request)
	if err != nil || size <= int64(len(raw)) {
		t.Fatal("fixture has no private accounting overhead")
	}
	// The original bytes fit. Only the frozen canonical frame proves that
	// this active intent exceeds its declared byte budget.
	state.WorkingStateSummary.MaxSerializedBytes = size - 1
	invocation.ContextSummary.Policy = *state.WorkingStateSummary
	sourceRun := snapshot.Run
	sourceRun.Revision = invocation.SourceRevision
	input := model.CloneRequest(invocation.Request)
	input.InvocationID = ""
	derived, err := newInvocation(sourceRun, input, nil, invocation.Provider, invocation.BindingFingerprint,
		invocation.Purpose, invocation.ContextSummary, invocation.CreatedAt)
	if err != nil {
		t.Fatal(err)
	}
	invocation.ID, invocation.RequestHash, invocation.Request.InvocationID = derived.ID, derived.RequestHash, derived.ID
	snapshot.State, err = encodeState(state)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = ViewState(snapshot); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("reader accepted canonical bytes beyond the frozen limit: %v", err)
	}
}

// A route-only prompt must trigger ordinary pressure without becoming part of
// the product history. Every compacted candidate uses the same Host frame.
func TestCanonicalOrdinaryCompactionUsesOneAccountingFrame(t *testing.T) {
	_, rt, ports, provider, clock, request := newSummaryCoreFixture(t, nil)
	request.WorkingStateSummary = nil
	ports.policy.SoftLimitPercent = 80
	ports.policy.MaxSerializedBytes = 12000
	ports.window.ContextTokens = 30000
	projectionCalls := 0
	private := strings.Repeat("PROVIDER_PRIVATE_SYSTEM", 180)
	ports.manager = summaryAccountingFixture(private, &projectionCalls)
	runner := summaryCoreRunner(t, rt, ports, provider, clock, nil)
	completed, err := runner.StartRun(t.Context(), request)
	if err != nil || completed.Run.Status != kernel.RunStatusCompleted || projectionCalls != 1 || len(provider.calls()) != 1 ||
		ports.current.Generation <= ports.source.Generation || len(ports.artifacts) == 0 {
		t.Fatalf("canonical pressure failed to compact before dispatch: %v projections=%d calls=%d artifacts=%d", err, projectionCalls, len(provider.calls()), len(ports.artifacts))
	}
	last := provider.calls()[0].Messages[len(provider.calls()[0].Messages)-1]
	if model.TextValue(last.Content) != "answer the current request" {
		t.Fatal("compaction changed current user input")
	}
	checkpoint, err := json.Marshal(ports.current)
	if err != nil || strings.Contains(string(checkpoint), private) || strings.Contains(string(completed.State), private) {
		t.Fatal("accounting prompt became product context or Run state")
	}
	for _, artifact := range ports.artifacts {
		if strings.Contains(string(artifact.ContentJSON), private) {
			t.Fatal("accounting prompt became archived source")
		}
	}
}
