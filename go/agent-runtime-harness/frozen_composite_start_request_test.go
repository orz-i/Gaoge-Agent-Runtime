package harness

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/groupchat"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/team"
)

// Every physical Team/Workflow/GroupChat Start, including a new Attempt,
// projects from its original typed frozen input. The only authorized
// differences across attempts are the explicit Run/Actor/Thread/RequestID
// from the relevant root or child execution owner.
func TestFrozenCompositeStartRequestsChangeOnlyExecutionIdentity(t *testing.T) {
	actor := kernel.ActorRef{TenantID: "tenant", ActorID: "owner"}
	thread := kernel.ThreadRef{Kind: "conversation", ID: "thread"}
	foreignActor := kernel.ActorRef{TenantID: "foreign", ActorID: "foreign"}
	foreignThread := kernel.ThreadRef{Kind: "foreign", ID: "foreign"}

	t.Run("team", func(t *testing.T) {
		input := teamInvocationInput{
			Goal: "sealed team", Actor: foreignActor, Thread: foreignThread,
			Mode:    team.ExecutionSequential,
			Members: []team.Member{{ID: "writer", Goal: "sealed evidence"}},
		}
		first := input.startRequest("run-1", actor, thread, "root-request")
		retry := input.startRequest("run-2", actor, thread, "retry-request")
		if first.Actor != actor || first.Thread != thread ||
			first.ID != "run-1" || retry.ID != "run-2" ||
			first.RequestID != "root-request" || retry.RequestID != "retry-request" {
			t.Fatalf("root execution owner not explicit: first=%+v retry=%+v", first, retry)
		}
		first.ID, first.RequestID, retry.ID, retry.RequestID = "", "", "", ""
		if !reflect.DeepEqual(first, retry) {
			t.Fatalf("Team policy changed between attempts: first=%+v retry=%+v", first, retry)
		}
		input.Members[0].Goal = "mutated after projection"
		if first.Members[0].Goal != "sealed evidence" {
			t.Fatal("Team projection aliases source member slice")
		}
	})

	t.Run("workflow", func(t *testing.T) {
		input := workflowInvocationInput{Goal: "sealed workflow",
			Actor: foreignActor, Thread: foreignThread, Input: json.RawMessage(`{"source":"original"}`)}
		first := input.startRequest("run-1", actor, thread, "root-request")
		retry := input.startRequest("run-2", actor, thread, "retry-request")
		if first.Actor != actor || first.Thread != thread ||
			first.ID != "run-1" || retry.ID != "run-2" ||
			first.RequestID != "root-request" || retry.RequestID != "retry-request" {
			t.Fatalf("Workflow execution owner not explicit: first=%+v retry=%+v", first, retry)
		}
		first.ID, first.RequestID, retry.ID, retry.RequestID = "", "", "", ""
		if !reflect.DeepEqual(first, retry) {
			t.Fatalf("Workflow policy changed between attempts: first=%+v retry=%+v", first, retry)
		}
		input.Input[10] = 'X'
		if string(first.Input) != `{"source":"original"}` {
			t.Fatal("Workflow projection aliases source input payload")
		}
	})

	t.Run("groupchat", func(t *testing.T) {
		input := groupChatInvocationInput{
			Goal: "sealed chat", Actor: foreignActor, Thread: foreignThread,
			SpeakerPolicy: groupchat.SpeakerSelector, MaxUtterances: 2,
			Participants:         []groupchat.Participant{{ID: "writer"}, {ID: "reviewer"}},
			SpeakerConfigs:       []groupchat.SpeakerConfig{{ParticipantID: "writer", Model: "original"}},
			CandidateSpeakerIDs:  []string{"writer", "reviewer"},
			SelectorModel:        "sealed-selector",
			SelectorModelOptions: json.RawMessage(`{"temperature":0.5}`),
		}
		first := input.startRequest("run-1", actor, thread, "root-request")
		retry := input.startRequest("run-2", actor, thread, "retry-request")
		if first.Actor != actor || first.Thread != thread ||
			first.ID != "run-1" || retry.ID != "run-2" ||
			first.RequestID != "root-request" || retry.RequestID != "retry-request" {
			t.Fatalf("GroupChat execution owner not explicit: first=%+v retry=%+v", first, retry)
		}
		first.ID, first.RequestID, retry.ID, retry.RequestID = "", "", "", ""
		if !reflect.DeepEqual(first, retry) {
			t.Fatalf("GroupChat policy changed between attempts: first=%+v retry=%+v", first, retry)
		}
		input.Participants[0].ID = "tampered"
		input.SpeakerConfigs[0].Model = "tampered-model"
		input.CandidateSpeakerIDs[0] = "tampered"
		input.SelectorModelOptions[2] = 'X'
		if first.Participants[0].ID != "writer" ||
			first.SpeakerConfigs[0].Model != "original" ||
			first.CandidateSpeakerIDs[0] != "writer" ||
			string(first.SelectorModelOptions) != `{"temperature":0.5}` {
			t.Fatal("GroupChat projection aliases caller-owned source slices")
		}
	})
}
