package harness

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
)

func TestHostSelectedMemberModelOptionsAreNotInheritedAcrossModels(t *testing.T) {
	options := json.RawMessage(`{"reasoning":{"effort":"high"}}`)
	f := newPR82HandoffFixture(t, agent.StartRequest{Model: "parent-model", ModelOptions: options})
	now := pr82HandoffClock{}.Now()
	config, err := SealConfigSnapshot(f.turn.ID, ConfigSnapshot{
		AdmissionHash: strings.Repeat("a", 64),
		Model:         "parent-model", ModelOptions: options,
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = f.store.PutConfigSnapshot(t.Context(), config); err != nil {
		t.Fatal(err)
	}
	turn := f.turn
	turn.ConfigSnapshotID = config.ID

	for _, tc := range []struct {
		name        string
		child       agent.StartRequest
		wantModel   string
		wantOptions json.RawMessage
	}{
		{"same-model-inherits-options", agent.StartRequest{}, "parent-model", options},
		{"different-model-has-own-options", agent.StartRequest{Model: "specialist-model", ModelOptions: json.RawMessage(`{"temperature":0.1}`)}, "specialist-model", json.RawMessage(`{"temperature":0.1}`)},
		{"different-model-does-not-inherit", agent.StartRequest{Model: "specialist-model"}, "specialist-model", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			scope, _, err := f.runner.prepareDelegation(t.Context(), turn, f.parent, DelegateRequest{
				MemberID: "specialist", Goal: "inspect", Agent: tc.child,
			})
			if err != nil || scope.Model != tc.wantModel || !bytes.Equal(scope.ModelOptions, tc.wantOptions) {
				t.Fatalf("wrong frozen model scope: model=%q options=%s err=%v", scope.Model, scope.ModelOptions, err)
			}
		})
	}
}
