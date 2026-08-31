package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

// ModelInvocationStatus is the durable lifecycle of one logical Agent model
// call. Provider delivery may repeat, but only one completed receipt may be
// consumed into Agent state.
type ModelInvocationStatus string

const (
	ModelInvocationPending   ModelInvocationStatus = "pending"
	ModelInvocationCompleted ModelInvocationStatus = "completed"
	ModelInvocationConsumed  ModelInvocationStatus = "consumed"

	modelInvocationLeaseDuration = 2 * time.Minute
)

// ModelInvocation is the Agent-owned durable intent/receipt for one logical
// model step. Kernel persists it only as opaque feature state.
type ModelInvocation struct {
	ID                  string                `json:"id"`
	RunID               string                `json:"runID"`
	SourceRevision      uint64                `json:"sourceRevision"`
	RequestHash         string                `json:"requestHash"`
	Provider            string                `json:"provider,omitempty"`
	Model               string                `json:"model,omitempty"`
	Status              ModelInvocationStatus `json:"status"`
	Request             model.Request         `json:"request,omitempty"`
	ProviderResponseID  string                `json:"providerResponseID,omitempty"`
	Response            model.Response        `json:"response,omitempty"`
	Usage               *model.Usage          `json:"usage,omitempty"`
	ExecutionAttempt    uint32                `json:"executionAttempt,omitempty"`
	ExecutionLeaseUntil *time.Time            `json:"executionLeaseUntil,omitempty"`
	CreatedAt           time.Time             `json:"createdAt"`
	CompletedAt         *time.Time            `json:"completedAt,omitempty"`
	ConsumedAt          *time.Time            `json:"consumedAt,omitempty"`
}

func newModelInvocation(
	run kernel.Run,
	request model.Request,
	provider string,
	now time.Time,
) (ModelInvocation, error) {
	request.InvocationID = ""
	hash, err := modelRequestHash(request)
	if err != nil {
		return ModelInvocation{}, err
	}
	idSource := run.ID + "\x00" + strconv.FormatUint(run.Revision, 10) + "\x00" + hash
	idDigest := sha256.Sum256([]byte(idSource))
	id := "modelinv_" + hex.EncodeToString(idDigest[:16])
	request.InvocationID = id
	return ModelInvocation{
		ID: id, RunID: run.ID, SourceRevision: run.Revision, RequestHash: hash,
		Provider: strings.TrimSpace(provider), Model: strings.TrimSpace(request.Model),
		Status: ModelInvocationPending, Request: model.CloneRequest(request), CreatedAt: now.UTC(),
	}, nil
}

func modelRequestHash(request model.Request) (string, error) {
	request.InvocationID = ""
	encoded, err := json.Marshal(model.CloneRequest(request))
	if err != nil {
		return "", errors.Join(ErrInvalidRequest, err)
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func activeModelInvocation(state runState) (*ModelInvocation, bool) {
	if len(state.ModelInvocations) == 0 {
		return nil, false
	}
	last := &state.ModelInvocations[len(state.ModelInvocations)-1]
	if last.Status == ModelInvocationPending || last.Status == ModelInvocationCompleted {
		return last, true
	}
	return nil, false
}

func replaceLastModelInvocation(state *runState, invocation ModelInvocation) bool {
	if state == nil || len(state.ModelInvocations) == 0 ||
		state.ModelInvocations[len(state.ModelInvocations)-1].ID != invocation.ID {
		return false
	}
	state.ModelInvocations[len(state.ModelInvocations)-1] = cloneModelInvocation(invocation)
	return true
}

func consumeLastModelInvocation(state *runState, now time.Time) (ModelInvocation, bool) {
	if state == nil || len(state.ModelInvocations) == 0 {
		return ModelInvocation{}, false
	}
	index := len(state.ModelInvocations) - 1
	invocation := state.ModelInvocations[index]
	if invocation.Status != ModelInvocationCompleted {
		return ModelInvocation{}, false
	}
	consumedAt := now.UTC()
	invocation.Status = ModelInvocationConsumed
	invocation.ConsumedAt = &consumedAt
	consumed := cloneModelInvocation(invocation)
	// Once consumed, the normalized request is no longer needed for replay. The
	// hash and receipt remain durable while duplicated transcript bytes are not.
	invocation.Request = model.Request{}
	state.ModelInvocations[index] = cloneModelInvocation(invocation)
	return consumed, true
}

func cloneModelInvocations(values []ModelInvocation) []ModelInvocation {
	result := make([]ModelInvocation, len(values))
	for index, invocation := range values {
		result[index] = cloneModelInvocation(invocation)
	}
	return result
}

func cloneModelInvocation(value ModelInvocation) ModelInvocation {
	value.Request = model.CloneRequest(value.Request)
	value.Response = model.CloneResponse(value.Response)
	value.Usage = model.CloneUsage(value.Usage)
	if value.ExecutionLeaseUntil != nil {
		leaseUntil := value.ExecutionLeaseUntil.UTC()
		value.ExecutionLeaseUntil = &leaseUntil
	}
	if value.CompletedAt != nil {
		completed := value.CompletedAt.UTC()
		value.CompletedAt = &completed
	}
	if value.ConsumedAt != nil {
		consumed := value.ConsumedAt.UTC()
		value.ConsumedAt = &consumed
	}
	return value
}

func validModelInvocations(values []ModelInvocation) bool {
	active := 0
	for index, invocation := range values {
		if strings.TrimSpace(invocation.ID) == "" || strings.TrimSpace(invocation.RunID) == "" ||
			invocation.SourceRevision == 0 || len(invocation.RequestHash) != sha256.Size*2 ||
			invocation.CreatedAt.IsZero() {
			return false
		}
		switch invocation.Status {
		case ModelInvocationPending:
			active++
			if index != len(values)-1 || strings.TrimSpace(invocation.Request.InvocationID) != invocation.ID {
				return false
			}
			hash, err := modelRequestHash(invocation.Request)
			if err != nil || hash != invocation.RequestHash {
				return false
			}
			if invocation.ExecutionLeaseUntil != nil && invocation.ExecutionAttempt == 0 {
				return false
			}
		case ModelInvocationCompleted:
			active++
			if index != len(values)-1 || invocation.CompletedAt == nil ||
				invocation.ExecutionLeaseUntil != nil || strings.TrimSpace(invocation.Request.InvocationID) != invocation.ID ||
				!validModelResponse(invocation.Response) {
				return false
			}
			hash, err := modelRequestHash(invocation.Request)
			if err != nil || hash != invocation.RequestHash {
				return false
			}
		case ModelInvocationConsumed:
			if invocation.CompletedAt == nil || invocation.ConsumedAt == nil || invocation.ExecutionLeaseUntil != nil ||
				!validModelResponse(invocation.Response) {
				return false
			}
		default:
			return false
		}
	}
	return active <= 1
}

func providerName(client model.Client) string {
	namer, ok := client.(model.ProviderNamer)
	if !ok {
		return ""
	}
	return strings.TrimSpace(namer.ProviderName())
}

func (runner *Runner) ensureModelInvocation(
	ctx context.Context,
	snapshot kernel.Snapshot,
	state runState,
) (kernel.Snapshot, runState, ModelInvocation, error) {
	if invocation, ok := activeModelInvocation(state); ok {
		return snapshot, state, cloneModelInvocation(*invocation), nil
	}
	request, err := runner.buildModelRequest(ctx, snapshot, state)
	if err != nil {
		return kernel.Snapshot{}, runState{}, ModelInvocation{}, err
	}
	invocation, err := newModelInvocation(snapshot.Run, request, providerName(runner.model), runner.clock.Now())
	if err != nil {
		return kernel.Snapshot{}, runState{}, ModelInvocation{}, err
	}
	state.ModelInvocations = append(state.ModelInvocations, cloneModelInvocation(invocation))
	encoded, err := encodeState(state)
	if err != nil {
		return kernel.Snapshot{}, runState{}, ModelInvocation{}, err
	}
	next, err := runner.runtime.Apply(ctx, snapshot.Run.ID, snapshot.Run.Revision, kernel.Mutation{
		Status: kernel.RunStatusRunning, State: encoded, Checkpoint: snapshot.Checkpoint,
		Events: []kernel.EventDraft{{
			Type: "agent.model_invocation.pending", Message: invocation.ID, Wakeup: true,
		}},
	})
	if err != nil {
		return kernel.Snapshot{}, runState{}, ModelInvocation{}, err
	}
	return next, state, invocation, nil
}

func (runner *Runner) buildModelRequest(
	ctx context.Context,
	snapshot kernel.Snapshot,
	state runState,
) (model.Request, error) {
	definitions, hostedTools, err := runner.resolveSelectedTools(ctx, state.ToolKeys, state.Model)
	if err != nil {
		return model.Request{}, err
	}
	messages := model.CloneMessages(state.Messages)
	if len(state.BlockedToolKeys) != 0 {
		definitions = definitionsWithoutKeys(definitions, state.BlockedToolKeys)
		hostedTools = hostedToolsWithoutKeys(hostedTools, state.BlockedToolKeys)
		messages = withBlockedToolGuidance(messages, state.BlockedToolKeys)
	}
	if repeatedToolKeys := repeatedUnchangedToolKeys(messages); len(repeatedToolKeys) != 0 {
		definitions = definitionsWithoutKeys(definitions, repeatedToolKeys)
		hostedTools = hostedToolsWithoutKeys(hostedTools, repeatedToolKeys)
		messages = withRepeatedToolGuidance(messages, repeatedToolKeys)
	}
	return model.Request{
		RunID: snapshot.Run.ID, Model: state.Model, ModelOptions: cloneRawJSON(state.ModelOptions),
		Messages: messages, Tools: definitions, HostedTools: model.CloneHostedTools(hostedTools),
		RequireToolCall: state.RequireToolCall && (len(definitions) != 0 || len(hostedTools) != 0),
	}, nil
}

func (runner *Runner) executeModelInvocation(
	ctx context.Context,
	snapshot kernel.Snapshot,
	state runState,
	invocation ModelInvocation,
) (kernel.Snapshot, runState, error) {
	if invocation.Status == ModelInvocationCompleted {
		return snapshot, state, nil
	}
	if invocation.Status != ModelInvocationPending {
		return kernel.Snapshot{}, runState{}, ErrInvalidRequest
	}
	if invocation.ExecutionAttempt == 0 || invocation.ExecutionLeaseUntil == nil ||
		!runner.clock.Now().UTC().Before(invocation.ExecutionLeaseUntil.UTC()) {
		return snapshot, state, ErrModelInvocationBusy
	}
	runner.publish(ctx, snapshot.Run.ID, plugin.Event{
		Type: EventModelStarted, Revision: snapshot.Run.Revision, Status: string(snapshot.Run.Status),
	})
	response, err := runner.generateModel(ctx, model.CloneRequest(invocation.Request))
	if err != nil {
		return snapshot, state, errors.Join(ErrModelFailure, err)
	}
	response.Content = strings.TrimSpace(response.Content)
	if !validModelResponse(response) {
		return snapshot, state, ErrInvalidModelResponse
	}
	completedAt := runner.clock.Now().UTC()
	invocation.Status = ModelInvocationCompleted
	invocation.ProviderResponseID = strings.TrimSpace(response.ResponseID)
	invocation.Response = model.CloneResponse(response)
	invocation.Usage = model.CloneUsage(response.Usage)
	invocation.ExecutionLeaseUntil = nil
	invocation.CompletedAt = &completedAt
	if !replaceLastModelInvocation(&state, invocation) {
		return kernel.Snapshot{}, runState{}, ErrInvalidRequest
	}
	encoded, err := encodeState(state)
	if err != nil {
		return kernel.Snapshot{}, runState{}, err
	}
	next, err := runner.runtime.Apply(ctx, snapshot.Run.ID, snapshot.Run.Revision, kernel.Mutation{
		Status: kernel.RunStatusRunning, State: encoded, Checkpoint: snapshot.Checkpoint,
		Events: []kernel.EventDraft{{
			Type: "agent.model_invocation.completed", Message: invocation.ID, Wakeup: true,
		}},
	})
	if err != nil {
		return snapshot, state, err
	}
	runner.publish(ctx, next.Run.ID, plugin.Event{
		Type: EventModelCompleted, Revision: next.Run.Revision, Status: string(next.Run.Status),
	})
	return next, state, nil
}

func (runner *Runner) claimModelInvocationExecution(
	ctx context.Context,
	snapshot kernel.Snapshot,
	state runState,
	invocation ModelInvocation,
) (kernel.Snapshot, runState, ModelInvocation, error) {
	if invocation.Status == ModelInvocationCompleted {
		return snapshot, state, invocation, nil
	}
	if invocation.Status != ModelInvocationPending {
		return snapshot, state, invocation, ErrInvalidRequest
	}
	now := runner.clock.Now().UTC()
	if invocation.ExecutionLeaseUntil != nil && now.Before(invocation.ExecutionLeaseUntil.UTC()) {
		return snapshot, state, invocation, ErrModelInvocationBusy
	}
	leaseUntil := now.Add(modelInvocationLeaseDuration)
	invocation.ExecutionAttempt++
	invocation.ExecutionLeaseUntil = &leaseUntil
	if !replaceLastModelInvocation(&state, invocation) {
		return snapshot, state, invocation, ErrInvalidRequest
	}
	encoded, err := encodeState(state)
	if err != nil {
		return snapshot, state, invocation, err
	}
	next, err := runner.runtime.Apply(ctx, snapshot.Run.ID, snapshot.Run.Revision, kernel.Mutation{
		Status: kernel.RunStatusRunning, State: encoded, Checkpoint: snapshot.Checkpoint,
		Events: []kernel.EventDraft{{
			Type: "agent.model_invocation.claimed", Message: invocation.ID, Wakeup: true,
			WakeupAt: &leaseUntil,
		}},
	})
	if err != nil {
		return snapshot, state, invocation, err
	}
	return next, state, invocation, nil
}

func (runner *Runner) releaseModelInvocationExecution(
	ctx context.Context,
	snapshot kernel.Snapshot,
	state runState,
	invocation ModelInvocation,
) (kernel.Snapshot, error) {
	if invocation.Status != ModelInvocationPending || invocation.ExecutionLeaseUntil == nil {
		return snapshot, nil
	}
	invocation.ExecutionLeaseUntil = nil
	if !replaceLastModelInvocation(&state, invocation) {
		return snapshot, ErrInvalidRequest
	}
	encoded, err := encodeState(state)
	if err != nil {
		return snapshot, err
	}
	return runner.runtime.Apply(ctx, snapshot.Run.ID, snapshot.Run.Revision, kernel.Mutation{
		Status: kernel.RunStatusRunning, State: encoded, Checkpoint: snapshot.Checkpoint,
		Events: []kernel.EventDraft{{
			Type: "agent.model_invocation.retryable", Message: invocation.ID, Wakeup: true,
		}},
	})
}

func modelDefinitions(invocation ModelInvocation) []tools.Definition {
	result := make([]tools.Definition, len(invocation.Request.Tools))
	for index, definition := range invocation.Request.Tools {
		result[index] = tools.CloneDefinition(definition)
	}
	return result
}

type agentClock struct{}

func (agentClock) Now() time.Time { return time.Now() }
