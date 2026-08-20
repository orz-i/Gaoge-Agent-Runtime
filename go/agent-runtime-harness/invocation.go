package harness

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"
	"time"
)

const (
	CapabilityAgent          = "runtime.agent"
	CapabilityTeam           = "runtime.team"
	CapabilityPlanExecute    = "runtime.plan_execute"
	CapabilityWorkflow       = "runtime.workflow"
	RuntimeCapabilityVersion = "v1"
)

// ExecutionClass identifies one already-selected execution implementation.
// It is never an automatic routing mode; selection happens before the
// Invocation is created.
type ExecutionClass string

const (
	ExecutionAgent       ExecutionClass = "agent"
	ExecutionTeam        ExecutionClass = "team"
	ExecutionPlanExecute ExecutionClass = "plan_execute"
	ExecutionWorkflow    ExecutionClass = "workflow"
	ExecutionApplication ExecutionClass = "application"
)

// InvocationStatus is the durable lifecycle of one capability invocation.
type InvocationStatus string

const (
	InvocationAccepted     InvocationStatus = "accepted"
	InvocationRunning      InvocationStatus = "running"
	InvocationWaitingInput InvocationStatus = "waiting_input"
	InvocationCompleted    InvocationStatus = "completed"
	InvocationFailed       InvocationStatus = "failed"
	InvocationCancelled    InvocationStatus = "cancelled"
)

// Invocation is the durable Harness envelope around one explicit Runtime
// Feature or application capability execution. Runtime Feature-private state
// remains in its owning Feature and is referenced only by ExecutionRefID.
type Invocation struct {
	ID                string           `json:"id"`
	TurnID            string           `json:"turnID"`
	ParentItemID      string           `json:"parentItemID,omitempty"`
	CapabilityKey     string           `json:"capabilityKey"`
	DefinitionVersion string           `json:"definitionVersion,omitempty"`
	ExecutionClass    ExecutionClass   `json:"executionClass"`
	Input             json.RawMessage  `json:"input,omitempty"`
	InputHash         string           `json:"inputHash,omitempty"`
	ExecutionRefID    string           `json:"executionRefID,omitempty"`
	Status            InvocationStatus `json:"status"`
	Attempt           int              `json:"attempt"`
	OutputRefs        []HostRef        `json:"outputRefs"`
	ErrorCode         string           `json:"errorCode,omitempty"`
	ErrorDetail       string           `json:"errorDetail,omitempty"`
	Revision          uint64           `json:"revision"`
	CreatedAt         time.Time        `json:"createdAt"`
	UpdatedAt         time.Time        `json:"updatedAt"`
}

// CapabilityExecutionRefID deterministically derives the Runtime execution
// identity for a non-Agent typed capability Invocation.
func CapabilityExecutionRefID(invocationID string) string {
	invocationID = strings.TrimSpace(invocationID)
	if invocationID == "" {
		return ""
	}
	return stableID("hcr", invocationID)
}

// AgentExecutionRefID deterministically derives the direct Agent Runtime Run
// identity owned by one already-created Agent Invocation. It is used only by
// host composition that must bind Tool state before the Agent begins.
func AgentExecutionRefID(invocationID string) string {
	invocationID = strings.TrimSpace(invocationID)
	if invocationID == "" {
		return ""
	}
	return stableID("hr", invocationID)
}

func invocationExecutionRefID(invocationID string, executionClass ExecutionClass, attempt int) string {
	invocationID = strings.TrimSpace(invocationID)
	if invocationID == "" || attempt <= 0 {
		return ""
	}
	prefix := "hcr"
	if executionClass == ExecutionAgent {
		prefix = "hr"
	}
	if attempt == 1 {
		return stableID(prefix, invocationID)
	}
	return stableID(prefix, invocationID, attemptString(attempt))
}

func retryableInvocationStatus(status InvocationStatus) bool {
	return status == InvocationFailed || status == InvocationCancelled
}

func attemptString(attempt int) string { return strconv.Itoa(attempt) }

func invocationRelationOwnerID(invocation Invocation) string {
	if invocation.Attempt <= 1 {
		return invocation.ID
	}
	return stableID("hivattempt", invocation.ID, attemptString(invocation.Attempt))
}

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

// TopLevelInvocation returns the top-level capability invocation from one
// durable Harness Snapshot.
func TopLevelInvocation(snapshot Snapshot) (Invocation, bool) {
	return topLevelInvocation(snapshot.Invocations)
}

// AgentInvocationID derives the first-party direct Agent Invocation identity
// without exposing the Harness-owned capability key to host code.
func AgentInvocationID(turnID, requestID string) (string, error) {
	return InvocationID(turnID, "", CapabilityAgent, requestID)
}

type agentInvocationInput struct {
	Goal             string   `json:"goal"`
	RequiredToolKeys []string `json:"requiredToolKeys,omitempty"`
}

func newDirectAgentInvocation(turnID, requestID, goal string, requiredToolKeys []string, now time.Time) (Invocation, error) {
	id, err := AgentInvocationID(turnID, requestID)
	if err != nil {
		return Invocation{}, err
	}
	input, inputHash, err := marshalInvocationValue(agentInvocationInput{
		Goal: strings.TrimSpace(goal), RequiredToolKeys: normalizeStrings(requiredToolKeys),
	})
	if err != nil {
		return Invocation{}, err
	}
	return Invocation{
		ID: id, TurnID: strings.TrimSpace(turnID), CapabilityKey: CapabilityAgent,
		DefinitionVersion: RuntimeCapabilityVersion, ExecutionClass: ExecutionAgent,
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

// InvocationID derives a stable invocation identity from its durable parent,
// capability descriptor and caller request identity.
func InvocationID(turnID, parentItemID, capabilityKey, requestID string) (string, error) {
	turnID = strings.TrimSpace(turnID)
	capabilityKey = strings.TrimSpace(capabilityKey)
	requestID = strings.TrimSpace(requestID)
	if turnID == "" || capabilityKey == "" || requestID == "" {
		return "", ErrInvalidRequest
	}
	return stableID("hiv", turnID, parentItemID, capabilityKey, requestID), nil
}

func validExecutionClass(value ExecutionClass) bool {
	switch value {
	case ExecutionAgent, ExecutionTeam, ExecutionPlanExecute, ExecutionWorkflow, ExecutionApplication:
		return true
	default:
		return false
	}
}

func validInvocationStatus(value InvocationStatus) bool {
	switch value {
	case InvocationAccepted, InvocationRunning, InvocationWaitingInput, InvocationCompleted, InvocationFailed, InvocationCancelled:
		return true
	default:
		return false
	}
}

func validInvocation(value Invocation) bool {
	if !validInvocationIdentity(value) || !validInvocationLifecycle(value) {
		return false
	}
	if value.ExecutionRefID == "" && value.Status != InvocationAccepted {
		return false
	}
	if len(value.Input) != 0 && (!json.Valid(value.Input) || hashInvocationBytes(value.Input) != value.InputHash) {
		return false
	}
	return validInvocationOutputRefs(value.OutputRefs)
}

func validInvocationIdentity(value Invocation) bool {
	if strings.TrimSpace(value.ID) == "" {
		return false
	}
	if strings.TrimSpace(value.TurnID) == "" {
		return false
	}
	return strings.TrimSpace(value.CapabilityKey) != ""
}

func validInvocationLifecycle(value Invocation) bool {
	if !validExecutionClass(value.ExecutionClass) || !validInvocationStatus(value.Status) {
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
	case TurnCompleted:
		return InvocationCompleted
	case TurnFailed:
		return InvocationFailed
	case TurnCancelled:
		return InvocationCancelled
	default:
		return InvocationAccepted
	}
}

func terminalInvocationStatus(status InvocationStatus) bool {
	return status == InvocationCompleted || status == InvocationFailed || status == InvocationCancelled
}

func invocationItemPayload(value Invocation) (json.RawMessage, error) {
	return json.Marshal(struct {
		CapabilityKey     string         `json:"capabilityKey"`
		DefinitionVersion string         `json:"definitionVersion,omitempty"`
		ExecutionClass    ExecutionClass `json:"executionClass"`
		ExecutionRefID    string         `json:"executionRefID,omitempty"`
		Attempt           int            `json:"attempt"`
		InputHash         string         `json:"inputHash,omitempty"`
	}{
		CapabilityKey: value.CapabilityKey, DefinitionVersion: value.DefinitionVersion,
		ExecutionClass: value.ExecutionClass, ExecutionRefID: value.ExecutionRefID,
		Attempt: value.Attempt, InputHash: value.InputHash,
	})
}
