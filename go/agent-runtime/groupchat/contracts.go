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
	ErrInvalidRequest         = errors.New("invalid groupchat request")
	ErrSpeakerPending         = errors.New("groupchat speaker run is not terminal")
	ErrSelectorFailure        = errors.New("groupchat selector failed")
	ErrSelectorPending        = errors.New("groupchat selector is waiting for retry")
	ErrSelectorInvocationBusy = errors.New("groupchat selector invocation is leased")
	ErrGroupChatFailed        = errors.New("groupchat execution failed")
	ErrGroupChatTerminal      = errors.New("groupchat run is terminal")
)

// SpeakerPolicy controls how the next visible speaker is selected. Directed
// consumes an ordered host decision. Selector asks the bounded Selector port to
// choose one candidate at a time from Runtime-owned durable state.
type SpeakerPolicy string

const (
	SpeakerDirected SpeakerPolicy = "directed"
	SpeakerSelector SpeakerPolicy = "selector"
	SpeakerHandoff  SpeakerPolicy = "handoff"
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
	ParticipantID  string          `json:"participantID"`
	AuthorKind     string          `json:"authorKind"`
	AuthorID       string          `json:"authorID"`
	AuthorRevision string          `json:"authorRevision"`
	AuthorName     string          `json:"authorName"`
	MemberID       string          `json:"memberID"`
	MemberRevision string          `json:"memberRevision,omitempty"`
	Instructions   string          `json:"instructions,omitempty"`
	Model          string          `json:"model,omitempty"`
	ModelOptions   json.RawMessage `json:"modelOptions,omitempty"`
	ToolKeys       []string        `json:"toolKeys,omitempty"`
	Limits         agent.Limits    `json:"limits,omitempty"`
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
	Ordinal        int               `json:"ordinal"`
	SpeakerID      string            `json:"speakerID"`
	AuthorKind     string            `json:"authorKind"`
	AuthorID       string            `json:"authorID"`
	AuthorRevision string            `json:"authorRevision"`
	AuthorName     string            `json:"authorName"`
	ChildRunID     string            `json:"childRunID,omitempty"`
	Status         SpeakerTurnStatus `json:"status"`
	Result         json.RawMessage   `json:"result,omitempty"`
	ErrorCode      string            `json:"errorCode,omitempty"`
}

// Utterance is the Runtime-safe terminal projection for one visible speaker.
// Conversation maps it into its own Message/author facts.
type Utterance struct {
	Ordinal        int               `json:"ordinal"`
	SpeakerID      string            `json:"speakerID"`
	AuthorKind     string            `json:"authorKind"`
	AuthorID       string            `json:"authorID"`
	AuthorRevision string            `json:"authorRevision"`
	AuthorName     string            `json:"authorName"`
	RunID          string            `json:"runID"`
	Status         SpeakerTurnStatus `json:"status"`
	Content        string            `json:"content,omitempty"`
	ErrorCode      string            `json:"errorCode,omitempty"`
}

// Result is the terminal public Group Chat output.
type Result struct {
	Kind       string      `json:"kind"`
	Utterances []Utterance `json:"utterances"`
}

// TerminationReason records why Group Chat stopped selecting speakers.
type TerminationReason string

const (
	TerminationCompleted         TerminationReason = "completed"
	TerminationSelectorCompleted TerminationReason = "selector_completed"
	TerminationHandoffCompleted  TerminationReason = "handoff_completed"
	TerminationMaxUtterances     TerminationReason = "max_utterances"
	TerminationNoEligibleSpeaker TerminationReason = "no_eligible_speaker"
	TerminationCancelled         TerminationReason = "cancelled"
	TerminationFailed            TerminationReason = "failed"
)

// View is the public durable Group Chat state.
type View struct {
	SpeakerPolicy       SpeakerPolicy       `json:"speakerPolicy"`
	Participants        []Participant       `json:"participants"`
	DirectedSpeakerIDs  []string            `json:"directedSpeakerIDs,omitempty"`
	CandidateSpeakerIDs []string            `json:"candidateSpeakerIDs,omitempty"`
	SelectorInvocation  *SelectorInvocation `json:"selectorInvocation,omitempty"`
	SpeakerTurns        []SpeakerTurn       `json:"speakerTurns"`
	NextSpeakerIndex    int                 `json:"nextSpeakerIndex"`
	TerminationReason   TerminationReason   `json:"terminationReason,omitempty"`
}

// StartRequest creates one explicit Group Chat Run.
type StartRequest struct {
	ID                   string
	Actor                kernel.ActorRef
	Thread               kernel.ThreadRef
	RequestID            string
	Goal                 string
	SpeakerPolicy        SpeakerPolicy
	Participants         []Participant
	SpeakerConfigs       []SpeakerConfig
	DirectedSpeakerIDs   []string
	CandidateSpeakerIDs  []string
	SelectorModel        string
	SelectorModelOptions json.RawMessage
	MaxUtterances        int
}

// ValidateStartRequest validates the stable host-visible selection contract.
func ValidateStartRequest(request StartRequest) error {
	if !validStartRequestIdentity(request) {
		return ErrInvalidRequest
	}
	participants, ok := participantIDSet(request.Participants)
	if !ok {
		return ErrInvalidRequest
	}
	switch request.SpeakerPolicy {
	case SpeakerDirected:
		if strings.TrimSpace(request.SelectorModel) != "" || len(request.CandidateSpeakerIDs) != 0 ||
			!validSpeakerIDList(request.DirectedSpeakerIDs, participants, request.MaxUtterances) {
			return ErrInvalidRequest
		}
	case SpeakerSelector:
		if len(request.DirectedSpeakerIDs) != 0 || strings.TrimSpace(request.SelectorModel) == "" ||
			!validSpeakerIDList(request.CandidateSpeakerIDs, participants, MaxParticipantCount) {
			return ErrInvalidRequest
		}
	case SpeakerHandoff:
		if len(request.DirectedSpeakerIDs) != 0 || strings.TrimSpace(request.SelectorModel) != "" ||
			len(request.CandidateSpeakerIDs) < 2 ||
			!validSpeakerIDList(request.CandidateSpeakerIDs, participants, MaxParticipantCount) {
			return ErrInvalidRequest
		}
	default:
		return ErrInvalidRequest
	}
	return nil
}

func validStartRequestIdentity(request StartRequest) bool {
	return strings.TrimSpace(request.ID) != "" &&
		strings.TrimSpace(request.Actor.TenantID) != "" &&
		strings.TrimSpace(request.Actor.ActorID) != "" &&
		strings.TrimSpace(request.Thread.Kind) != "" &&
		strings.TrimSpace(request.Thread.ID) != "" &&
		strings.TrimSpace(request.Goal) != "" &&
		request.MaxUtterances > 0 &&
		request.MaxUtterances <= MaxUtteranceCount &&
		len(request.Participants) > 0 &&
		len(request.Participants) <= MaxParticipantCount
}

func participantIDSet(participants []Participant) (map[string]struct{}, bool) {
	result := make(map[string]struct{}, len(participants))
	for _, participant := range participants {
		id := strings.TrimSpace(participant.ID)
		if id == "" {
			return nil, false
		}
		if _, duplicate := result[id]; duplicate {
			return nil, false
		}
		result[id] = struct{}{}
	}
	return result, true
}

func validSpeakerIDList(values []string, participants map[string]struct{}, max int) bool {
	if len(values) == 0 || len(values) > max {
		return false
	}
	seen := make(map[string]struct{}, len(values))
	for _, raw := range values {
		id := strings.TrimSpace(raw)
		if _, exists := participants[id]; !exists {
			return false
		}
		if _, duplicate := seen[id]; duplicate {
			return false
		}
		seen[id] = struct{}{}
	}
	return true
}
