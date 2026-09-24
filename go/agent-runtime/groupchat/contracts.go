package groupchat

import (
	"errors"
	"strings"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

const (
	// RunKind identifies the explicit Group Chat Feature.
	RunKind kernel.RunKind = "group_chat"

	MaxParticipantCount = 16
	MaxUtteranceCount   = 32
)

var ErrInvalidRequest = errors.New("invalid groupchat request")

// SpeakerPolicy controls how the next visible speaker is selected.
//
// P0 intentionally exposes only Directed. Selector and handoff policies require
// their own durable state transitions and are not compatibility aliases.
type SpeakerPolicy string

const (
	SpeakerDirected SpeakerPolicy = "directed"
)

// Participant identifies one host-authorized visible speaker. Runtime keeps
// only the neutral participant identity and description; product-owned Role
// configuration remains frozen by the hosting Harness.
type Participant struct {
	ID          string `json:"id"`
	Description string `json:"description,omitempty"`
}

// SpeakerTurnStatus is the durable lifecycle of one visible speaker execution.
type SpeakerTurnStatus string

const (
	SpeakerTurnQueued    SpeakerTurnStatus = "queued"
	SpeakerTurnRunning   SpeakerTurnStatus = "running"
	SpeakerTurnCompleted SpeakerTurnStatus = "completed"
	SpeakerTurnFailed    SpeakerTurnStatus = "failed"
	SpeakerTurnCancelled SpeakerTurnStatus = "cancelled"
)

// SpeakerTurn binds one ordered visible utterance slot to its stable Child Run.
// Conversation remains responsible for projecting the resulting content into a
// user-visible Message with an author snapshot.
type SpeakerTurn struct {
	Ordinal    int               `json:"ordinal"`
	SpeakerID  string            `json:"speakerID"`
	ChildRunID string            `json:"childRunID,omitempty"`
	Status     SpeakerTurnStatus `json:"status"`
	ErrorCode  string            `json:"errorCode,omitempty"`
}

// TerminationReason records why Group Chat stopped selecting speakers.
type TerminationReason string

const (
	TerminationCompleted     TerminationReason = "completed"
	TerminationMaxUtterances TerminationReason = "max_utterances"
	TerminationCancelled     TerminationReason = "cancelled"
	TerminationFailed        TerminationReason = "failed"
)

// View is the public durable Group Chat state. It contains execution topology,
// not host-owned visible message bodies.
type View struct {
	SpeakerPolicy      SpeakerPolicy     `json:"speakerPolicy"`
	Participants       []Participant     `json:"participants"`
	DirectedSpeakerIDs []string          `json:"directedSpeakerIDs,omitempty"`
	SpeakerTurns       []SpeakerTurn     `json:"speakerTurns"`
	NextSpeakerIndex   int               `json:"nextSpeakerIndex"`
	TerminationReason  TerminationReason `json:"terminationReason,omitempty"`
}

// StartRequest creates one explicit Group Chat Run.
type StartRequest struct {
	ID                 string
	Actor              kernel.ActorRef
	Thread             kernel.ThreadRef
	RequestID          string
	Goal               string
	SpeakerPolicy      SpeakerPolicy
	Participants       []Participant
	DirectedSpeakerIDs []string
	MaxUtterances      int
}

// ValidateStartRequest validates only the stable P0 public contract. Runtime
// execution is introduced separately so callers cannot mistake declaration for
// an available compatibility path.
func ValidateStartRequest(request StartRequest) error {
	if strings.TrimSpace(request.ID) == "" ||
		strings.TrimSpace(request.Actor.TenantID) == "" ||
		strings.TrimSpace(request.Actor.ActorID) == "" ||
		strings.TrimSpace(request.Thread.Kind) == "" ||
		strings.TrimSpace(request.Thread.ID) == "" ||
		strings.TrimSpace(request.Goal) == "" ||
		request.SpeakerPolicy != SpeakerDirected ||
		request.MaxUtterances <= 0 ||
		request.MaxUtterances > MaxUtteranceCount ||
		len(request.Participants) == 0 ||
		len(request.Participants) > MaxParticipantCount ||
		len(request.DirectedSpeakerIDs) == 0 ||
		len(request.DirectedSpeakerIDs) > request.MaxUtterances {
		return ErrInvalidRequest
	}

	participants := make(map[string]struct{}, len(request.Participants))
	for _, participant := range request.Participants {
		id := strings.TrimSpace(participant.ID)
		if id == "" {
			return ErrInvalidRequest
		}
		if _, duplicate := participants[id]; duplicate {
			return ErrInvalidRequest
		}
		participants[id] = struct{}{}
	}

	directed := make(map[string]struct{}, len(request.DirectedSpeakerIDs))
	for _, raw := range request.DirectedSpeakerIDs {
		id := strings.TrimSpace(raw)
		if _, exists := participants[id]; !exists {
			return ErrInvalidRequest
		}
		if _, duplicate := directed[id]; duplicate {
			return ErrInvalidRequest
		}
		directed[id] = struct{}{}
	}
	return nil
}
