package provider_test

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-provider/anthropic"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-provider/openai"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

// Frozen pre-effort adapter layout. A new optional field must not change the
// HMAC material of a pending Run that never selected the new feature.
type historicalBindingInput struct {
	Protocol               string
	Endpoint               string
	DefaultModel           string
	BindingScope           string
	MaxOutputTokens        int64
	StructuredOutputModels []string
}

func historicalBindingHMAC(key string, input historicalBindingInput) string {
	input.StructuredOutputModels = slices.Clone(input.StructuredOutputModels)
	slices.Sort(input.StructuredOutputModels)
	bytes, _ := json.Marshal(input)
	mac := hmac.New(sha256.New, []byte(key))
	_, _ = mac.Write([]byte("gaoge-provider-binding.v1\x00"))
	_, _ = mac.Write(bytes)
	return hex.EncodeToString(mac.Sum(nil))
}

func TestPendingNamedProviderBindingResumesOnlyItsOriginalCatalogSnapshot(t *testing.T) {
	for _, protocol := range []string{"openai", "anthropic"} {
		for _, unsent := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/unsent=%t", protocol, unsent), func(t *testing.T) {
				physical := 0
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					physical++
					if physical == 1 && !unsent {
						w.WriteHeader(http.StatusServiceUnavailable)
						_, _ = fmt.Fprint(w, `{"error":{"code":"server_error","message":"fixture unavailable"}}`)
						return
					}
					w.Header().Set("Content-Type", "text/event-stream")
					if protocol == "openai" {
						_, _ = fmt.Fprint(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"object\":\"response\",\"id\":\"historical-openai\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":\"Done\"}]}],\"usage\":{\"input_tokens\":2,\"output_tokens\":1}}}\n\n")
						return
					}
					for _, e := range []struct{ name, data string }{
						{"message_start", `{"type":"message_start","message":{"type":"message","role":"assistant","id":"historical-anthropic","usage":{"input_tokens":2}}}`},
						{"content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":"Done"}}`},
						{"content_block_stop", `{"type":"content_block_stop","index":0}`},
						{"message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}`},
						{"message_stop", `{"type":"message_stop"}`},
					} {
						_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", e.name, e.data)
					}
				}))
				defer server.Close()
				const key = "durable-fixture-key"
				profiles := model.StaticModelProfiles{"fixture-model": {StructuredJSONSchema: true}}
				newModel := func() model.Client {
					t.Helper()
					if protocol == "openai" {
						client, err := openai.New(openai.Config{APIKey: key, DefaultModel: "fixture-model", Endpoint: server.URL, ModelProfiles: profiles})
						if err != nil {
							t.Fatal(err)
						}
						return client
					}
					client, err := anthropic.New(anthropic.Config{APIKey: key, DefaultModel: "fixture-model", Endpoint: server.URL, ModelProfiles: profiles})
					if err != nil {
						t.Fatal(err)
					}
					return client
				}
				now := &providerRetryClock{now: time.Date(2026, 11, 5, 14, 0, 0, 0, time.UTC)}
				base := memory.NewStoreWithExecutionClock(now)
				store := &providerUnsentStore{Store: base, runID: "historical-" + protocol}
				if !unsent {
					store.hits = 1
				}
				build := func() (*kernel.Runtime, *agent.Runner) {
					rt, err := kernel.New(kernel.Dependencies{Store: store, Clock: now})
					if err != nil {
						t.Fatal(err)
					}
					runner, err := agent.NewRunner(agent.Dependencies{Runtime: rt, Model: newModel(), Clock: now})
					if err != nil {
						t.Fatal(err)
					}
					return rt, runner
				}
				runtime, runner := build()
				request := agent.StartRequest{
					ID: "historical-" + protocol, Model: "fixture-model", Goal: "answer once",
					Actor:  kernel.ActorRef{TenantID: "fixture", ActorID: "owner"},
					Thread: kernel.ThreadRef{Kind: "conversation", ID: "old-thread"},
				}
				first, err := runner.StartRun(t.Context(), request)
				if err == nil || !model.IsRetryableError(err) || (unsent && (physical != 0 || store.hits != 1)) || (!unsent && physical != 1) {
					t.Fatalf("did not commit retryable pending intent: physical=%d run=%+v err=%v", physical, first.Run, err)
				}
				physicalErr := err
				persisted, err := runtime.Load(t.Context(), request.ID)
				if err != nil {
					t.Fatal(err)
				}
				view, err := agent.ViewState(persisted)
				if err != nil || len(view.ModelInvocations) != 1 || view.ModelInvocations[0].Status != agent.ModelInvocationPending {
					t.Fatalf("original intent not pending: %+v %v", view, err)
				}
				active := view.ModelInvocations[0]
				assertProviderPendingSource(t, persisted.Run.ID, active)
				previous := historicalBindingInput{Endpoint: server.URL, DefaultModel: "fixture-model"}
				if protocol == "openai" {
					previous.Protocol = "openai.responses.v1"
				} else {
					previous.Protocol = "anthropic.messages.2023-06-01"
					previous.MaxOutputTokens = 4096
				}
				if active.BindingFingerprint == historicalBindingHMAC(key, previous) {
					t.Fatalf("new profile authority reused incompatible beta.24 HMAC: %s", active.BindingFingerprint)
				}
				binder, ok := newModel().(model.RequestBindingFingerprinter)
				if !ok || active.BindingFingerprint != binder.BindingFingerprintForRequest(model.Request{Model: request.Model}) {
					t.Fatal("new pending intent is not bound to the original normalized Host catalog")
				}
				if !unsent {
					profiles["fixture-model"] = model.Capabilities{StructuredJSONSchema: false}
					// Unknown physical outcome takes precedence over changed binding.
					assertProviderUnknownNoReplay(t, base, now, agent.Dependencies{Model: newModel()}, first, physicalErr, func() int64 { return int64(physical) })
					return
				}
				if active.DispatchAttempt != 0 || active.ProviderResponseID != "" || active.Usage != nil ||
					!reflect.DeepEqual(active.Request, store.rejected.Request) || active.RequestHash != store.rejected.RequestHash ||
					active.ID != store.rejected.ID || active.SourceRevision != store.rejected.SourceRevision ||
					active.BindingFingerprint != store.rejected.BindingFingerprint || active.Provider != store.rejected.Provider ||
					!reflect.DeepEqual(active.CatalogFingerprints, store.rejected.CatalogFingerprints) || !active.CreatedAt.Equal(store.rejected.CreatedAt) {
					t.Fatalf("precommit rejection forged physical receipt: %+v", active)
				}
				if active.RetryAfter == nil {
					t.Fatal("pending retry lost durable due time")
				}
				now.now = active.RetryAfter.Add(time.Second)
				// External discovery adds an unrelated model after the first call.
				// The original selected model's frozen identity must still resume.
				profiles["newly-discovered-model"] = model.Capabilities{StructuredJSONSchema: true}
				if binder.BindingFingerprintForRequest(model.Request{Model: request.Model}) != newModel().(model.RequestBindingFingerprinter).BindingFingerprintForRequest(model.Request{Model: request.Model}) {
					t.Fatal("unrelated catalog update invalidated selected pending model binding")
				}
				// A changed SELECTED model policy must reject before physical HTTP,
				// preserving the original durable pending grant for later recovery.
				original := profiles["fixture-model"]
				profiles["fixture-model"] = model.Capabilities{StructuredJSONSchema: false}
				_, incompatible := build()
				if _, changedErr := incompatible.Resume(t.Context(), persisted.Run.ID, persisted.Run.Revision); !errors.Is(changedErr, agent.ErrProviderBindingMismatch) || physical != 0 {
					t.Fatalf("selected capability drift crossed original physical binding: %v calls=%d", changedErr, physical)
				}
				unchanged, loadErr := runtime.Load(t.Context(), persisted.Run.ID)
				if loadErr != nil || !reflect.DeepEqual(persisted, unchanged) {
					t.Fatalf("binding rejection changed original intent: %v", loadErr)
				}
				profiles["fixture-model"] = original
				_, resumed := build()
				done, err := resumed.Resume(t.Context(), persisted.Run.ID, persisted.Run.Revision)
				if errors.Is(err, agent.ErrProviderBindingMismatch) || err != nil || physical != 1 ||
					done.Run.Status != kernel.RunStatusCompleted {
					t.Fatalf("historical pending run could not resume: err=%v physical=%d status=%s", err, physical, done.Run.Status)
				}
				final, err := agent.ViewState(done)
				if err != nil || len(final.ModelInvocations) != 1 || final.ModelInvocations[0].ID != active.ID ||
					final.ModelInvocations[0].Status != agent.ModelInvocationConsumed ||
					final.ModelInvocations[0].RunID != active.RunID || final.ModelInvocations[0].SourceRevision != active.SourceRevision ||
					final.ModelInvocations[0].RequestHash != active.RequestHash || final.ModelInvocations[0].BindingFingerprint != active.BindingFingerprint ||
					final.ModelInvocations[0].Provider != active.Provider || !final.ModelInvocations[0].CreatedAt.Equal(active.CreatedAt) ||
					final.ModelInvocations[0].ExecutionAttempt != 2 || final.ModelInvocations[0].DispatchAttempt != 2 ||
					final.ModelInvocations[0].ProviderResponseID != "historical-"+protocol ||
					final.ModelInvocations[0].Response.ResponseID != "historical-"+protocol ||
					final.ModelInvocations[0].Usage == nil || final.ModelInvocations[0].Usage.InputTokens != 2 || final.ModelInvocations[0].Usage.OutputTokens != 1 ||
					!reflect.DeepEqual(final.ModelInvocations[0].Usage, final.ModelInvocations[0].Response.Usage) ||
					final.ModelInvocations[0].Response.Content != "Done" || len(final.ModelInvocations[0].Response.ToolCalls) != 0 ||
					final.Budget.Usage.LLMCalls != 1 || final.Budget.Usage.ToolCalls != 0 || final.Budget.Usage.InputTokens != 2 || final.Budget.Usage.OutputTokens != 1 {
					t.Fatalf("same original logical ModelInvocation was not consumed: %+v err=%v", final, err)
				}
			})
		}
	}
}
