package groupchat_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/groupchat"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/handoff"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/runrelation"
)

func TestDirectedGroupChatExecutesVisibleSpeakersInOrder(t *testing.T) {
	runtime, err := kernel.New(kernel.Dependencies{
		Store: memory.NewStore(), Clock: groupChatClock{}, IDs: &groupChatIDs{},
	})
	if err != nil {
		t.Fatal(err)
	}
	delegator := &recordingDelegator{}
	relations, err := runrelation.New(memory.NewRunRelationStore(), groupChatClock{})
	if err != nil {
		t.Fatal(err)
	}
	runner, err := groupchat.NewRunner(groupchat.Dependencies{
		Runtime: runtime, Handoffs: delegator, Relations: relations,
	})
	if err != nil {
		t.Fatal(err)
	}

	snapshot, err := runner.StartRun(t.Context(), directedRequest())
	if err != nil {
		t.Fatalf("start group chat: %v", err)
	}
	if snapshot.Run.Status != kernel.RunStatusCompleted || snapshot.Result == nil {
		t.Fatalf("unexpected snapshot: %#v", snapshot)
	}
	if len(delegator.goals) != 2 {
		t.Fatalf("goals = %#v", delegator.goals)
	}
	if !strings.Contains(delegator.goals[1], "[Speaker: Researcher]") ||
		!strings.Contains(delegator.goals[1], "answer-researcher") {
		t.Fatalf("second speaker did not receive visible transcript: %q", delegator.goals[1])
	}

	var result groupchat.Result
	if err = json.Unmarshal(snapshot.Result.Content, &result); err != nil {
		t.Fatal(err)
	}
	if result.Kind != groupchat.ResultKind || len(result.Utterances) != 2 {
		t.Fatalf("result = %#v", result)
	}
	if result.Utterances[0].SpeakerID != "participant-researcher" ||
		result.Utterances[1].SpeakerID != "participant-reviewer" {
		t.Fatalf("utterances = %#v", result.Utterances)
	}
	items, err := relations.ListChildren(t.Context(), snapshot.Run.ID)
	if err != nil || len(items) != 2 {
		t.Fatalf("relations=%#v err=%v", items, err)
	}
	for _, item := range items {
		if item.Kind != runrelation.KindGroupChatSpeaker {
			t.Fatalf("relation kind = %q", item.Kind)
		}
	}
}

func directedRequest() groupchat.StartRequest {
	return groupchat.StartRequest{
		ID:            "groupchat-1",
		Actor:         kernel.ActorRef{TenantID: "tenant", ActorID: "actor"},
		Thread:        kernel.ThreadRef{Kind: "conversation", ID: "conversation-1"},
		RequestID:     "request-1",
		Goal:          "Compare the evidence.",
		SpeakerPolicy: groupchat.SpeakerDirected,
		Participants: []groupchat.Participant{
			{ID: "participant-researcher", Description: "Find evidence."},
			{ID: "participant-reviewer", Description: "Review evidence."},
		},
		SpeakerConfigs: []groupchat.SpeakerConfig{
			{ParticipantID: "participant-researcher", RoleID: "17", RoleRevision: 3, RoleName: "Researcher"},
			{ParticipantID: "participant-reviewer", RoleID: "23", RoleRevision: 5, RoleName: "Reviewer"},
		},
		DirectedSpeakerIDs: []string{"participant-researcher", "participant-reviewer"},
		MaxUtterances:      2,
	}
}

type recordingDelegator struct {
	goals []string
}

func (delegator *recordingDelegator) StartOrLoad(
	_ context.Context,
	_ kernel.Snapshot,
	delegation handoff.Delegation,
) (handoff.Delegation, error) {
	delegator.goals = append(delegator.goals, delegation.Goal)
	delegation.Status = handoff.StatusCompleted
	delegation.Result = json.RawMessage(fmt.Sprintf(`{"content":"answer-%s"}`, strings.TrimPrefix(delegation.MemberID, "participant-")))
	return delegation, nil
}

type groupChatClock struct{}

func (groupChatClock) Now() time.Time {
	return time.Date(2026, time.September, 24, 0, 0, 0, 0, time.UTC)
}

type groupChatIDs struct {
	next int
}

func (ids *groupChatIDs) NewID(prefix string) (string, error) {
	ids.next++
	return fmt.Sprintf("%s-%d", prefix, ids.next), nil
}
