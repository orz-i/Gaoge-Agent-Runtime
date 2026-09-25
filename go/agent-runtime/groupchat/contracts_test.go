package groupchat_test

import (
	"errors"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/groupchat"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

func validRequest() groupchat.StartRequest {
	return groupchat.StartRequest{
		ID:            "group-run-1",
		Actor:         kernel.ActorRef{TenantID: "tenant", ActorID: "actor"},
		Thread:        kernel.ThreadRef{Kind: "conversation", ID: "conversation-1"},
		RequestID:     "request-1",
		Goal:          "Compare the evidence in the shared conversation.",
		SpeakerPolicy: groupchat.SpeakerDirected,
		Participants: []groupchat.Participant{
			{ID: "researcher", Description: "Find evidence."},
			{ID: "reviewer", Description: "Review evidence."},
		},
		DirectedSpeakerIDs: []string{"researcher", "reviewer"},
		MaxUtterances:      2,
	}
}

func TestValidateStartRequestAcceptsDirectedVisibleSpeakers(t *testing.T) {
	if err := groupchat.ValidateStartRequest(validRequest()); err != nil {
		t.Fatalf("validate directed group chat: %v", err)
	}
}

func TestValidateStartRequestRejectsUnknownOrDuplicateSpeaker(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*groupchat.StartRequest)
	}{
		{
			name: "unknown",
			mutate: func(request *groupchat.StartRequest) {
				request.DirectedSpeakerIDs = []string{"researcher", "writer"}
			},
		},
		{
			name: "duplicate",
			mutate: func(request *groupchat.StartRequest) {
				request.DirectedSpeakerIDs = []string{"researcher", "researcher"}
			},
		},
		{
			name: "over limit",
			mutate: func(request *groupchat.StartRequest) {
				request.MaxUtterances = 1
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := validRequest()
			test.mutate(&request)
			if err := groupchat.ValidateStartRequest(request); !errors.Is(err, groupchat.ErrInvalidRequest) {
				t.Fatalf("expected invalid request, got %v", err)
			}
		})
	}
}

func TestValidateStartRequestAcceptsSelectorCandidates(t *testing.T) {
	request := validRequest()
	request.SpeakerPolicy = groupchat.SpeakerSelector
	request.DirectedSpeakerIDs = nil
	request.CandidateSpeakerIDs = []string{"researcher", "reviewer"}
	request.SelectorModel = "selector-model"
	request.MaxUtterances = 4
	if err := groupchat.ValidateStartRequest(request); err != nil {
		t.Fatalf("validate selector group chat: %v", err)
	}
}

func TestValidateStartRequestAcceptsHandoffCandidates(t *testing.T) {
	request := validRequest()
	request.SpeakerPolicy = groupchat.SpeakerHandoff
	request.DirectedSpeakerIDs = nil
	request.CandidateSpeakerIDs = []string{"researcher", "reviewer"}
	request.MaxUtterances = 4
	if err := groupchat.ValidateStartRequest(request); err != nil {
		t.Fatalf("validate handoff group chat: %v", err)
	}
}

func TestValidateStartRequestRejectsSingleHandoffCandidate(t *testing.T) {
	request := validRequest()
	request.SpeakerPolicy = groupchat.SpeakerHandoff
	request.DirectedSpeakerIDs = nil
	request.CandidateSpeakerIDs = []string{"researcher"}
	if err := groupchat.ValidateStartRequest(request); !errors.Is(err, groupchat.ErrInvalidRequest) {
		t.Fatalf("single-candidate handoff must be invalid, got %v", err)
	}
}

func TestValidateStartRequestRejectsAmbiguousSelectorContract(t *testing.T) {
	request := validRequest()
	request.SpeakerPolicy = groupchat.SpeakerSelector
	request.CandidateSpeakerIDs = []string{"researcher", "reviewer"}
	request.SelectorModel = "selector-model"
	if err := groupchat.ValidateStartRequest(request); !errors.Is(err, groupchat.ErrInvalidRequest) {
		t.Fatalf("selector with directed speakers must be invalid, got %v", err)
	}
}
