package groupchat

import (
	"encoding/json"
	"errors"
	"strings"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

const (
	// RunKind identifies the explicit Group Chat Feature.
	RunKind kernel.RunKind = "group_chat"

	ResultKind          = "group_chat_result"
	MaxParticipantCount = 16
	MaxUtteranceCount   = 32
)

var (
	ErrInvalidRequest    = errors.New("invalid groupchat request")
	ErrSpeakerPending    = errors.New("groupchat speaker run is not terminal")
	ErrGroupChatFailed   = errors.New("groupchat execution failed")
	ErrGroupChatTerminal = errors.New("groupchat run is terminal")
)

// SpeakerPolicy controls how the next visible speaker is selected.
// P1 intentionally exposes only Directed. Selector and handoff policies require
// their own durable state transitions and are not compatibility aliases.
type SpeakerPolicy string

const (
	SpeakerDirected SpeakerPolicy = "directed"
)

// Participant identifies one host-authorized visible speaker. Runtime keeps
// only the neutral participant identity and description.
type Participant struct {
	ID          string `json:"id"`
	Description string `json:"description,omitempty"`
}

// SpeakerConfig is the immutable execution snapshot materialized by the hosting
// Harness from an authorized Role snapshot. It is execution data, not product
// ownership of the Assistant Role definition.
type SpeakerConfig struct {
	ParticipantID string          `json:"participantID"`
	RoleID        string          `json:"roleID"`
	RoleRevision  uint64          `json:"roleRevision"`
	RoleName      string          `json:"roleName"`
	Instructions  string          `json:"instructions,omitempty"`
	Model         string          `json:"model,omitempty"`
	ModelOptions  json.RawMessage `json:"modelOptions,omitempty"`
	ToolKeys      []string        `json:"toolKeys,omitempty"`
	Limits        agent.Limits    `json:"limits,omitempty"`
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
type SpeakerTurn struct {
	Ordinal      int               `json:"ordinal"`
	SpeakerID    string            `json:"speakerID"`
	RoleID       string            `json:"roleID"`
	RoleRevision uint64            `json:"roleRevision"`
	RoleName     string            `json:"roleName"`
	ChildRunID   string            `json:"childRunID,omitempty"`
	Status       SpeakerTurnStatus `json:"status"`
	Result       json.RawMessage   `json:"result,omitempty"`
	ErrorCode    string            `json:"errorCode,omitempty"`
}

// Utterance is the Runtime-safe terminal projection for one visible speaker.
// Conversation maps it into its own Message/author facts.
type Utterance struct {
	Ordinal      int               `json:"ordinal"`
	SpeakerID    string            `json:"speakerID"`
	RoleID       string            `json:"roleID"`
	RoleRevision uint64            `json:"roleRevision"`
	RoleName     string            `json:"roleName"`
	RunID        string            `json:"runID"`
	Status       SpeakerTurnStatus `json:"status"`
	Content      string            `json:"content,omitempty"`
	ErrorCode    string            `json:"errorCode,omitempty"`
}

// Result is the terminal public Group Chat output.
type Result struct {
	Kind       string      `json:"kind"`
	Utterances []Utterance `json:"utterances"`
}

// TerminationReason records why Group Chat stopped selecting speakers.
type TerminationReason string

const (
	TerminationCompleted     TerminationReason = "completed"
	TerminationMaxUtterances TerminationReason = "max_utterances"
	TerminationCancelled     TerminationReason = "cancelled"
	TerminationFailed        TerminationReason = "failed"
)

// View is the public durable Group Chat state.
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
	SpeakerConfigs     []SpeakerConfig
	DirectedSpeakerIDs []string
	MaxUtterances      int
}

// ValidateStartRequest validates the stable host-visible selection contract.
// Runner construction additionally requires one execution config per directed
// participant.
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
