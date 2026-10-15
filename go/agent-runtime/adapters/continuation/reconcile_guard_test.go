package continuationadapter_test

import (
	"context"
	"encoding/json"
	"testing"

	continuationadapter "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/adapters/continuation"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/runrelation"
)

func TestReconcileGuardRecognizesConsumedFirstPartyChildren(t *testing.T) {
	tests := []struct {
		name         string
		kind         kernel.RunKind
		relationKind runrelation.Kind
		state        string
		want         bool
	}{
		{"team consumed", "team", runrelation.KindTeamMember, "{\"members\":[{\"delegation\":{\"childRunID\":\"child\",\"status\":\"completed\"}}]}", false},
		{"team pending", "team", runrelation.KindTeamMember, "{\"members\":[{\"delegation\":{\"childRunID\":\"child\",\"status\":\"running\"}}]}", true},
		{"groupchat consumed", "group_chat", runrelation.KindGroupChatSpeaker, "{\"speakers\":[{\"turn\":{\"childRunID\":\"child\",\"status\":\"completed\"}}]}", false},
		{"groupchat pending", "group_chat", runrelation.KindGroupChatSpeaker, "{\"speakers\":[{\"turn\":{\"childRunID\":\"child\",\"status\":\"running\"}}]}", true},
		{"workflow consumed", "workflow", runrelation.KindWorkflowEffect, "{\"effects\":[{\"childRunID\":\"child\",\"status\":\"completed\"}]}", false},
		{"workflow pending", "workflow", runrelation.KindWorkflowEffect, "{\"effects\":[{\"childRunID\":\"child\",\"status\":\"pending\"}]}", true},
		{"unknown feature stays eligible", "custom", runrelation.KindCapability, "{}", true},
		{"unmatched child stays eligible", "team", runrelation.KindTeamMember, "{\"members\":[]}", true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			parent := kernel.Snapshot{Run: kernel.Run{Kind: test.kind}, State: json.RawMessage(test.state)}
			needed, err := continuationadapter.ReconcileGuard(
				context.Background(), parent,
				runrelation.Relation{ChildRunID: "child", Kind: test.relationKind},
			)
			if err != nil || needed != test.want {
				t.Fatalf("needed=%v want=%v error=%v", needed, test.want, err)
			}
		})
	}
}
