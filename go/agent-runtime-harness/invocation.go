package harness

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

// The Agent ID's immutable namespace is a domain separator, not a
// selectable capability. Keep its bytes to preserve already-frozen Run /
// Invocation identity across the pre-stable schema cut.
const agentInvocationDomain = "runtime.agent"

// InvocationStatus is the durable lifecycle of one Agent invocation.
type InvocationStatus string

const (
	InvocationAccepted     InvocationStatus = "accepted"
	InvocationRunning      InvocationStatus = "running"
	InvocationWaitingInput InvocationStatus = "waiting_input"
	InvocationPausedBudget InvocationStatus = "paused_budget"
	InvocationCompleted    InvocationStatus = "completed"
	InvocationFailed       InvocationStatus = "failed"
	InvocationCancelled    InvocationStatus = "cancelled"
)

// Invocation is the durable Harness envelope around one Agent execution.
// Original Run identity and lifecycle facts remain bound to ExecutionRefID.
type Invocation struct {
	ID           string `json:"id"`
	TurnID       string `json:"turnID"`
	ParentItemID string `json:"parentItemID,omitempty"`

	Input          json.RawMessage  `json:"input,omitempty"`
	InputHash      string           `json:"inputHash,omitempty"`
	ExecutionRefID string           `json:"executionRefID,omitempty"`
	Status         InvocationStatus `json:"status"`
	Attempt        int              `json:"attempt"`
	OutputRefs     []HostRef        `json:"outputRefs"`
	ErrorCode      string           `json:"errorCode,omitempty"`
	ErrorDetail    string           `json:"errorDetail,omitempty"`
	Revision       uint64           `json:"revision"`
	CreatedAt      time.Time        `json:"createdAt"`
	UpdatedAt      time.Time        `json:"updatedAt"`
}

// AgentExecutionRefID deterministically derives the Agent Runtime Run
// identity owned by one already-created Agent Invocation. It is used only by
// host composition that must bind Tool state before the Agent begins.
func AgentExecutionRefID(invocationID string) string {
	invocationID = strings.TrimSpace(invocationID)
	if invocationID == "" {
		return ""
	}
	return stableID("hr", invocationID)
}

func invocationExecutionRefID(invocationID string, attempt int) string {
	invocationID = strings.TrimSpace(invocationID)
	if invocationID == "" || attempt <= 0 {
		return ""
	}
	if attempt == 1 {
		return AgentExecutionRefID(invocationID)
	}
	return stableID("hr", invocationID, attemptString(attempt))
}

func retryableInvocationStatus(status InvocationStatus) bool {
	return status == InvocationFailed || status == InvocationCancelled
}

func attemptString(attempt int) string { return strconv.Itoa(attempt) }

func loadTopLevelInvocation(ctx context.Context, store Store, turnID string) (Invocation, error) {
	values, err := store.ListInvocations(ctx, strings.TrimSpace(turnID))
	if err != nil {
		return Invocation{}, err
	}
	value, ok := topLevelInvocation(values)
	if !ok {
		return Invocation{}, ErrNotFound
	}
	return value, nil
}

// TopLevelInvocation returns the top-level Agent invocation from one
// durable Harness Snapshot.
func TopLevelInvocation(snapshot Snapshot) (Invocation, bool) {
	return topLevelInvocation(snapshot.Invocations)
}

// AgentInvocationID derives the only allowed physical Agent Invocation
// identity. The fixed domain separator cannot be selected by Host callers.
func AgentInvocationID(turnID, parentItemID, requestID string) (string, error) {
	turnID = strings.TrimSpace(turnID)
	parentItemID = strings.TrimSpace(parentItemID)
	requestID = strings.TrimSpace(requestID)
	if turnID == "" || requestID == "" {
		return "", ErrInvalidRequest
	}
	return stableID("hiv", turnID, parentItemID, agentInvocationDomain, requestID), nil
}

type agentInvocationInput struct {
	Goal             string           `json:"goal"`
	Actor            kernel.ActorRef  `json:"actor"`
	Thread           kernel.ThreadRef `json:"thread"`
	RequiredToolKeys []string         `json:"requiredToolKeys,omitempty"`
	InitialToolKeys  []string         `json:"initialToolKeys,omitempty"`
}

func newAgentInvocation(
	turnID, requestID, goal string,
	actor kernel.ActorRef,
	thread kernel.ThreadRef,
	requiredToolKeys, initialToolKeys []string,
	now time.Time,
) (Invocation, error) {
	id, err := AgentInvocationID(turnID, "", requestID)
	if err != nil {
		return Invocation{}, err
	}
	input, inputHash, err := marshalInvocationValue(agentInvocationInput{
		Goal: strings.TrimSpace(goal), Actor: actor, Thread: thread,
		RequiredToolKeys: normalizeStrings(requiredToolKeys),
		InitialToolKeys:  normalizeStrings(initialToolKeys),
	})
	if err != nil {
		return Invocation{}, err
	}
	return Invocation{
		ID: id, TurnID: strings.TrimSpace(turnID),
		Input: input, InputHash: inputHash, ExecutionRefID: AgentExecutionRefID(id),
		Status: InvocationAccepted, Attempt: 1, OutputRefs: []HostRef{}, Revision: 1,
		CreatedAt: now, UpdatedAt: now,
	}, nil
}

func marshalInvocationValue(value any) (json.RawMessage, string, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, "", err
	}
	return raw, hashInvocationBytes(raw), nil
}

func hashInvocationBytes(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}

func hashInvocationInput(value string) string {
	return hashInvocationBytes([]byte(strings.TrimSpace(value)))
}

func topLevelInvocation(values []Invocation) (Invocation, bool) {
	for _, value := range values {
		if strings.TrimSpace(value.ParentItemID) == "" {
			return value, true
		}
	}
	return Invocation{}, false
}

func validInvocationStatus(value InvocationStatus) bool {
	switch value {
	case InvocationAccepted, InvocationRunning, InvocationWaitingInput, InvocationPausedBudget, InvocationCompleted, InvocationFailed, InvocationCancelled:
		return true
	default:
		return false
	}
}

func validInvocation(value Invocation) bool {
	return validInvocationExecutionAuthority(value) && validInvocationOutputRefs(value.OutputRefs)
}

// ValidAgentInvocationForStore validates the complete first-party Agent record
// before either relational or in-memory storage can admit it. The caller may
// not select a different executor by supplying a legacy capability marker.
func ValidAgentInvocationForStore(value Invocation) bool { return validInvocation(value) }

// validInvocationExecutionAuthority validates the frozen execution identity,
// lifecycle and InputHash without treating Host-visible OutputRefs as a
// prerequisite to a protective physical stop. Root/child Cancel must still
// prove exact owner, Run and ancestry before a Kernel cancellation; all
// ordinary reads and new execution retain the full validInvocation check.
func validInvocationExecutionAuthority(value Invocation) bool {
	if !validInvocationIdentity(value) || !validInvocationLifecycle(value) {
		return false
	}
	if value.ExecutionRefID == "" && value.Status != InvocationAccepted {
		return false
	}
	// A frozen InputHash without its payload is not an optional input: it
	// is an incomplete ownership record. In particular, dropping a Handoff
	// child's payload must never turn its isolated Run into an ordinary child.
	if len(value.Input) == 0 {
		if value.InputHash != "" || value.ParentItemID != "" {
			return false
		}
	} else if !json.Valid(value.Input) || hashInvocationBytes(value.Input) != value.InputHash {
		return false
	}
	return true
}

func validInvocationIdentity(value Invocation) bool {
	if strings.TrimSpace(value.ID) == "" {
		return false
	}
	if strings.TrimSpace(value.TurnID) == "" {
		return false
	}
	// The only persisted Invocation type is Agent. Historical non-Agent rows
	// must be rejected at the pre-stable SQL schema boundary, never promoted
	// by dropping an untrusted mutable category field.
	return strings.TrimSpace(value.ID) == value.ID && strings.TrimSpace(value.TurnID) == value.TurnID
}

func validInvocationLifecycle(value Invocation) bool {
	if !validInvocationStatus(value.Status) {
		return false
	}
	if value.Attempt <= 0 || value.Revision == 0 {
		return false
	}
	return !value.CreatedAt.IsZero() && !value.UpdatedAt.IsZero()
}

func validInvocationOutputRefs(values []HostRef) bool {
	for _, ref := range values {
		if _, err := normalizeHostRef(ref); err != nil {
			return false
		}
	}
	return true
}

func cloneInvocation(value Invocation) Invocation {
	value.Input = append(json.RawMessage(nil), value.Input...)
	value.OutputRefs = append([]HostRef(nil), value.OutputRefs...)
	return value
}

func cloneInvocations(values []Invocation) []Invocation {
	result := make([]Invocation, len(values))
	for index, value := range values {
		result[index] = cloneInvocation(value)
	}
	return result
}

func invocationStatusFromTurn(status TurnStatus) InvocationStatus {
	switch status {
	case TurnRunning:
		return InvocationRunning
	case TurnWaitingInput:
		return InvocationWaitingInput
	case TurnPausedBudget:
		return InvocationPausedBudget
	case TurnCompleted:
		return InvocationCompleted
	case TurnFailed:
		return InvocationFailed
	case TurnCancelled:
		return InvocationCancelled
	case TurnAccepted:
		return InvocationAccepted
	default:
		return InvocationAccepted
	}
}

func terminalInvocationStatus(status InvocationStatus) bool {
	return status == InvocationCompleted || status == InvocationFailed || status == InvocationCancelled
}

func invocationItemPayload(value Invocation) (json.RawMessage, error) {
	return json.Marshal(struct {
		ExecutionRefID string    `json:"executionRefID,omitempty"`
		Attempt        int       `json:"attempt"`
		InputHash      string    `json:"inputHash,omitempty"`
		OutputRefs     []HostRef `json:"outputRefs"`
	}{value.ExecutionRefID, value.Attempt, value.InputHash, append([]HostRef(nil), value.OutputRefs...)})
}
