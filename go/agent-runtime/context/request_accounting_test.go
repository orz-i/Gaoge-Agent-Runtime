package context_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	runtimectx "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/context"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

func materializedAccounting(r model.Request, projection runtimectx.AccountingProjection) model.Request {
	r = model.CloneRequest(r)
	r.ModelOptions, r.MaxOutputTokens = projection.ModelOptions, projection.ReservedOutputTokens
	if projection.SystemPrompt != "" && (len(r.Messages) == 0 || r.Messages[0].Role != model.RoleSystem ||
		model.TextValue(r.Messages[0].Content) != projection.SystemPrompt) {
		r.Messages = append([]model.Message{{Role: model.RoleSystem, Content: model.TextParts(projection.SystemPrompt)}}, r.Messages...)
	}
	return r
}

func TestFrozenAccountingMatchesMaterializedRequestAcrossCandidates(t *testing.T) {
	const privatePrompt = "private provider prompt: <tags> \"\\\n🚀\u2028"
	request := model.Request{RunID: "run", InvocationID: "inv", BindingRef: "route", Model: "model",
		ModelOptions: json.RawMessage(`{"long": 0.100000000000000000000000001,"text":"<>" }`),
		Tools:        []tools.Definition{{Key: "read", Name: "read", InputSchema: json.RawMessage(`{"type": "object"}`)}},
		Messages:     []model.Message{{Role: model.RoleUser, Content: model.TextParts("protected input")}}}
	window := runtimectx.ModelWindow{ContextTokens: 100000, ReservedOutputTokens: 256,
		TokenCountContext: runtimectx.TokenCountContext{Protocol: "fixture", Model: "provider"}}
	policy := runtimectx.Policy{EstimateSafetyPercent: 25}
	for _, projection := range []runtimectx.AccountingProjection{
		{ReservedOutputTokens: 256},
		{ModelOptions: json.RawMessage(`{"provider_default":true,"n":9007199254740993}`), ReservedOutputTokens: 256},
		{ModelOptions: json.RawMessage(`{"x":"<private>","budget":1e1000}`), SystemPrompt: privatePrompt, ReservedOutputTokens: 256},
	} {
		original := model.CloneRequest(request)
		calls := 0
		var seen runtimectx.TokenCountRequest
		manager := runtimectx.NewManager(runtimectx.Dependencies{Counter: exactCounter{tokens: 17, seen: &seen},
			Accounting: func(_ context.Context, header model.Request, _ runtimectx.ModelWindow) (runtimectx.AccountingProjection, error) {
				calls++
				if len(header.Messages) != 0 || header.InvocationID != "" || header.Model != request.Model ||
					header.BindingRef != request.BindingRef || len(header.Tools) != 1 {
					t.Fatal("projection received transcript/physical identity or lost accounting header")
				}
				// No output field can replace the original Tool, route or transcript.
				header.Tools[0].InputSchema[0] = '!'
				header.ModelOptions[0] = '!'
				header.Model = "other"
				return projection, nil
			}})
		frame, err := manager.FreezeRequestAccounting(t.Context(), request, window)
		requireNoError(t, err)
		encoded, err := json.Marshal(frame)
		requireNoError(t, err)
		if bytes.Contains(encoded, []byte("private provider prompt")) || bytes.Contains(encoded, []byte("provider_default")) || bytes.Contains(encoded, []byte("private>")) {
			t.Fatalf("private accounting material persisted: %s", encoded)
		}
		var recovered runtimectx.FrozenAccounting
		requireNoError(t, json.Unmarshal(encoded, &recovered))
		for _, messages := range [][]model.Message{
			nil,
			{{Role: model.RoleUser, Content: model.TextParts("a")}},
			{{Role: model.RoleSystem, Content: model.TextParts(privatePrompt)}, {Role: model.RoleUser, Content: model.TextParts("b")}},
			{{Role: model.RoleSystem, Content: []model.ContentPart{{Kind: model.ContentText, Text: privatePrompt[:7]}, {Kind: model.ContentText, Text: privatePrompt[7:]}}}},
			{{Role: model.RoleUser, Content: model.TextParts(privatePrompt)}},
			{{Role: model.RoleAssistant, ToolCalls: []tools.Call{{ID: "call", ToolKey: "read", Name: "read", Arguments: json.RawMessage(`{"x": "<>","n":9007199254740993}`)}},
				ProviderReplay: &model.ProviderReplay{Provider: "fixture", Format: "signed", Data: json.RawMessage(`{"proof": "signed <bytes>"}`)}}},
		} {
			candidate := model.CloneRequest(request)
			candidate.Messages = model.CloneMessages(messages)
			candidate.InvocationID = "new-id<>\""
			before := model.CloneRequest(candidate)
			originalBytes, err := model.MarshalRequestForInputCount(candidate)
			requireNoError(t, err)
			canonical, err := model.MarshalRequestForInputCount(materializedAccounting(candidate, projection))
			requireNoError(t, err)
			wantBytes := int64(max(len(originalBytes), len(canonical)))
			a, err := recovered.AssessModelRequest(candidate, window, policy)
			requireNoError(t, err)
			if a.TokenCountSource != runtimectx.CountEstimated || a.SerializedBytes != wantBytes ||
				a.RawTokenEstimate != int64((len(canonical)+3)/4) || a.HardTokenEstimate != (wantBytes*125+99)/100 {
				t.Fatalf("frozen frame differs from typed serialization: %+v, canonical=%d, original=%d", a, len(canonical), len(originalBytes))
			}
			exact, err := manager.AssessModelRequestWithAccounting(t.Context(), candidate, window, policy, frame)
			requireNoError(t, err)
			if exact.SerializedBytes != wantBytes || exact.TokenCountSource != runtimectx.CountExact || exact.HardTokenEstimate != 17 ||
				!bytes.Equal(seen.Payload, originalBytes) || seen.Context != window.TokenCountContext || !reflect.DeepEqual(candidate, before) {
				t.Fatal("frozen projection changed exact counter identity, evidence or original request")
			}
			if n, err := recovered.SerializedBytes(candidate); err != nil || n != wantBytes {
				t.Fatalf("recovered serialized bound disagrees: %d %v", n, err)
			}
		}
		if calls != 1 || !reflect.DeepEqual(request.ModelOptions, original.ModelOptions) || !reflect.DeepEqual(request.Tools, original.Tools) {
			t.Fatalf("projection repeated or changed the request: %d", calls)
		}
	}
}

func TestCanonicalAccountingFallbackAndMediaBoundaries(t *testing.T) {
	request := model.Request{RunID: "run", Model: "model", Messages: []model.Message{{Role: model.RoleUser, Content: model.TextParts("protected")}}}
	window := runtimectx.ModelWindow{ContextTokens: 4096, ReservedOutputTokens: 256,
		TokenCountContext: runtimectx.TokenCountContext{Protocol: "fixture", Model: "provider"}}
	policy := runtimectx.Policy{MaxInputTokens: 900, EstimateSafetyPercent: 25}
	project := func(context.Context, model.Request, runtimectx.ModelWindow) (runtimectx.AccountingProjection, error) {
		return runtimectx.AccountingProjection{SystemPrompt: strings.Repeat("p", 1200), ReservedOutputTokens: 256}, nil
	}
	for _, counter := range []runtimectx.TokenCounter{nil, exactCounter{tokens: 0}, failingAccountingCounter{}} {
		manager := runtimectx.NewManager(runtimectx.Dependencies{Counter: counter, Accounting: project})
		a, err := manager.AssessModelRequest(t.Context(), request, window, policy)
		requireNoError(t, err)
		if a.TokenCountSource != runtimectx.CountEstimated || runtimectx.WithinHardBudget(a, policy) {
			t.Fatalf("canonical input escaped fallback: %+v", a)
		}
		media := model.CloneRequest(request)
		media.Messages[0].Content = []model.ContentPart{{Kind: model.ContentImage, MediaType: "image/png", Source: &model.MediaSource{Kind: model.MediaSourceURL, URL: "https://example.test/a.png"}}}
		if _, err = manager.AssessModelRequest(t.Context(), media, window, policy); !errors.Is(err, runtimectx.ErrModelWindowUnknown) {
			t.Fatalf("media was admitted without exact counter: %v", err)
		}
		exact := runtimectx.NewManager(runtimectx.Dependencies{Counter: exactCounter{tokens: 100}, Accounting: project})
		a, err = exact.AssessModelRequest(t.Context(), media, window, policy)
		requireNoError(t, err)
		if a.TokenCountSource != runtimectx.CountExact || a.HardTokenEstimate != 100 || a.SerializedBytes < 1200 {
			t.Fatalf("exact media or canonical byte accounting lost: %+v", a)
		}
	}
}

type failingAccountingCounter struct{}

func (failingAccountingCounter) Count(context.Context, runtimectx.TokenCountRequest) (int64, error) {
	return 0, errors.New("counter unavailable")
}

func TestCanonicalAccountingRejectsInvalidProjectionAndFrame(t *testing.T) {
	request := model.Request{RunID: "run", BindingRef: "route", Model: "model",
		Messages: []model.Message{{Role: model.RoleUser, Content: model.TextParts("protected")}}}
	window := runtimectx.ModelWindow{ContextTokens: 4096, ReservedOutputTokens: 256}
	for _, projection := range []runtimectx.AccountingProjection{
		{ReservedOutputTokens: -1}, {ReservedOutputTokens: 257},
		{ReservedOutputTokens: 256, ModelOptions: json.RawMessage(`{`)},
		{ReservedOutputTokens: 256, SystemPrompt: string([]byte{0xff})},
	} {
		manager := runtimectx.NewManager(runtimectx.Dependencies{Accounting: func(context.Context, model.Request, runtimectx.ModelWindow) (runtimectx.AccountingProjection, error) {
			return projection, nil
		}})
		if _, err := manager.AssessModelRequest(t.Context(), request, window, runtimectx.Policy{}); !errors.Is(err, runtimectx.ErrInvalidInput) {
			t.Fatalf("invalid accounting projection accepted: %v", err)
		}
	}
	unavailable := errors.New("gateway policy unavailable")
	manager := runtimectx.NewManager(runtimectx.Dependencies{Accounting: func(context.Context, model.Request, runtimectx.ModelWindow) (runtimectx.AccountingProjection, error) {
		return runtimectx.AccountingProjection{}, unavailable
	}})
	if _, err := manager.AssessModelRequest(t.Context(), request, window, runtimectx.Policy{}); !errors.Is(err, unavailable) {
		t.Fatalf("projection failure silently fell back: %v", err)
	}
	frame, err := runtimectx.NewManager(runtimectx.Dependencies{}).FreezeRequestAccounting(t.Context(), request, window)
	requireNoError(t, err)
	changed := model.CloneRequest(request)
	changed.BindingRef = "other"
	if _, err := frame.SerializedBytes(changed); !errors.Is(err, runtimectx.ErrInvalidInput) {
		t.Fatal("frame reused for another route")
	}
	otherWindow := window
	otherWindow.ReservedOutputTokens++
	if _, err := frame.AssessModelRequest(request, otherWindow, runtimectx.Policy{}); !errors.Is(err, runtimectx.ErrInvalidInput) {
		t.Fatal("frame reused for another window")
	}
}
