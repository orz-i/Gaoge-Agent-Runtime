package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	runtimecontext "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/context"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
)

// The fake refuses any write. A budget rejection must remain pre-dispatch and
// produce only numeric facts, even when the prompt contains secrets.
type pressureTestPorts struct{ owner, reader, exact bool }

func (p pressureTestPorts) Hydrate(ctx context.Context, invocation plugin.RunInvocation) (context.Context, BoundedContextHydration, error) {
	return ctx, BoundedContextHydration{RunID: invocation.RunID, SourceBinding: json.RawMessage(`{}`)}, nil
}
func (p pressureTestPorts) Plan(_ context.Context, request model.Request) (BoundedContextPlan, error) {
	window := runtimecontext.ModelWindow{ContextTokens: 1024, ReservedOutputTokens: 128}
	if p.exact {
		window.TokenCountContext = runtimecontext.TokenCountContext{Protocol: "openai", Model: "example"}
	}
	return BoundedContextPlan{
		Request: request, Checkpoint: &runtimecontext.Checkpoint{ID: "not-read"},
		ModelWindow: window, Policy: runtimecontext.Policy{MaxInputTokens: 48, MaxSerializedBytes: 2048},
		Owner: p.owner, ArtifactReader: p.reader, TurnID: "turn", TurnRevision: 1,
		SourceBinding: json.RawMessage(`{}`),
	}, nil
}
func (pressureTestPorts) PutArtifact(context.Context, runtimecontext.Artifact) (runtimecontext.Artifact, error) {
	panic("unapproved Context write")
}
func (pressureTestPorts) CommitCheckpoint(context.Context, BoundedContextPlan, runtimecontext.Checkpoint) (runtimecontext.Checkpoint, error) {
	panic("unapproved checkpoint")
}
func (pressureTestPorts) BindCheckpoint(context.Context, string, runtimecontext.Checkpoint) error {
	panic("unapproved bind")
}

type pressureTestCounter struct {
	calls int
	count int64
}

func (c *pressureTestCounter) Count(context.Context, runtimecontext.TokenCountRequest) (int64, error) {
	c.calls++
	if c.count != 0 {
		return c.count, nil
	}
	return 150, nil
}

func TestBoundedContextNativeCountCanAdmitLargeBytesWithoutRaisingHostLimit(t *testing.T) {
	counter := &pressureTestCounter{count: 24}
	manager := runtimecontext.NewManager(runtimecontext.Dependencies{Counter: counter})
	adapter, err := NewBoundedContextAdapter(manager, pressureTestPorts{exact: true})
	if err != nil {
		t.Fatal(err)
	}
	original := model.Request{RunID: "run", Model: "openai/example", Messages: []model.Message{{
		Role: model.RoleUser, Content: model.TextParts(strings.Repeat("B", 512)),
	}}}
	prepared, err := adapter.Prepare(t.Context(), original)
	if err != nil || counter.calls != 1 || !sameBoundedValue(original, prepared) {
		t.Fatalf("exact native Count could not admit a valid long request within the unchanged hard limit: %v calls=%d", err, counter.calls)
	}
	// Exactly the same bytes, without the counter, are correctly denied under
	// the conservative hard-byte upper bound. Neither path changes policy.
	without, err := NewBoundedContextAdapter(runtimecontext.NewManager(runtimecontext.Dependencies{}), pressureTestPorts{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = without.Prepare(t.Context(), original)
	if !errors.Is(err, runtimecontext.ErrBudgetExceeded) {
		t.Fatalf("missing native count silently weakened the byte-hard budget: %v", err)
	}
}

func TestBoundedContextSerializedByteLimitRejectsEvenWithExactTokenCount(t *testing.T) {
	counter := &pressureTestCounter{count: 24}
	manager := runtimecontext.NewManager(runtimecontext.Dependencies{Counter: counter})
	adapter, err := NewBoundedContextAdapter(manager, pressureTestPorts{exact: true})
	if err != nil {
		t.Fatal(err)
	}
	request := model.Request{RunID: "run", Model: "openai/example", Messages: []model.Message{{
		Role: model.RoleUser, Content: model.TextParts(strings.Repeat("B", 4096)),
	}}}
	_, err = adapter.Prepare(t.Context(), request)
	var details *BoundedContextBudgetError
	if !errors.Is(err, runtimecontext.ErrBudgetExceeded) || !errors.As(err, &details) ||
		details.Assessment.TokenCountSource != runtimecontext.CountExact ||
		details.Assessment.HardTokenEstimate != 24 ||
		details.Assessment.HardInputTokens != 48 ||
		details.Assessment.SerializedBytes <= details.MaxSerializedBytes ||
		details.Reason != "no_artifact_reader" || counter.calls != 1 {
		t.Fatalf("byte-hard limit bypassed or misattributed after exact count: %v detail=%+v calls=%d", err, details, counter.calls)
	}
}

func TestBoundedContextBudgetRejectionReportsOriginalAssessmentWithoutInput(t *testing.T) {
	secret := "PRIVATE_USER_API_KEY_DO_NOT_PRINT"
	for _, tc := range []struct {
		name, reason         string
		owner, reader, exact bool
	}{
		{name: "no-artifact-reader", reason: "no_artifact_reader", owner: true},
		{name: "not-owner", reason: "not_owner", reader: true},
		{name: "exact-count", reason: "no_artifact_reader", owner: true, exact: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			counter := &pressureTestCounter{}
			manager := runtimecontext.NewManager(runtimecontext.Dependencies{})
			if tc.exact {
				manager = runtimecontext.NewManager(runtimecontext.Dependencies{Counter: counter})
			}
			adapter, err := NewBoundedContextAdapter(manager, pressureTestPorts{tc.owner, tc.reader, tc.exact})
			if err != nil {
				t.Fatal(err)
			}
			request := model.Request{RunID: "run", Model: "openai/example", Messages: []model.Message{{
				Role: model.RoleUser, Content: model.TextParts(secret + strings.Repeat("A", 512)),
			}}}
			_, err = adapter.Prepare(t.Context(), request)
			var details *BoundedContextBudgetError
			if !errors.Is(err, runtimecontext.ErrBudgetExceeded) || !errors.As(err, &details) ||
				details.Reason != tc.reason || details.Assessment.HardInputTokens != 48 ||
				details.Assessment.HardTokenEstimate <= 48 || details.MaxSerializedBytes != 2048 {
				t.Fatalf("original budget rejection evidence unavailable: %T %v", err, err)
			}
			wantSource := runtimecontext.CountEstimated
			wantCalls := 0
			if tc.exact {
				wantSource, wantCalls = runtimecontext.CountExact, 1
			}
			if details.Assessment.TokenCountSource != wantSource || counter.calls != wantCalls {
				t.Fatalf("diagnostics changed counter attempts: %+v calls=%d", details, counter.calls)
			}
			body, marshalErr := json.Marshal(details)
			if marshalErr != nil || strings.Contains(err.Error(), secret) || strings.Contains(string(body), secret) {
				t.Fatalf("input leaked via budget failure: %q", body)
			}
		})
	}
}
