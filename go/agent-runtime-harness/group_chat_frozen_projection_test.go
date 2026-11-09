package harness_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/groupchat"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

var errGroupChatFrozenFirstNoEffect = errors.New("group chat first attempt rejected before effect")

type captureFrozenGroupChat struct {
	runtime *kernel.Runtime
	starts  []groupchat.StartRequest
}

func (feature *captureFrozenGroupChat) StartRun(ctx context.Context, request groupchat.StartRequest) (kernel.Snapshot, error) {
	feature.starts = append(feature.starts, request)
	if err := groupchat.ValidateStartRequest(request); err != nil {
		return kernel.Snapshot{}, err
	}
	if len(feature.starts) == 1 {
		return kernel.Snapshot{}, harness.FeatureStartNoEffect(errGroupChatFrozenFirstNoEffect)
	}
	return completeFeatureRun(ctx, feature.runtime, request.ID, groupchat.RunKind,
		request.Actor, request.Thread, request.RequestID, request.Goal)
}

func (feature *captureFrozenGroupChat) Resume(ctx context.Context, runID string, _ uint64) (kernel.Snapshot, error) {
	return feature.runtime.Load(ctx, runID)
}

func TestGroupChatFrozenFirstStartAndRetryUseIdenticalPolicyProjection(t *testing.T) {
	for _, policy := range []groupchat.SpeakerPolicy{
		groupchat.SpeakerDirected, groupchat.SpeakerSelector, groupchat.SpeakerHandoff,
	} {
		t.Run(string(policy), func(t *testing.T) {
			runtime := newFeatureInvocationRuntime(t)
			feature := &captureFrozenGroupChat{runtime: runtime}
			request := harness.GroupChatTurnRequest{
				StartRequest:  testStartRequest(),
				SpeakerPolicy: policy,
				Participants: []groupchat.Participant{
					{ID: "writer", Description: "Original writer"},
					{ID: "reviewer", Description: "Original reviewer"},
				},
				Speakers: []harness.GroupChatSpeakerSelection{
					{ParticipantID: "writer", RoleID: "writer-role",
						AuthorKind: "assistant", AuthorID: "writer", AuthorRevision: "writer-v1", AuthorName: "Writer"},
					{ParticipantID: "reviewer", RoleID: "reviewer-role",
						AuthorKind: "assistant", AuthorID: "reviewer", AuthorRevision: "reviewer-v1", AuthorName: "Reviewer"},
				},
				MaxUtterances: 2,
			}
			request.Config.Model = "original-selector-model"
			request.Config.ToolKeys = []string{"source.read"}
			request.Config.ModelOptions = json.RawMessage(`{"temperature":0.25}`)
			request.Config.Roles = []harness.RoleSnapshot{
				{ID: "writer-role", Revision: 1, Name: "Original Writer", Model: "writer-model",
					ToolKeys: []string{"source.read"}},
				{ID: "reviewer-role", Revision: 1, Name: "Original Reviewer", Model: "reviewer-model",
					ToolKeys: []string{"source.read"}},
			}
			// The Store commits the original Invocation, then the caller mutates
			// every live host field that formerly appeared in a second Runtime
			// request. Neither first physical Start nor retry may see it.
			store := &auditMutateAfterInvocation{Store: harness.NewMemoryStore(), onCreate: func() {
				request.Goal = "tampered goal"
				request.Participants[0].ID = "tampered-writer"
				request.Speakers[0].AuthorName = "Tampered Author"
				request.Config.Model = "tampered-selector"
				request.Config.ModelOptions = json.RawMessage(`{"temperature":0.9}`)
				request.Config.Roles[0].Name = "Tampered Role"
				request.Config.Roles[0].ToolKeys[0] = "unauthorized.tool"
			}}
			runner, err := harness.NewRunner(harness.Dependencies{
				Runtime: runtime, Agent: loadingFeatureAgent{runtime: runtime},
				Store: store, Clock: featureInvocationClock{}, GroupChats: feature,
			})
			if err != nil {
				t.Fatal(err)
			}
			first, firstErr := runner.StartGroupChatTurn(t.Context(), request)
			if !errors.Is(firstErr, errGroupChatFrozenFirstNoEffect) ||
				first.Turn.Status != harness.TurnFailed || len(feature.starts) != 1 {
				t.Fatalf("first GroupChat attempt not rejected as no-effect: status=%s err=%v calls=%d",
					first.Turn.Status, firstErr, len(feature.starts))
			}
			var original harness.Invocation
			for _, invocation := range first.Invocations {
				if invocation.ParentItemID == "" {
					original = invocation
				}
			}
			if original.ID == "" || original.Attempt != 1 || len(original.Input) == 0 {
				t.Fatalf("first frozen invocation missing: %+v", original)
			}
			retried, retryErr := runner.RetryInvocation(t.Context(), original.ID)
			if retryErr != nil || retried.Turn.Status != harness.TurnCompleted || len(feature.starts) != 2 {
				t.Fatalf("GroupChat retry did not start with original frozen policy: status=%s err=%v calls=%d",
					retried.Turn.Status, retryErr, len(feature.starts))
			}
			firstSent, secondSent := feature.starts[0], feature.starts[1]
			if firstSent.ID == secondSent.ID || firstSent.RequestID == secondSent.RequestID {
				t.Fatalf("retry did not mint an attempt-specific physical identity: first=%q second=%q", firstSent.ID, secondSent.ID)
			}
			firstSent.ID, firstSent.RequestID = "", ""
			secondSent.ID, secondSent.RequestID = "", ""
			if !reflect.DeepEqual(firstSent, secondSent) ||
				firstSent.Participants[0].ID != "writer" ||
				firstSent.SpeakerConfigs[0].AuthorName != "Writer" ||
				firstSent.SpeakerConfigs[0].Model != "writer-model" ||
				firstSent.Goal == "tampered goal" ||
				firstSent.SelectorModel == "tampered-selector" {
				t.Fatalf("first/retried policy drift from the original frozen Invocation: first=%+v second=%+v", firstSent, secondSent)
			}
			switch policy {
			case groupchat.SpeakerDirected:
				if !reflect.DeepEqual(firstSent.DirectedSpeakerIDs, []string{"writer", "reviewer"}) ||
					len(firstSent.CandidateSpeakerIDs) != 0 || firstSent.SelectorModel != "" {
					t.Fatalf("directed policy changed: %+v", firstSent)
				}
			case groupchat.SpeakerSelector:
				if firstSent.SelectorModel != "original-selector-model" ||
					string(firstSent.SelectorModelOptions) != `{"temperature":0.25}` ||
					!reflect.DeepEqual(firstSent.CandidateSpeakerIDs, []string{"writer", "reviewer"}) {
					t.Fatalf("selector policy changed: %+v", firstSent)
				}
			case groupchat.SpeakerHandoff:
				if len(firstSent.DirectedSpeakerIDs) != 0 ||
					len(firstSent.CandidateSpeakerIDs) != 2 ||
					!containsString(firstSent.SpeakerConfigs[0].ToolKeys, harness.GroupChatHandoffToolKey) ||
					containsString(firstSent.SpeakerConfigs[0].ToolKeys, "unauthorized.tool") {
					t.Fatalf("handoff policy changed: %+v", firstSent)
				}
			}
		})
	}
}

func TestGroupChatInvalidPolicyRejectedBeforePhysicalStart(t *testing.T) {
	for _, tc := range []struct {
		name     string
		policy   groupchat.SpeakerPolicy
		model    string
		speakers int
	}{
		{"selector-without-model", groupchat.SpeakerSelector, "", 2},
		{"handoff-single-participant", groupchat.SpeakerHandoff, "model", 1},
		{"unknown-policy", groupchat.SpeakerPolicy("future-policy"), "model", 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runtime := newFeatureInvocationRuntime(t)
			feature := &captureFrozenGroupChat{runtime: runtime}
			request := harness.GroupChatTurnRequest{
				StartRequest: testStartRequest(), SpeakerPolicy: tc.policy, MaxUtterances: 2,
				Participants: []groupchat.Participant{{ID: "writer"}, {ID: "reviewer"}},
				Speakers: []harness.GroupChatSpeakerSelection{
					{ParticipantID: "writer", RoleID: "writer-role", AuthorKind: "assistant",
						AuthorID: "writer", AuthorRevision: "1", AuthorName: "Writer"},
					{ParticipantID: "reviewer", RoleID: "reviewer-role", AuthorKind: "assistant",
						AuthorID: "reviewer", AuthorRevision: "1", AuthorName: "Reviewer"},
				},
			}
			request.Config.Model = tc.model
			request.Config.Roles = []harness.RoleSnapshot{
				{ID: "writer-role", Revision: 1, Name: "Writer", Model: "model"},
				{ID: "reviewer-role", Revision: 1, Name: "Reviewer", Model: "model"},
			}
			request.Participants = request.Participants[:tc.speakers]
			request.Speakers = request.Speakers[:tc.speakers]
			store := harness.NewMemoryStore()
			runner, err := harness.NewRunner(harness.Dependencies{
				Runtime: runtime, Agent: loadingFeatureAgent{runtime: runtime},
				Store: store, Clock: featureInvocationClock{}, GroupChats: feature,
			})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = runner.StartGroupChatTurn(t.Context(), request); !errors.Is(err, harness.ErrInvalidRequest) {
				t.Fatalf("unverified policy accepted before durable admission: %v", err)
			}
			if len(feature.starts) != 0 {
				t.Fatalf("invalid policy triggered %d physical GroupChat calls", len(feature.starts))
			}
		})
	}
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
