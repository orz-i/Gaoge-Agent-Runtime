package groupchat

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"
)

const selectorInvocationLeaseDuration = 2 * time.Minute

// SelectorCandidate is one Runtime-authorized speaker option visible to the Selector.
type SelectorCandidate struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// SelectorHistoryItem is the minimal visible transcript projected into the Selector request.
type SelectorHistoryItem struct {
	SpeakerID   string `json:"speakerID"`
	SpeakerName string `json:"speakerName"`
	Content     string `json:"content"`
}

// SelectorRequest is one bounded next-speaker decision.
type SelectorRequest struct {
	InvocationID string                `json:"invocationID"`
	RunID        string                `json:"runID"`
	Goal         string                `json:"goal"`
	Model        string                `json:"model,omitempty"`
	ModelOptions json.RawMessage       `json:"modelOptions,omitempty"`
	Candidates   []SelectorCandidate   `json:"candidates"`
	History      []SelectorHistoryItem `json:"history,omitempty"`
	CanComplete  bool                  `json:"canComplete"`
}

// SelectorResponse is one validated next-speaker decision. Complete and SpeakerID
// are mutually exclusive.
type SelectorResponse struct {
	SpeakerID  string `json:"speakerID,omitempty"`
	Complete   bool   `json:"complete,omitempty"`
	ResponseID string `json:"responseID,omitempty"`
}

// Selector chooses the next visible speaker from the supplied candidates.
type Selector interface {
	Select(context.Context, SelectorRequest) (SelectorResponse, error)
}

type SelectorInvocationStatus string

const (
	SelectorInvocationPending   SelectorInvocationStatus = "pending"
	SelectorInvocationCompleted SelectorInvocationStatus = "completed"
	SelectorInvocationConsumed  SelectorInvocationStatus = "consumed"
)

// SelectorInvocation is the durable logical selector call. Physical execution
// may repeat after a crash, but InvocationID remains stable and only one decision
// may be consumed into Group Chat state.
type SelectorInvocation struct {
	ID                  string                   `json:"id"`
	RunID               string                   `json:"runID"`
	Round               int                      `json:"round"`
	RequestHash         string                   `json:"requestHash"`
	Status              SelectorInvocationStatus `json:"status"`
	Request             SelectorRequest          `json:"request"`
	Response            SelectorResponse         `json:"response,omitempty"`
	ExecutionAttempt    uint32                   `json:"executionAttempt,omitempty"`
	ExecutionLeaseUntil *time.Time               `json:"executionLeaseUntil,omitempty"`
	CreatedAt           time.Time                `json:"createdAt"`
	CompletedAt         *time.Time               `json:"completedAt,omitempty"`
	ConsumedAt          *time.Time               `json:"consumedAt,omitempty"`
}

func newSelectorInvocation(request SelectorRequest, round int, now time.Time) (SelectorInvocation, error) {
	request = cloneSelectorRequest(request)
	request.InvocationID = ""
	hash, err := selectorRequestHash(request)
	if err != nil {
		return SelectorInvocation{}, err
	}
	material := request.RunID + "\x00" + strconv.Itoa(round) + "\x00" + hash
	digest := sha256.Sum256([]byte(material))
	id := "selectorinv_" + hex.EncodeToString(digest[:16])
	request.InvocationID = id
	return SelectorInvocation{
		ID: id, RunID: request.RunID, Round: round, RequestHash: hash,
		Status: SelectorInvocationPending, Request: request, CreatedAt: now.UTC(),
	}, nil
}

func selectorRequestHash(request SelectorRequest) (string, error) {
	request = cloneSelectorRequest(request)
	request.InvocationID = ""
	encoded, err := json.Marshal(request)
	if err != nil {
		return "", errors.Join(ErrInvalidRequest, err)
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func cloneSelectorRequest(request SelectorRequest) SelectorRequest {
	request.ModelOptions = append(json.RawMessage(nil), request.ModelOptions...)
	request.Candidates = append([]SelectorCandidate(nil), request.Candidates...)
	request.History = append([]SelectorHistoryItem(nil), request.History...)
	return request
}

func cloneSelectorInvocation(value *SelectorInvocation) *SelectorInvocation {
	if value == nil {
		return nil
	}
	cloned := *value
	cloned.Request = cloneSelectorRequest(value.Request)
	if value.ExecutionLeaseUntil != nil {
		at := value.ExecutionLeaseUntil.UTC()
		cloned.ExecutionLeaseUntil = &at
	}
	if value.CompletedAt != nil {
		at := value.CompletedAt.UTC()
		cloned.CompletedAt = &at
	}
	if value.ConsumedAt != nil {
		at := value.ConsumedAt.UTC()
		cloned.ConsumedAt = &at
	}
	return &cloned
}

func validSelectorInvocation(value *SelectorInvocation) bool {
	if value == nil || strings.TrimSpace(value.ID) == "" || strings.TrimSpace(value.RunID) == "" ||
		value.Round < 0 || len(value.RequestHash) != sha256.Size*2 || value.CreatedAt.IsZero() ||
		strings.TrimSpace(value.Request.InvocationID) != value.ID || value.Request.RunID != value.RunID {
		return false
	}
	hash, err := selectorRequestHash(value.Request)
	if err != nil || hash != value.RequestHash {
		return false
	}
	switch value.Status {
	case SelectorInvocationPending:
		return value.CompletedAt == nil && value.ConsumedAt == nil &&
			(value.ExecutionLeaseUntil == nil || value.ExecutionAttempt > 0)
	case SelectorInvocationCompleted:
		return value.CompletedAt != nil && value.ConsumedAt == nil && value.ExecutionLeaseUntil == nil &&
			validSelectorResponse(value.Response, value.Request)
	case SelectorInvocationConsumed:
		return value.CompletedAt != nil && value.ConsumedAt != nil && value.ExecutionLeaseUntil == nil &&
			validSelectorResponse(value.Response, value.Request)
	default:
		return false
	}
}

func validSelectorResponse(response SelectorResponse, request SelectorRequest) bool {
	speakerID := strings.TrimSpace(response.SpeakerID)
	if response.Complete {
		return request.CanComplete && speakerID == ""
	}
	if speakerID == "" {
		return false
	}
	for _, candidate := range request.Candidates {
		if strings.TrimSpace(candidate.ID) == speakerID {
			return true
		}
	}
	return false
}
