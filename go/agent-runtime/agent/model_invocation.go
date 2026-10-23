package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"time"

	runtimebudget "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/observability"
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
)

const rejectedModelResponseCode = "agent.model_response_rejected"

// ModelInvocation is the Agent-owned durable intent/receipt for one logical
// model step. Kernel persists it only as opaque feature state.
// ErrorCode marks a known completed but unusable response; its usage is still
// consumed exactly once, without executing any local Tools from that response.
type ModelInvocation struct {
	ID             string `json:"id"`
	RunID          string `json:"runID"`
	SourceRevision uint64 `json:"sourceRevision"`
	RequestHash    string `json:"requestHash"`
	// CatalogFingerprints bind each model-visible Tool to its original Catalog
	// definition. Request.Tools may have a narrower Host projection, but replay
	// still rejects Catalog changes. Entry order matches Request.Tools.
	CatalogFingerprints []string              `json:"catalogFingerprints,omitempty"`
	Provider            string                `json:"provider,omitempty"`
	Model               string                `json:"model,omitempty"`
	Status              ModelInvocationStatus `json:"status"`
	Request             model.Request         `json:"request,omitempty"`
	ProviderResponseID  string                `json:"providerResponseID,omitempty"`
	Response            model.Response        `json:"response,omitempty"`
	Usage               *model.Usage          `json:"usage,omitempty"`
	ErrorCode           string                `json:"errorCode,omitempty"`
	ExecutionAttempt    uint32                `json:"executionAttempt,omitempty"`
	ExecutionLeaseUntil *time.Time            `json:"executionLeaseUntil,omitempty"`
	// RetryAfter is the Feature-owned ReadyAt gate for a released,
	// retryable model call. It is not a second execution lease.
	RetryAfter  *time.Time `json:"retryAfter,omitempty"`
	CreatedAt   time.Time  `json:"createdAt"`
	CompletedAt *time.Time `json:"completedAt,omitempty"`
	ConsumedAt  *time.Time `json:"consumedAt,omitempty"`
}

func telemetryModelUsage(value *model.Usage) runtimebudget.Usage {
	if value == nil {
		return runtimebudget.Usage{}
	}
	total := value.InputTokens + value.OutputTokens
	if value.InputTokens < 0 || value.OutputTokens < 0 || total < 0 {
		total = 0
	}
	return runtimebudget.Usage{
		InputTokens: value.InputTokens, OutputTokens: value.OutputTokens, TotalTokens: total,
		CacheReadTokens: value.CacheReadTokens, CacheWriteTokens: value.CacheWriteTokens,
		ReasoningTokens: value.ReasoningTokens,
	}
}

func newModelInvocation(
	run kernel.Run,
	request model.Request,
	catalogFingerprints []string,
	provider string,
	now time.Time,
) (ModelInvocation, error) {
	request.InvocationID = ""
	hash, err := modelRequestHash(request, catalogFingerprints)
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
		Status: ModelInvocationPending, Request: model.CloneRequest(request),
		CatalogFingerprints: append([]string(nil), catalogFingerprints...), CreatedAt: now.UTC(),
	}, nil
}

func modelRequestHash(request model.Request, fingerprints []string) (string, error) {
	request.InvocationID = ""
	encoded, err := json.Marshal(struct {
		Request             model.Request `json:"request"`
		CatalogFingerprints []string      `json:"catalogFingerprints"`
	}{Request: model.CloneRequest(request), CatalogFingerprints: fingerprints})
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
	invocation.CatalogFingerprints = nil
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
	value.CatalogFingerprints = append([]string(nil), value.CatalogFingerprints...)
	value.Response = model.CloneResponse(value.Response)
	value.Usage = model.CloneUsage(value.Usage)
	if value.ExecutionLeaseUntil != nil {
		leaseUntil := value.ExecutionLeaseUntil.UTC()
		value.ExecutionLeaseUntil = &leaseUntil
	}
	if value.RetryAfter != nil {
		retryAfter := value.RetryAfter.UTC()
		value.RetryAfter = &retryAfter
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
			if invocation.ErrorCode != "" {
				return false
			}
			if invocation.RetryAfter != nil &&
				(invocation.RetryAfter.IsZero() || invocation.ExecutionLeaseUntil != nil) {
				return false
			}
			if index != len(values)-1 || strings.TrimSpace(invocation.Request.InvocationID) != invocation.ID {
				return false
			}
			if !validCatalogFingerprints(invocation.Request.Tools, invocation.CatalogFingerprints) {
				return false
			}
			hash, err := modelRequestHash(invocation.Request, invocation.CatalogFingerprints)
			if err != nil || hash != invocation.RequestHash {
				return false
			}
			if invocation.ExecutionLeaseUntil != nil && invocation.ExecutionAttempt == 0 {
				return false
			}
		case ModelInvocationCompleted:
			active++
			if invocation.RetryAfter != nil || index != len(values)-1 || invocation.CompletedAt == nil ||
				invocation.ExecutionLeaseUntil != nil || strings.TrimSpace(invocation.Request.InvocationID) != invocation.ID ||
				!validModelInvocationReceipt(invocation) {
				return false
			}
			if !validCatalogFingerprints(invocation.Request.Tools, invocation.CatalogFingerprints) {
				return false
			}
			hash, err := modelRequestHash(invocation.Request, invocation.CatalogFingerprints)
			if err != nil || hash != invocation.RequestHash {
				return false
			}
		case ModelInvocationConsumed:
			if len(invocation.CatalogFingerprints) != 0 {
				return false
			}
			if invocation.RetryAfter != nil || invocation.CompletedAt == nil || invocation.ConsumedAt == nil || invocation.ExecutionLeaseUntil != nil ||
				!validModelInvocationReceipt(invocation) {
				return false
			}
		default:
			return false
		}
	}
	return active <= 1
}

func validCatalogFingerprints(definitions []tools.Definition, fingerprints []string) bool {
	if len(definitions) != len(fingerprints) {
		return false
	}
	for _, fingerprint := range fingerprints {
		if len(fingerprint) != sha256.Size*2 {
			return false
		}
		if _, err := hex.DecodeString(fingerprint); err != nil {
			return false
		}
	}
	return true
}

func validModelInvocationReceipt(invocation ModelInvocation) bool {
	if invocation.ErrorCode != "" {
		return invocation.Usage != nil && invocation.Response.Usage != nil &&
			validModelUsage(invocation.Usage) && *invocation.Usage == *invocation.Response.Usage
	}
	return validModelResponse(invocation.Response)
}

func providerName(client model.Client, request model.Request) string {
	if namer, ok := client.(model.RequestProviderNamer); ok {
		return strings.TrimSpace(namer.ProviderNameForRequest(request))
	}
	if namer, ok := client.(model.ProviderNamer); ok {
		return strings.TrimSpace(namer.ProviderName())
	}
	return ""
}

func (runner *Runner) ensureModelInvocation(
	ctx context.Context,
	snapshot kernel.Snapshot,
	state runState,
) (kernel.Snapshot, runState, ModelInvocation, error) {
	if invocation, ok := activeModelInvocation(state); ok {
		return snapshot, state, cloneModelInvocation(*invocation), nil
	}
	request, fingerprints, err := runner.buildModelRequest(ctx, snapshot, state)
	if err != nil {
		// Caller needs the original committed aggregate to fail a stale
		// discovery grant through Kernel CAS. Returning a zero snapshot here
		// obscures policy revocation as invalid Kernel input.
		return snapshot, state, ModelInvocation{}, err
	}
	invocation, err := newModelInvocation(snapshot.Run, request, fingerprints, providerName(runner.model, request), runner.clock.Now())
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
) (model.Request, []string, error) {
	definitions, hostedTools, err := runner.resolveSelectedTools(ctx, state.ToolKeys, state.Model, snapshot.Run.DeadlineAt)
	if err != nil {
		return model.Request{}, nil, err
	}
	if err = verifyHostedGrantVersions(state.HostedToolGrants, hostedTools); err != nil {
		return model.Request{}, nil, err
	}
	if err = validateDiscoveryCatalog(state.Discovery, definitions); err != nil {
		return model.Request{}, nil, err
	}
	if state.Discovery != nil && state.Discovery.RunID != snapshot.Run.ID {
		return model.Request{}, nil, ErrToolDiscoveryInvalid
	}
	definitions = filterLoadedDiscoveryTools(definitions, state.Discovery)
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
	// Unlike identical-call suppression, this detects *consecutive failed
	// outcomes* even when the model changes search terms or Tool arguments.
	// A stopped Tool is removed only from this Run's next declarations; this
	// cannot increase authorized capabilities and requires no new state key.
	if stalled := unproductiveToolKeys(messages); len(stalled) != 0 {
		definitions = definitionsWithoutKeys(definitions, stalled)
		hostedTools = hostedToolsWithoutKeys(hostedTools, stalled)
		messages = withSystemGuidance(messages,
			"Repeated attempts to use these tools returned no authorized match or recoverable failures: "+strings.Join(stalled, ", ")+
				". Do not repeat the same operation using altered parameters. Continue with available evidence or finish now, clearly reporting any missing current information or sources. Never invent citations.")
	}
	if hint := automaticAllowanceHint(state); hint != "" {
		messages = withSystemGuidance(messages, hint)
	}
	request := model.Request{
		RunID: snapshot.Run.ID, Model: state.Model, ModelOptions: cloneRawJSON(state.ModelOptions),
		OutputContract: model.CloneOutputContract(state.OutputContract),
		Messages:       messages, Tools: definitions, HostedTools: model.CloneHostedTools(hostedTools),
		RequireToolCall: state.RequireToolCall && (len(definitions) != 0 || len(hostedTools) != 0),
	}
	if state.CompletionRepair {
		zero := 0
		request.Tools, request.RequireToolCall, request.HostedToolCallLimit = nil, false, &zero
	}
	// Capture Catalog authority before Host-owned, model-facing projection.
	// The final Request and original Catalog fingerprints are both hashed into
	// the same durable ModelInvocation, never recomputed on resume.
	originalTools := append([]tools.Definition(nil), request.Tools...)
	if runner.prepareModelRequest != nil {
		prepared, prepareErr := runner.prepareModelRequest(ctx, model.CloneRequest(request))
		if prepareErr != nil {
			return model.Request{}, nil, prepareErr
		}
		if !validPreparedModelRequest(request, prepared) {
			return model.Request{}, nil, ErrInvalidRequest
		}
		request = model.CloneRequest(prepared)
	}
	fingerprints, err := catalogFingerprintsForRequest(originalTools, request.Tools)
	if err != nil {
		return model.Request{}, nil, err
	}
	return request, fingerprints, nil
}

// PrepareModelRequest may project messages and visible Tool schemas before the
// intent is sealed. It may never change identity, granted Tool keys, provider
// targets or budgets. Dispatch-time ModelMiddleware cannot change them later.
func validPreparedModelRequest(original, prepared model.Request) bool {
	return len(prepared.Messages) > 0 && sameModelRequestIdentity(original, prepared) &&
		prepared.MaxOutputTokens == original.MaxOutputTokens &&
		reflect.DeepEqual(prepared.HostedToolCallLimit, original.HostedToolCallLimit) &&
		prepared.RequireToolCall == original.RequireToolCall &&
		preparedOutputToolsWithinAuthority(original.Tools, prepared.Tools) &&
		reflect.DeepEqual(original.HostedTools, prepared.HostedTools)
}

func sameModelRequestIdentity(frozen, candidate model.Request) bool {
	return frozen.RunID == candidate.RunID && frozen.InvocationID == candidate.InvocationID &&
		frozen.Model == candidate.Model && sameFrozenModelOptions(frozen.ModelOptions, candidate.ModelOptions) &&
		sameFrozenOutputContract(frozen.OutputContract, candidate.OutputContract)
}

// A model-facing schema may narrow or specialize a registered Tool before the
// intent is frozen. Execution still uses the registered Catalog definition.
func preparedOutputToolsWithinAuthority(registered, projected []tools.Definition) bool {
	if len(projected) > len(registered) {
		return false
	}
	byKey := make(map[string]tools.Definition, len(registered))
	for _, definition := range registered {
		if definition.Key == "" || definition.Name == "" {
			return false
		}
		if _, exists := byKey[definition.Key]; exists {
			return false
		}
		byKey[definition.Key] = definition
	}
	seen := make(map[string]bool, len(projected))
	for _, definition := range projected {
		original, ok := byKey[definition.Key]
		if !ok || seen[definition.Key] || definition.Name != original.Name || definition.Terminal != original.Terminal ||
			tools.ValidateDefinition(definition) != nil {
			return false
		}
		seen[definition.Key] = true
	}
	return true
}

func catalogFingerprintsForRequest(registered, projected []tools.Definition) ([]string, error) {
	byKey := make(map[string]tools.Definition, len(registered))
	for _, definition := range registered {
		byKey[definition.Key] = definition
	}
	var fingerprints []string
	for _, definition := range projected {
		original, ok := byKey[definition.Key]
		if !ok {
			return nil, ErrToolDiscoveryDenied
		}
		hash, err := definitionFingerprint(original)
		if err != nil {
			return nil, err
		}
		fingerprints = append(fingerprints, hash)
	}
	return fingerprints, nil
}

func sameFrozenModelMessages(original, candidate []model.Message) bool {
	return reflect.DeepEqual(original, candidate)
}

func sameFrozenOutputContract(original, candidate *model.OutputContract) bool {
	return reflect.DeepEqual(original, candidate)
}

func sameFrozenModelOptions(original, candidate json.RawMessage) bool {
	return reflect.DeepEqual(original, candidate)
}

// A durable invocation permits only budget restrictions and filtering already
// authorized Tool declarations at physical dispatch. All model-visible bytes
// (messages, Tool schemas and descriptions) are sealed before intent commit.
func validDispatchModelRequest(frozen, candidate model.Request) bool {
	return sameModelRequestIdentity(frozen, candidate) &&
		sameFrozenModelMessages(frozen.Messages, candidate.Messages) &&
		dispatchLocalToolsWithinIntent(frozen.Tools, candidate.Tools) &&
		dispatchHostedToolsWithinIntent(frozen.HostedTools, candidate.HostedTools) &&
		dispatchToolChoiceWithinIntent(frozen, candidate) &&
		dispatchOutputCeilingWithinIntent(frozen.MaxOutputTokens, candidate.MaxOutputTokens) &&
		dispatchHostedToolCeilingWithinIntent(frozen.HostedToolCallLimit, candidate.HostedToolCallLimit)
}

// A required Tool call may be relaxed only if dispatch restrictions leave no
// callable local or NEW provider-hosted Tools. Budget exhaustion must not turn
// an otherwise enforceable required call into an optional one by accident.
func dispatchToolChoiceWithinIntent(frozen, candidate model.Request) bool {
	if !frozen.RequireToolCall {
		return !candidate.RequireToolCall
	}
	if candidate.RequireToolCall {
		return true
	}
	return len(candidate.Tools) == 0 && (len(candidate.HostedTools) == 0 ||
		(candidate.HostedToolCallLimit != nil && *candidate.HostedToolCallLimit == 0))
}

// A zero output ceiling means unspecified (not zero new tokens). A positive
// bound may be added to an unbounded intent, but never removed or increased.
func dispatchOutputCeilingWithinIntent(frozen, candidate int64) bool {
	return frozen >= 0 && candidate >= 0 && (frozen == 0 || (candidate > 0 && candidate <= frozen))
}

// A nil native Tool limit is unlimited, while an explicit zero forbids NEW
// calls but still permits previously dispatched completions and provider replay.
func dispatchHostedToolCeilingWithinIntent(frozen, candidate *int) bool {
	if candidate != nil && *candidate < 0 {
		return false
	}
	if frozen == nil {
		return true
	}
	return *frozen >= 0 && candidate != nil && *candidate <= *frozen
}

func dispatchLocalToolsWithinIntent(frozen, candidate []tools.Definition) bool {
	if len(candidate) > len(frozen) {
		return false
	}
	authorized := make(map[string]tools.Definition, len(frozen))
	for _, definition := range frozen {
		if definition.Key == "" || definition.Name == "" {
			return false
		}
		if _, exists := authorized[definition.Key]; exists {
			return false
		}
		authorized[definition.Key] = definition
	}
	seen := make(map[string]bool, len(candidate))
	for _, definition := range candidate {
		original, ok := authorized[definition.Key]
		if !ok || seen[definition.Key] || !reflect.DeepEqual(original, definition) {
			return false
		}
		seen[definition.Key] = true
	}
	return true
}

func dispatchHostedToolsWithinIntent(frozen, candidate []model.HostedTool) bool {
	if len(candidate) > len(frozen) {
		return false
	}
	authorized := make(map[string]model.HostedTool, len(frozen))
	for _, definition := range frozen {
		if definition.Key == "" {
			return false
		}
		if _, exists := authorized[definition.Key]; exists {
			return false
		}
		authorized[definition.Key] = definition
	}
	seen := make(map[string]bool, len(candidate))
	for _, definition := range candidate {
		original, ok := authorized[definition.Key]
		if !ok || seen[definition.Key] || !reflect.DeepEqual(original, definition) {
			return false
		}
		seen[definition.Key] = true
	}
	return true
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
	// A restart may reconstruct a Router with a different default provider.
	// The committed invocation must not silently cross that routing boundary.
	if providerName(runner.model, invocation.Request) != invocation.Provider {
		return snapshot, state, ErrInvalidRequest
	}
	if err := runner.validatePendingModelToolDeclarations(ctx, snapshot, state, invocation); err != nil {
		return snapshot, state, err
	}
	startedAt := runner.clock.Now().UTC()
	runner.recordTelemetry(ctx, observability.Event{
		Scope: observability.ScopeModelInvocation, Phase: observability.PhaseStarted,
		RunID: snapshot.Run.ID, RunKind: RunKind, Revision: snapshot.Run.Revision,
		Status: string(snapshot.Run.Status), OperationID: invocation.ID,
		Operation: "generate", Provider: invocation.Provider, Model: invocation.Model,
		Attempt: int(invocation.ExecutionAttempt), ObservedAt: startedAt,
	})
	runner.publish(ctx, snapshot.Run.ID, plugin.Event{
		Type: EventModelStarted, Revision: snapshot.Run.Revision, Status: string(snapshot.Run.Status),
	})
	response, err := runner.generateModelWithPolicyGuarded(ctx, model.CloneRequest(invocation.Request), snapshot.Run.DeadlineAt,
		func(dispatchCtx context.Context) error {
			if err := runner.runtime.RequireExecution(dispatchCtx, snapshot.Run.ID); err != nil {
				return err
			}
			return runner.validatePendingModelToolDeclarations(dispatchCtx, snapshot, state, invocation)
		},
	)
	late := errors.Is(err, ErrModelLateResult)
	response.Content = strings.TrimSpace(response.Content)
	if err == nil && response.Usage != nil && validModelUsage(response.Usage) && !validModelResponse(response) {
		err = model.NewResponseRejectedError(ErrInvalidModelResponse)
	}
	knownRejection := model.IsResponseRejectedError(err) && response.Usage != nil && validModelUsage(response.Usage)
	if err != nil && !late && !knownRejection {
		endedAt := runner.clock.Now().UTC()
		phase := observability.PhaseFailed
		errorCode := "provider_error"
		cause := errors.Join(ErrModelFailure, err)
		if ctxErr := ctx.Err(); ctxErr != nil {
			phase = observability.PhaseCancelled
			errorCode = "cancelled"
			cause = ctxErr
		} else if errors.Is(err, ErrModelTimeout) {
			errorCode = "timeout"
		} else if errors.Is(err, ErrModelPanic) {
			errorCode = "panic"
		} else if errors.Is(err, kernel.ErrDeadline) {
			errorCode = "deadline"
		}
		runner.recordTelemetry(ctx, observability.Event{
			Scope: observability.ScopeModelInvocation, Phase: phase,
			RunID: snapshot.Run.ID, RunKind: RunKind, Revision: snapshot.Run.Revision,
			Status: string(snapshot.Run.Status), OperationID: invocation.ID,
			Operation: "generate", Provider: invocation.Provider, Model: invocation.Model,
			Attempt: int(invocation.ExecutionAttempt), ErrorCode: errorCode,
			ObservedAt: endedAt, Duration: endedAt.Sub(startedAt),
		})
		return snapshot, state, cause
	}
	if !knownRejection && !validModelResponse(response) {
		endedAt := runner.clock.Now().UTC()
		runner.recordTelemetry(ctx, observability.Event{
			Scope: observability.ScopeModelInvocation, Phase: observability.PhaseFailed,
			RunID: snapshot.Run.ID, RunKind: RunKind, Revision: snapshot.Run.Revision,
			Status: string(snapshot.Run.Status), OperationID: invocation.ID,
			Operation: "generate", Provider: invocation.Provider, Model: invocation.Model,
			Attempt: int(invocation.ExecutionAttempt), ErrorCode: "invalid_response",
			ObservedAt: endedAt, Duration: endedAt.Sub(startedAt),
		})
		return snapshot, state, ErrInvalidModelResponse
	}
	completedAt := runner.clock.Now().UTC()
	invocation.Status = ModelInvocationCompleted
	invocation.ProviderResponseID = strings.TrimSpace(response.ResponseID)
	invocation.Response = model.CloneResponse(response)
	invocation.Usage = model.CloneUsage(response.Usage)
	if knownRejection {
		invocation.ErrorCode = model.ClassifiedErrorCode(err)
		if invocation.ErrorCode == "" {
			invocation.ErrorCode = rejectedModelResponseCode
		}
	}
	invocation.ExecutionLeaseUntil = nil
	invocation.CompletedAt = &completedAt
	if !replaceLastModelInvocation(&state, invocation) {
		return kernel.Snapshot{}, runState{}, ErrInvalidRequest
	}
	encoded, err := encodeState(state)
	if err != nil {
		return kernel.Snapshot{}, runState{}, err
	}
	next, err := runner.runtime.Apply(context.WithoutCancel(ctx), snapshot.Run.ID, snapshot.Run.Revision, kernel.Mutation{
		Status: kernel.RunStatusRunning, State: encoded, Checkpoint: snapshot.Checkpoint,
		Events: []kernel.EventDraft{{
			Type: "agent.model_invocation.completed", Message: invocation.ID, Wakeup: true,
		}},
	})
	if err != nil {
		endedAt := runner.clock.Now().UTC()
		runner.recordTelemetry(ctx, observability.Event{
			Scope: observability.ScopeModelInvocation, Phase: observability.PhaseFailed,
			RunID: snapshot.Run.ID, RunKind: RunKind, Revision: snapshot.Run.Revision,
			Status: string(snapshot.Run.Status), OperationID: invocation.ID,
			Operation: "generate", Provider: invocation.Provider, Model: invocation.Model,
			ResponseID: response.ResponseID, Attempt: int(invocation.ExecutionAttempt), ErrorCode: "receipt_persist_failed",
			ObservedAt: endedAt, Duration: endedAt.Sub(startedAt), Usage: telemetryModelUsage(response.Usage),
		})
		return snapshot, state, err
	}
	endedAt := runner.clock.Now().UTC()
	runner.recordTelemetry(ctx, observability.Event{
		Scope: observability.ScopeModelInvocation, Phase: observability.PhaseCompleted,
		RunID: next.Run.ID, RunKind: RunKind, Revision: next.Run.Revision,
		Status: string(next.Run.Status), OperationID: invocation.ID,
		Operation: "generate", Provider: invocation.Provider, Model: invocation.Model,
		ResponseID: response.ResponseID, Attempt: int(invocation.ExecutionAttempt), ObservedAt: endedAt,
		Duration: endedAt.Sub(startedAt), Usage: telemetryModelUsage(response.Usage), ErrorCode: invocation.ErrorCode,
	})
	runner.publish(ctx, next.Run.ID, plugin.Event{
		Type: EventModelCompleted, Revision: next.Run.Revision, Status: string(next.Run.Status),
	})
	runner.publishHostedToolReceipts(ctx, next, response.HostedToolCalls)
	if late {
		return next, state, ErrModelLateResult
	}
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
	if (invocation.ExecutionLeaseUntil != nil && now.Before(invocation.ExecutionLeaseUntil.UTC())) ||
		(invocation.RetryAfter != nil && now.Before(invocation.RetryAfter.UTC())) {
		return snapshot, state, invocation, ErrModelInvocationBusy
	}
	leaseUntil := now.Add(runner.modelLeaseDuration())
	invocation.RetryAfter = nil
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
	delay := retryDelay(runner.execution.Model, int(invocation.ExecutionAttempt), invocation.ID)
	wakeupAt := runner.clock.Now().UTC().Add(delay)
	if runner.runExecution != nil {
		// Only native opted-in execution promises a shared ReadyAt
		// eligibility gate. Legacy explicit Resume retains its previous
		// immediate-retry semantics until a separate compatibility cut.
		invocation.RetryAfter = &wakeupAt
	}
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
			Type: "agent.model_invocation.retryable", Message: invocation.ID, Wakeup: true, WakeupAt: &wakeupAt,
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
