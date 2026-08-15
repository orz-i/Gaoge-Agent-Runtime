package harness

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/kernel"
)

// TurnStatus is the durable Harness execution status.
type TurnStatus string

const (
	TurnAccepted     TurnStatus = "accepted"
	TurnRunning      TurnStatus = "running"
	TurnWaitingInput TurnStatus = "waiting_input"
	TurnCompleted    TurnStatus = "completed"
	TurnFailed       TurnStatus = "failed"
	TurnCancelled    TurnStatus = "cancelled"
)

// Turn binds one product Turn to one direct Agent root Run and immutable configuration snapshot.
type Turn struct {
	ID                string     `json:"id"`
	SessionID         string     `json:"sessionID"`
	HostTurn          HostRef    `json:"hostTurn"`
	RootRunID         string     `json:"rootRunID,omitempty"`
	ConfigSnapshotID  string     `json:"configSnapshotID"`
	ContextSnapshotID string     `json:"contextSnapshotID,omitempty"`
	Status            TurnStatus `json:"status"`
	Revision          uint64     `json:"revision"`
	ErrorCode         string     `json:"errorCode,omitempty"`
	ErrorDetail       string     `json:"errorDetail,omitempty"`
	CreatedAt         time.Time  `json:"createdAt"`
	UpdatedAt         time.Time  `json:"updatedAt"`
}

// Output is the provider-neutral terminal result projection returned by Harness.
type Output struct {
	ContentType string          `json:"contentType"`
	Content     json.RawMessage `json:"content"`
}

// Snapshot is a complete durable Harness Turn projection plus the current root output if available.
type Snapshot struct {
	Session Session        `json:"session"`
	Turn    Turn           `json:"turn"`
	Config  ConfigSnapshot `json:"config"`
	Items   []Item         `json:"items"`
	Output  *Output        `json:"output,omitempty"`
}

// TurnID deterministically derives one Harness Turn identity within a Session.
func TurnID(sessionID string, hostTurn HostRef) (string, error) {
	sessionID = strings.TrimSpace(sessionID)
	normalized, err := normalizeHostRef(hostTurn)
	if err != nil || sessionID == "" {
		return "", ErrInvalidRequest
	}
	return stableID("ht", sessionID, normalized.Kind, normalized.ID), nil
}

// RootRunID deterministically derives the direct Agent root Run identity for one Harness Turn.
func RootRunID(turnID string) string {
	turnID = strings.TrimSpace(turnID)
	if turnID == "" {
		return ""
	}
	return stableID("hr", turnID)
}

func turnStatusFromRuntime(status kernel.RunStatus) (TurnStatus, error) {
	switch status {
	case kernel.RunStatusRunning:
		return TurnRunning, nil
	case kernel.RunStatusWaitingInput:
		return TurnWaitingInput, nil
	case kernel.RunStatusCompleted:
		return TurnCompleted, nil
	case kernel.RunStatusFailed:
		return TurnFailed, nil
	case kernel.RunStatusCancelled:
		return TurnCancelled, nil
	default:
		return "", ErrInvalidRequest
	}
}

func terminalTurnStatus(status TurnStatus) bool {
	return status == TurnCompleted || status == TurnFailed || status == TurnCancelled
}

func cloneOutput(value *Output) *Output {
	if value == nil {
		return nil
	}
	result := *value
	result.Content = append(json.RawMessage(nil), value.Content...)
	return &result
}
