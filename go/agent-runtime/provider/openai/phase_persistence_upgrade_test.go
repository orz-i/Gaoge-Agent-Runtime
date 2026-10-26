package openai_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/provider/openai"
)

// Simulates the same JSON representation the Agent commits inside its opaque
// persisted Feature State. Do not "repair" mismatched phase projections on
// restart: that would invent lost provider history or regenerate settled work.
func persistedAssistant(t *testing.T) model.Message {
	t.Helper()
	history := []model.Message{{
		Role:    model.RoleAssistant,
		Content: `{"ok":true}`,
		ProviderReplay: &model.ProviderReplay{
			Provider: "openai", Format: "responses.output.v1",
			Data: json.RawMessage("[" + phaseCommentaryItem + "," + phaseFinalItem + "]"),
		},
	}}
	b, err := json.Marshal(history)
	if err != nil {
		t.Fatal(err)
	}
	var restored []model.Message
	if err = json.Unmarshal(b, &restored); err != nil || len(restored) != 1 {
		t.Fatalf("invalid persisted transcript: %v", err)
	}
	return restored[0]
}

func TestRestoredPhaseHistoryRejectsLegacyFlatteningBeforeHTTP(t *testing.T) {
	physicalCalls := 0
	client := newClient(t, func(w http.ResponseWriter, _ *http.Request) {
		physicalCalls++
		writePhaseResponse(w, phaseResponse(phaseFinalItem), false)
	})
	base := persistedAssistant(t)
	invalid := []struct {
		name string
		edit func(*model.Message)
	}{
		{name: "old-flattened-content", edit: func(m *model.Message) { m.Content = "Working." + m.Content }},
		{name: "unrecognized-format", edit: func(m *model.Message) { m.ProviderReplay.Format = "responses.output.legacy" }},
		{name: "phase-erased", edit: func(m *model.Message) {
			m.ProviderReplay.Data = json.RawMessage(strings.Replace(string(m.ProviderReplay.Data), `"phase":"final_answer"`, `"phase":"commentary"`, 1))
		}},
		{name: "missing-final", edit: func(m *model.Message) {
			m.ProviderReplay.Data = json.RawMessage("[" + phaseCommentaryItem + "]")
		}},
	}
	for _, test := range invalid {
		t.Run(test.name, func(t *testing.T) {
			msg := model.CloneRequest(model.Request{Messages: []model.Message{base}}).Messages[0]
			test.edit(&msg)
			req := model.Request{
				Model:    "gpt-4.1",
				Messages: []model.Message{{Role: model.RoleUser, Content: "first"}, msg, {Role: model.RoleUser, Content: "next"}},
			}
			_, err := client.Generate(context.Background(), req)
			if !errors.Is(err, openai.ErrUnsupported) || physicalCalls != 0 {
				t.Fatalf("persisted mismatched replay dispatched: err=%v requests=%d", err, physicalCalls)
			}
		})
	}
	// A verified persisted phase envelope remains usable after restore, with
	// zero fallback or wire-schema migration.
	validReq := model.Request{
		Model:    "gpt-4.1",
		Messages: []model.Message{{Role: model.RoleUser, Content: "first"}, base, {Role: model.RoleUser, Content: "next"}},
	}
	result, err := client.Generate(t.Context(), validReq)
	if err != nil || result.Content != `{"ok":true}` || physicalCalls != 1 {
		t.Fatalf("legitimate phase replay could not resume: %+v err=%v dispatched=%d", result, err, physicalCalls)
	}
}
