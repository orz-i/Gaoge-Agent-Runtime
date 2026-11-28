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
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/jsoncontract"
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
	ID             string                 `json:"id"`
	RunID          string                 `json:"runID"`
	SourceRevision uint64                 `json:"sourceRevision"`
	RequestHash    string                 `json:"requestHash"`
	Purpose        string                 `json:"purpose,omitempty"`
	ContextSummary *ContextSummaryBinding `json:"contextSummary,omitempty"`
	// SummaryTargetHash records the installed state, never a second receipt.
	SummaryTargetHash string `json:"summaryTargetHash,omitempty"`
	// CatalogFingerprints bind each model-visible Tool to its original Catalog
	// definition. Request.Tools may have a narrower Host projection, but replay
	// still rejects Catalog changes. Entry order matches Request.Tools.
	CatalogFingerprints []string `json:"catalogFingerprints,omitempty"`
	Provider            string   `json:"provider,omitempty"`
	// BindingFingerprint freezes the effective named Provider binding without
	// persisting endpoint addresses or credentials. Unnamed local Model clients
	// are not identified as external Provider bindings.
	BindingFingerprint  string                `json:"bindingFingerprint,omitempty"`
	Model               string                `json:"model,omitempty"`
	Status              ModelInvocationStatus `json:"status"`
	Request             model.Request         `json:"request,omitempty"`
	SamplingPlan        *SamplingPlan         `json:"samplingPlan,omitempty"`
	ProviderResponseID  string                `json:"providerResponseID,omitempty"`
	Response            model.Response        `json:"response,omitempty"`
	Usage               *model.Usage          `json:"usage,omitempty"`
	ErrorCode           string                `json:"errorCode,omitempty"`
	OutcomeErrorCode    string                `json:"outcomeErrorCode,omitempty"`
	OutcomeErrorDetail  string                `json:"outcomeErrorDetail,omitempty"`
	DispatchAttempt     uint32                `json:"dispatchAttempt,omitempty"`
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

func newInvocation(
	run kernel.Run,
	request model.Request,
	catalogFingerprints []string,
	provider, bindingFingerprint, purpose string,
	summary *ContextSummaryBinding,
	now time.Time,
) (ModelInvocation, error) {
	if err := model.ValidateRequestBinding(request); err != nil {
		return ModelInvocation{}, errors.Join(ErrInvalidRequest, err)
	}
	plannedID := request.InvocationID
	request.InvocationID = ""
	hash, err := modelInvocationRequestHash(request, catalogFingerprints, provider, bindingFingerprint, purpose, summary)
	if err != nil {
		return ModelInvocation{}, err
	}
	idSource := run.ID + "\x00" + strconv.FormatUint(run.Revision, 10) + "\x00" + hash
	idDigest := sha256.Sum256([]byte(idSource))
	id := "modelinv_" + hex.EncodeToString(idDigest[:16])
	if plannedID != "" && plannedID != id {
		return ModelInvocation{}, ErrInvalidRequest
	}
	request.InvocationID = id
	result := ModelInvocation{
		ID: id, RunID: run.ID, SourceRevision: run.Revision, RequestHash: hash,
		Purpose: purpose, ContextSummary: cloneContextSummaryBinding(summary),
		Provider: strings.TrimSpace(provider), BindingFingerprint: bindingFingerprint,
		Model: strings.TrimSpace(request.Model), Status: ModelInvocationPending, Request: model.CloneRequest(request),
		CatalogFingerprints: append([]string(nil), catalogFingerprints...), CreatedAt: now.UTC(),
	}
	if !validContextSummaryInvocation(result) {
		return ModelInvocation{}, ErrInvalidRequest
	}
	return result, nil
}

func modelRequestHash(request model.Request, fingerprints []string, provider, bindingFingerprint string) (string, error) {
	return modelInvocationRequestHash(request, fingerprints, provider, bindingFingerprint, "", nil)
}

func modelInvocationRequestHash(request model.Request, fingerprints []string, provider, bindingFingerprint, purpose string, summary *ContextSummaryBinding) (string, error) {
	if err := model.ValidateRequestBinding(request); err != nil {
		return "", errors.Join(ErrInvalidRequest, err)
	}
	request.InvocationID = ""
	proof, err := jsoncontract.SealOpaque("agent-model-intent-opaque-v2", struct {
		Request             model.Request          `json:"request"`
		CatalogFingerprints []string               `json:"catalogFingerprints"`
		Provider            string                 `json:"provider"`
		BindingFingerprint  string                 `json:"bindingFingerprint"`
		Purpose             string                 `json:"purpose,omitempty"`
		ContextSummary      *ContextSummaryBinding `json:"contextSummary,omitempty"`
	}{Request: model.CloneRequest(request), CatalogFingerprints: fingerprints,
		Provider: provider, BindingFingerprint: bindingFingerprint, Purpose: purpose, ContextSummary: summary})
	if err != nil {
		return "", errors.Join(ErrInvalidRequest, err)
	}
	return proof.Digest, nil
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
	invocation.SamplingPlan = nil
	// The stored receipt already belongs to this state. The returned consumed
	// value above is isolated, so copying this entire response again only
	// duplicates opaque replay and Tool payloads without adding isolation.
	state.ModelInvocations[index] = invocation
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
	value.ContextSummary = cloneContextSummaryBinding(value.ContextSummary)
	value.SamplingPlan = cloneSamplingPlan(value.SamplingPlan)
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
	seenPhysical := make(map[[3]string]struct{})
	for index, invocation := range values {
		if !validContextSummaryInvocation(invocation) || !contextSummaryInputBytesWithinPolicy(invocation) ||
			(invocation.SamplingPlan != nil && (invocation.Status == ModelInvocationConsumed ||
				!invocation.SamplingPlan.valid(invocation.Request))) {
			return false
		}
		if strings.TrimSpace(invocation.ID) == "" || strings.TrimSpace(invocation.RunID) == "" ||
			invocation.SourceRevision == 0 || len(invocation.RequestHash) != sha256.Size*2 ||
			invocation.CreatedAt.IsZero() {
			return false
		}
		if (invocation.Status == ModelInvocationPending || invocation.Status == ModelInvocationCompleted) &&
			(invocation.Provider != "" || invocation.BindingFingerprint != "") &&
			!validBindingFingerprint(invocation.BindingFingerprint) {
			return false
		}
		if invocation.DispatchAttempt > invocation.ExecutionAttempt || (invocation.DispatchAttempt != 0 && invocation.DispatchAttempt != invocation.ExecutionAttempt) {
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
			hash, err := modelInvocationRequestHash(invocation.Request, invocation.CatalogFingerprints, invocation.Provider, invocation.BindingFingerprint, invocation.Purpose, invocation.ContextSummary)
			if err != nil || hash != invocation.RequestHash {
				return false
			}
			if (invocation.ExecutionLeaseUntil != nil && invocation.ExecutionAttempt == 0) ||
				(invocation.ExecutionAttempt > 0 && invocation.ExecutionLeaseUntil == nil && invocation.RetryAfter == nil) {
				// A released attempt must always carry its durable retry due
				// time. The old direct-Resume immediate-retry state is invalid.
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
			hash, err := modelInvocationRequestHash(invocation.Request, invocation.CatalogFingerprints, invocation.Provider, invocation.BindingFingerprint, invocation.Purpose, invocation.ContextSummary)
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
		if invocation.Provider != "" && invocation.BindingFingerprint != "" &&
			(invocation.Status == ModelInvocationCompleted || invocation.Status == ModelInvocationConsumed) {
			key := [3]string{invocation.Provider, invocation.BindingFingerprint, invocation.ProviderResponseID}
			if _, duplicate := seenPhysical[key]; duplicate {
				return false
			}
			seenPhysical[key] = struct{}{}
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
	// Restarted Runs must not trust legacy completed/consumed named Provider
	// receipts whose original physical identity was never established. Agent
	// normalizes ProviderResponseID when writing it; insist that the stored
	// ID still exactly matches the unmodified Provider response evidence.
	if invocation.Provider != "" || invocation.BindingFingerprint != "" {
		if invocation.Provider == "" || !validBindingFingerprint(invocation.BindingFingerprint) ||
			invocation.ProviderResponseID == "" ||
			strings.TrimSpace(invocation.ProviderResponseID) != invocation.ProviderResponseID ||
			invocation.ProviderResponseID != invocation.Response.ResponseID {
			return false
		}
	}
	if invocation.ErrorCode != "" {
		return invocation.Usage != nil && invocation.Response.Usage != nil &&
			model.ValidUsage(invocation.Usage) && *invocation.Usage == *invocation.Response.Usage
	}
	return model.ValidResponse(invocation.Response)
}

func validBindingFingerprint(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && hex.EncodeToString(decoded) == value
}

func declaresProviderIdentity(client model.Client) bool {
	if _, ok := client.(model.RequestProviderNamer); ok {
		return true
	}
	_, ok := client.(model.ProviderNamer)
	return ok
}

func modelBindingFingerprint(client model.Client, request model.Request) string {
	if binder, ok := client.(model.RequestBindingFingerprinter); ok {
		return binder.BindingFingerprintForRequest(request)
	}
	return ""
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

// matchesFrozenProviderBinding is evaluated before reclaiming a persisted
// pending intent as well as at physical dispatch. A changed Host binding must
// not acquire another durable Model execution attempt simply to be rejected.
func matchesFrozenProviderBinding(client model.Client, invocation ModelInvocation) bool {
	if !matchesProviderBindingForRequest(client, invocation, invocation.Request) {
		return false
	}
	return invocation.SamplingPlan == nil || matchesProviderBindingForRequest(client, invocation, invocation.SamplingPlan.request(invocation.Request))
}

func matchesProviderBindingForRequest(client model.Client, invocation ModelInvocation, request model.Request) bool {
	if invocation.Provider == "" && declaresProviderIdentity(client) {
		return false
	}
	if providerName(client, request) != invocation.Provider {
		return false
	}
	if invocation.Provider == "" && invocation.BindingFingerprint == "" {
		return true // No external Provider identity was declared by this Client.
	}
	return validBindingFingerprint(invocation.BindingFingerprint) &&
		modelBindingFingerprint(client, request) == invocation.BindingFingerprint
}

func (runner *Runner) ensureModelInvocation(
	ctx context.Context,
	snapshot kernel.Snapshot,
	state runState,
) (kernel.Snapshot, runState, ModelInvocation, error) {
	if invocation, ok := activeModelInvocation(state); ok {
		if invocation.Status == ModelInvocationPending && !matchesFrozenProviderBinding(runner.model, *invocation) {
			return snapshot, state, ModelInvocation{}, ErrProviderBindingMismatch
		}
		return snapshot, state, cloneModelInvocation(*invocation), nil
	}
	invocation, err := runner.buildInvocation(ctx, snapshot, state)
	if err != nil {
		return snapshot, state, ModelInvocation{}, err
	}
	if !matchesFrozenProviderBinding(runner.model, invocation) {
		return snapshot, state, ModelInvocation{}, ErrInvalidRequest
	}
	state.ModelInvocations = append(state.ModelInvocations, cloneModelInvocation(invocation))
	encoded, err := encodeState(state)
	if err != nil {
		return kernel.Snapshot{}, runState{}, ModelInvocation{}, err
	}
	next, err := runner.commitNativeTransition(ctx, snapshot, kernel.Mutation{
		Status: kernel.RunStatusRunning, State: encoded, Checkpoint: snapshot.Checkpoint,
		Events: []kernel.EventDraft{{Type: "agent.model_invocation.pending", Message: invocation.ID, Wakeup: true}},
	})
	if err != nil {
		return kernel.Snapshot{}, runState{}, ModelInvocation{}, err
	}
	return next, state, invocation, nil
}

func (runner *Runner) buildUnpreparedModelRequest(ctx context.Context, snapshot kernel.Snapshot, state runState) (model.Request, []tools.Definition, error) {
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
	definitions, hostedTools, messages := projectModelPreparation(state, definitions, hostedTools)
	request := model.Request{
		RunID: snapshot.Run.ID, Model: state.Model, ModelOptions: cloneRawJSON(state.ModelOptions),
		OutputContract:  model.CloneOutputContract(state.OutputContract),
		MaxOutputTokens: state.MaxModelOutputTokens,
		Messages:        messages, Tools: definitions, HostedTools: model.CloneHostedTools(hostedTools),
		RequireToolCall: state.RequireToolCall && (len(definitions) != 0 || len(hostedTools) != 0),
	}
	if state.CompletionRepair {
		zero := 0
		request.Tools, request.RequireToolCall, request.HostedToolCallLimit = nil, false, &zero
	}
	request, err = runner.normalizeModelPolicy(ctx, request)
	if err != nil {
		return model.Request{}, nil, err
	}
	// Capture Catalog authority before Host-owned, model-facing projection.
	// The final Request and original Catalog fingerprints are both hashed into
	// the same durable ModelInvocation, never recomputed on resume.
	return request, append([]tools.Definition(nil), request.Tools...), nil
}

// The pre-intent message projection is pure. Recovery can verify a committed
// Context candidate against the exact durable state without selecting Tools,
// calling a Provider, resolving a window or measuring tokens.
func projectModelPreparation(state runState, definitions []tools.Definition, hostedTools []model.HostedTool) ([]tools.Definition, []model.HostedTool, []model.Message) {
	messages := model.CloneMessages(state.Messages)
	if len(state.BlockedToolKeys) != 0 {
		definitions = definitionsWithoutKeys(definitions, state.BlockedToolKeys)
		hostedTools = hostedToolsWithoutKeys(hostedTools, state.BlockedToolKeys)
		messages = withBlockedToolGuidance(messages, state.BlockedToolKeys)
	}
	definitions, hostedTools, messages = projectToolProgress(state, definitions, hostedTools, messages)
	if hint := automaticAllowanceHint(state); hint != "" {
		messages = withSystemGuidance(messages, hint)
	}
	return definitions, hostedTools, messages
}

func (runner *Runner) buildInvocation(ctx context.Context, snapshot kernel.Snapshot, state runState) (ModelInvocation, error) {
	request, definitions, err := runner.buildUnpreparedModelRequest(ctx, snapshot, state)
	if err != nil {
		return ModelInvocation{}, err
	}
	preparation := &modelPreparation{run: snapshot.Run, client: runner.model, now: runner.clock.Now(),
		original: model.CloneRequest(request), catalog: definitions, budget: runner.samplingBudget}
	defer preparation.close()
	if summaryEligible(snapshot, state) {
		if runner.contextSummary == nil {
			return ModelInvocation{}, ErrInvalidRequest
		}
		// A configured but unused Summary owner is not an entered callback.
		// The actual planning phase retains the conservative Host boundary.
		if runner.rootCallbacks != nil {
			runner.rootCallbacks.summaryPhaseEntered.Store(true)
		}
		planned, ok, planErr := runner.contextSummary.plan(ctx, runner, snapshot, state, preparation)
		if planErr != nil {
			return ModelInvocation{}, planErr
		}
		if ok {
			return planned, nil
		}
	}
	ctx = context.WithValue(ctx, modelPreparationKey{}, preparation)
	if runner.boundedContext != nil {
		prepared, err := runner.prepareBoundedContext(ctx, snapshot, request)
		if err != nil {
			return ModelInvocation{}, err
		}
		request = prepared
	} else if runner.prepareModelRequest != nil {
		prepared, prepareErr := runner.prepareModelRequest(ctx, model.CloneRequest(request))
		runner.noteRootCallbackFailure(prepareErr)
		if prepareErr != nil {
			return ModelInvocation{}, prepareErr
		}
		if !validPreparedModelRequest(request, prepared) {
			return ModelInvocation{}, ErrInvalidRequest
		}
		request = model.CloneRequest(prepared)
	}
	return preparation.finish(ctx, request)
}

// PrepareModelRequest may project messages and visible Tool schemas before the
// intent is sealed. It may add immutable Host binding metadata and option defaults,
// but never override caller options, granted Tool keys, provider targets or budgets.
// Dispatch-time ModelMiddleware cannot change any of the sealed fields later.
func validPreparedModelRequest(original, prepared model.Request) bool {
	// The preparer may freeze new Host binding metadata, but cannot replace metadata already supplied.
	bindingOriginal := original
	if !additivePreparedOptions(original.ModelOptions, prepared.ModelOptions) {
		return false
	}
	bindingOriginal.ModelOptions = prepared.ModelOptions
	if bindingOriginal.BindingRef == "" {
		bindingOriginal.BindingRef = prepared.BindingRef
	}
	if bindingOriginal.PromptCacheKey == "" {
		bindingOriginal.PromptCacheKey = prepared.PromptCacheKey
	}
	if bindingOriginal.PreviousResponseID == "" {
		bindingOriginal.PreviousResponseID = prepared.PreviousResponseID
	}
	return model.ValidateRequestBinding(prepared) == nil && len(prepared.Messages) > 0 && sameModelRequestIdentity(bindingOriginal, prepared) &&
		prepared.MaxOutputTokens == original.MaxOutputTokens &&
		reflect.DeepEqual(prepared.HostedToolCallLimit, original.HostedToolCallLimit) &&
		prepared.RequireToolCall == original.RequireToolCall &&
		preparedOutputToolsWithinAuthority(original.Tools, prepared.Tools) &&
		reflect.DeepEqual(original.HostedTools, prepared.HostedTools)
}

func sameModelRequestIdentity(frozen, candidate model.Request) bool {
	return frozen.BindingRef == candidate.BindingRef && frozen.PromptCacheKey == candidate.PromptCacheKey && frozen.PreviousResponseID == candidate.PreviousResponseID && frozen.RunID == candidate.RunID && frozen.InvocationID == candidate.InvocationID &&
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

// Active intents retain the Run's exact sampling ceiling across restart.
// Dispatch-only narrowing never rewrites the intent, and consumed receipts
// no longer retain their request.
func modelInvocationsMatchOutputCeiling(invocations []ModelInvocation, ceiling int64) bool {
	for _, invocation := range invocations {
		if (invocation.Status == ModelInvocationPending || invocation.Status == ModelInvocationCompleted) &&
			invocation.Request.MaxOutputTokens != ceiling {
			return false
		}
	}
	return true
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

// Reusing one physical Provider completion ID for a different durable
// ModelInvocation cannot prove a second generation or authorize a new local
// Tool side effect. Retries of the *same* pending invocation are unaffected.
// Scope this check to the same named Provider binding: independent providers
// and changed Host bindings may legitimately issue colliding opaque IDs.
func duplicatePhysicalProviderResponseID(state runState, current ModelInvocation, responseID string) bool {
	responseID = strings.TrimSpace(responseID)
	if current.Provider == "" || current.BindingFingerprint == "" || responseID == "" {
		return false
	}
	for _, previous := range state.ModelInvocations {
		if previous.ID == current.ID ||
			(previous.Status != ModelInvocationCompleted && previous.Status != ModelInvocationConsumed) {
			continue
		}
		if previous.Provider == current.Provider &&
			previous.BindingFingerprint == current.BindingFingerprint &&
			previous.ProviderResponseID == responseID {
			return true
		}
	}
	return false
}

func (runner *Runner) executeModelInvocation(
	ctx context.Context,
	snapshot kernel.Snapshot,
	state runState,
	invocation ModelInvocation,
) (kernel.Snapshot, runState, error) {
	if invocation.Status == ModelInvocationCompleted {
		return snapshot, state, runner.requireSettledModelQualification(ctx, invocation)
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
	if !matchesFrozenProviderBinding(runner.model, invocation) {
		return snapshot, state, ErrProviderBindingMismatch
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
	authority := modelCompletionAuthority{
		named:   invocation.Provider != "" && invocation.BindingFingerprint != "",
		request: model.CloneRequest(invocation.Request),
		plan:    cloneSamplingPlan(invocation.SamplingPlan),
		duplicate: func(id string) bool {
			return duplicatePhysicalProviderResponseID(state, invocation, id)
		},
	}
	generateCtx := ctx
	if invocation.SamplingPlan != nil {
		if (invocation.SamplingPlan.LedgerID != "" && runner.samplingBudget == nil) || !invocation.SamplingPlan.valid(invocation.Request) {
			return snapshot, state, ErrInvalidRequest
		}
	}
	if invocation.Purpose == ModelInvocationContextSummary {
		generateCtx = context.WithValue(ctx, contextSummaryModelKey{}, true)
	}
	atomicBudget := runner.atomicModelBudget(invocation.SamplingPlan)
	response, err := runner.generateModelWithPolicyGuarded(generateCtx, model.CloneRequest(invocation.Request), snapshot.Run.DeadlineAt,
		func(dispatchCtx context.Context, candidate model.Request) error {
			// Resolve and revalidate Catalog authority first: a slow external
			// Hosted Tool resolver can also outlive the claim lease. The
			// committed Revision/lease and Store epoch checks MUST be last,
			// after all such waits and immediately before Provider dispatch.
			if err := runner.validatePendingModelToolDeclarations(dispatchCtx, snapshot, state, invocation); err != nil {
				return err
			}
			// The named binding can change while ModelMiddleware, Tool
			// discovery or the shared concurrency limiter holds this call.
			// Recheck after every such wait, before the final claim/epoch
			// guards and physical Provider dispatch.
			if !matchesProviderBindingForRequest(runner.model, invocation, candidate) {
				return ErrProviderBindingMismatch
			}
			var ledgerMutation *runtimebudget.LedgerMutation
			if atomicBudget != nil {
				var planErr error
				ledgerMutation, planErr = atomicBudget.planModelDispatch(dispatchCtx, invocation.Request, candidate, invocation.SamplingPlan)
				if planErr != nil {
					return planErr
				}
			}
			if err := runner.validateModelInvocationClaim(dispatchCtx, snapshot, invocation); err != nil {
				return err
			}
			if err := runner.runtime.RequireExecution(dispatchCtx, snapshot.Run.ID); err != nil {
				return err
			}
			if invocation.DispatchAttempt != 0 {
				return ErrModelOutcomeUnknown
			}
			if invocation.SamplingPlan == nil {
				plan, err := newPhysicalSamplingPlan(invocation.Request, candidate)
				if err != nil {
					return err
				}
				invocation.SamplingPlan = plan
			}
			invocation.DispatchAttempt = invocation.ExecutionAttempt
			if !replaceLastModelInvocation(&state, invocation) {
				return ErrInvalidRequest
			}
			encoded, err := encodeState(state)
			if err != nil {
				return err
			}
			staged, err := runner.commitNativeTransition(dispatchCtx, snapshot, kernel.Mutation{
				Ledger: ledgerMutation,
				Status: kernel.RunStatusRunning, State: encoded, Checkpoint: snapshot.Checkpoint,
				Events: []kernel.EventDraft{{Type: "agent.model_invocation.dispatch_intent", Message: invocation.ID, Wakeup: true}},
			})
			if err != nil {
				return err
			}
			snapshot = staged
			return nil
		},
		&authority,
	)
	if err == nil && response.Usage != nil && model.ValidUsage(response.Usage) && !model.ValidResponse(response) {
		err = model.NewResponseRejectedError(ErrInvalidModelResponse)
	}
	// Validate against the original receipt, not just another syntactically
	// valid ID. The same authority also fences downstream shared settlement.
	if atomicBudget != nil {
		response, err = authority.validate(response, err)
	} else {
		response, err = authority.validateFinal(response, err)
	}
	late := errors.Is(err, ErrModelLateResult)
	knownRejection := model.IsResponseRejectedError(err) && response.Usage != nil && model.ValidUsage(response.Usage)
	if knownRejection {
		// The rejected assistant reply is NOT an authorized transcript or
		// executable Tool payload. Persist only the verified billing receipt
		// even for custom named clients and Model middleware that return
		// an unsanitized rejected Response.
		response = model.BillingOnlyResponse(response)
	}
	var denied modelPreDispatchDenied
	if errors.As(err, &denied) {
		// Nothing reached the Provider. Keep the committed pending Claim
		// recoverable without consuming a retry or forging a Model receipt.
		// Retain any downstream budget reconciliation errors as well.
		return snapshot, state, err
	}
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
	if !knownRejection && !model.ValidResponse(response) {
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
	var ledgerMutation *runtimebudget.LedgerMutation
	physicalResponse, physicalErr := response, err
	if late {
		physicalErr = nil
	}
	accounting := context.WithoutCancel(ctx)
	if atomicBudget != nil {
		var receiptErr, planErr error
		ledgerMutation, response, receiptErr, planErr = atomicBudget.planModelReceipt(accounting, invocation, physicalResponse, physicalErr)
		if planErr != nil {
			return snapshot, state, errors.Join(ErrModelOutcomeUnknown, planErr)
		}
		response, err = authority.settle(response, receiptErr)
		knownRejection = model.IsResponseRejectedError(err) && response.Usage != nil && model.ValidUsage(response.Usage)
		if err != nil && !knownRejection {
			return snapshot, state, errors.Join(ErrModelOutcomeUnknown, err)
		}
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
	mutation := kernel.Mutation{
		Ledger: ledgerMutation,
		Status: kernel.RunStatusRunning, State: encoded, Checkpoint: snapshot.Checkpoint,
		Events: []kernel.EventDraft{{
			Type: "agent.model_invocation.completed", Message: invocation.ID, Wakeup: true,
		}},
	}
	next, err := runner.commitNativeTransition(accounting, snapshot, mutation)
	// A competing Run may advance the shared ledger without changing this
	// Run's revision. Replan only the same already-returned physical receipt;
	// never retry the Provider, or change the qualified receipt on a stale CAS.
	for attempt := 0; atomicBudget != nil && errors.Is(err, runtimebudget.ErrLedgerConflict) && attempt < 7; attempt++ {
		replanned, receipt, receiptErr, planErr := atomicBudget.planModelReceipt(accounting, invocation, physicalResponse, physicalErr)
		if planErr != nil {
			err = errors.Join(err, planErr)
			break
		}
		code := model.ClassifiedErrorCode(receiptErr)
		if model.IsResponseRejectedError(receiptErr) && code == "" {
			code = rejectedModelResponseCode
		}
		if !samePersistedModelResponse(response, receipt) || code != invocation.ErrorCode {
			err = errors.Join(err, ErrInvalidModelResponse)
			break
		}
		ledgerMutation, mutation.Ledger = replanned, replanned
		next, err = runner.commitNativeTransition(accounting, snapshot, mutation)
	}
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
		return snapshot, state, errors.Join(ErrModelOutcomeUnknown, err)
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
	if invocation.Purpose != ModelInvocationContextSummary {
		runner.publishHostedToolReceipts(ctx, next, response.HostedToolCalls)
	}
	if ledgerMutation != nil {
		if projectErr := atomicBudget.ports.Project(context.WithoutCancel(ctx), ledgerMutation.Next()); projectErr != nil {
			// The original receipt and charge have already committed. The projection
			// cannot replace their result or grant another provider dispatch.
			return next, state, errors.Join(ErrModelOutcomeUnknown, projectErr)
		}
	}
	if late {
		return next, state, ErrModelLateResult
	}
	return next, state, nil
}

// validateModelInvocationClaim checks the current committed Run Revision
// AFTER all ModelMiddleware and semaphore waits, at the physical Provider
// boundary. Claim identity, attempt and hash are already sealed by the
// previous successful Kernel Apply, so any valid claimant/takeover or other
// change necessarily increments Revision. Do not unmarshal the full Agent
// state a second time here: that would amplify long-session read costs.
// The local Claim lease is checked independently, including in unfenced Runs.
func (runner *Runner) validateModelInvocationClaim(
	ctx context.Context, snapshot kernel.Snapshot, claimed ModelInvocation,
) error {
	if claimed.Status != ModelInvocationPending || claimed.ExecutionAttempt == 0 ||
		claimed.ExecutionLeaseUntil == nil ||
		!runner.clock.Now().UTC().Before(claimed.ExecutionLeaseUntil.UTC()) {
		return ErrModelInvocationBusy
	}
	current, err := runner.runtime.Load(ctx, snapshot.Run.ID)
	if err != nil {
		return err
	}
	if current.Run.Status != kernel.RunStatusRunning || current.Run.Revision != snapshot.Run.Revision {
		return kernel.ErrConflict
	}
	return nil
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
	if invocation.DispatchAttempt != 0 {
		return snapshot, state, invocation, ErrModelOutcomeUnknown
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
	next, err := runner.commitNativeTransition(ctx, snapshot, kernel.Mutation{
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
	if invocation.DispatchAttempt != 0 {
		return snapshot, ErrModelOutcomeUnknown
	}
	if invocation.Status != ModelInvocationPending || invocation.ExecutionLeaseUntil == nil {
		return snapshot, nil
	}
	invocation.ExecutionLeaseUntil = nil
	delay := retryDelay(runner.execution.Model, int(invocation.ExecutionAttempt), invocation.ID)
	wakeupAt := runner.clock.Now().UTC().Add(delay)
	// One Feature-owned retry eligibility applies to every Agent Runner.
	// No legacy immediate-Resume compatibility path remains.
	invocation.RetryAfter = &wakeupAt
	if !replaceLastModelInvocation(&state, invocation) {
		return snapshot, ErrInvalidRequest
	}
	encoded, err := encodeState(state)
	if err != nil {
		return snapshot, err
	}
	return runner.commitNativeTransition(ctx, snapshot, kernel.Mutation{
		Status: kernel.RunStatusRunning, State: encoded, Checkpoint: snapshot.Checkpoint,
		Events: []kernel.EventDraft{{
			Type: "agent.model_invocation.retryable", Message: invocation.ID, Wakeup: true, WakeupAt: &wakeupAt,
		}},
	})
}

// The physical Tool subset and its original Catalog fingerprints travel
// together. A narrowed or reordered plan cannot misbind a logical index.
func modelDefinitions(invocation ModelInvocation) ([]tools.Definition, []string) {
	definitions := invocation.Request.Tools
	if p := invocation.SamplingPlan; p != nil {
		definitions = samplingTools(definitions, p.LocalToolKeys, p.OmitLocalTools, localSamplingKey)
	}
	byKey := make(map[string]string, len(invocation.Request.Tools))
	for i, d := range invocation.Request.Tools {
		if i < len(invocation.CatalogFingerprints) {
			byKey[d.Key] = invocation.CatalogFingerprints[i]
		}
	}
	result := make([]tools.Definition, len(definitions))
	fingerprints := make([]string, len(definitions))
	for i, d := range definitions {
		result[i] = tools.CloneDefinition(d)
		fingerprints[i] = byKey[d.Key]
	}
	return result, fingerprints
}

type agentClock struct{}

func (agentClock) Now() time.Time { return time.Now() }
