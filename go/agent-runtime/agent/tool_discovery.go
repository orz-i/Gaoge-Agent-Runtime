package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"strings"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

const (
	// ToolDiscoveryKey identifies the SDK-owned, read-only search control.
	// This is never an ordinary catalog/Executor capability.
	ToolDiscoveryKey       = "agent.search_tools"
	discoveryToolName      = "agent_search_tools"
	discoveryMinCandidates = 16
	discoveryMaxResults    = 5
	// At most 128 configured local/MCP tools plus SDK/Host first-party
	// controls. Total candidate size remains explicitly bounded.
	discoveryMaxCandidates = 256
)

var (
	ErrToolDiscoveryInvalid = errors.New("invalid tool discovery state")
	ErrToolDiscoveryDenied  = errors.New("tool discovery returned an unauthorized or stale tool")
)

// ToolDiscoveryCandidate identifies one frozen local Tool without disclosing
// its description, schema, args or credentials into the candidate snapshot.
type ToolDiscoveryCandidate struct {
	Key         string `json:"key"`
	Fingerprint string `json:"fingerprint"`
}

// ToolDiscoveryDescriptor is ephemeral search material freshly resolved from
// the frozen SDK Catalog. Descriptions never enter the durable discovery state.
type ToolDiscoveryDescriptor struct {
	Key         string
	Name        string
	Description string
}

// ToolDiscoveryRequest carries ephemeral model query and catalog metadata plus
// the frozen authorized candidate identities. Search cannot grant Tools.
type ToolDiscoveryRequest struct {
	RunID       string
	Model       string
	Query       string
	Candidates  []ToolDiscoveryCandidate
	Definitions []ToolDiscoveryDescriptor
	MaxResults  int
}

// ToolDiscovery is a read-only host-composed search port. Returned keys are
// untrusted and rechecked by Agent against the durable frozen grant.
type ToolDiscovery interface {
	Search(context.Context, ToolDiscoveryRequest) ([]string, error)
}

type discoveryReceipt struct {
	CallID       string   `json:"callID"`
	SnapshotHash string   `json:"snapshotHash"`
	LoadedKeys   []string `json:"loadedKeys"`
}

type discoveryState struct {
	RunID           string                   `json:"runID"`
	Model           string                   `json:"model"`
	SnapshotHash    string                   `json:"snapshotHash"`
	Candidates      []ToolDiscoveryCandidate `json:"candidates"`
	InitialToolKeys []string                 `json:"initialToolKeys,omitempty"`
	LoadedKeys      []string                 `json:"loadedKeys,omitempty"`
	Receipts        []discoveryReceipt       `json:"receipts,omitempty"`
}

var discoveryToolDefinition = tools.Definition{
	Key:         ToolDiscoveryKey,
	Name:        discoveryToolName,
	Description: "Find tools already authorized for this Agent task. Search loads only matching local tool definitions and does not grant additional permissions.",
	InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"required":["query"],"properties":{"query":{"type":"string","minLength":1,"maxLength":512}}}`),
}

func definitionFingerprint(value tools.Definition) (string, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return "", errors.Join(ErrToolDiscoveryInvalid, err)
	}
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:]), nil
}

func candidateSnapshotHash(runID, modelName string, candidates []ToolDiscoveryCandidate) (string, error) {
	data, err := json.Marshal(struct {
		RunID      string                   `json:"runID"`
		Model      string                   `json:"model"`
		Candidates []ToolDiscoveryCandidate `json:"candidates"`
	}{RunID: runID, Model: modelName, Candidates: candidates})
	if err != nil {
		return "", errors.Join(ErrToolDiscoveryInvalid, err)
	}
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:]), nil
}

func (runner *Runner) freezeToolDiscovery(request StartRequest, local []tools.Definition) (*discoveryState, error) {
	if runner.discovery == nil || len(local) < discoveryMinCandidates {
		return nil, nil
	}
	if len(local) > discoveryMaxCandidates || runner.catalog == nil || runner.executor == nil ||
		strings.TrimSpace(request.ID) == "" {
		return nil, ErrToolDiscoveryInvalid
	}
	candidates := make([]ToolDiscoveryCandidate, 0, len(local))
	seen := make(map[string]struct{}, len(local))
	for _, definition := range local {
		if definition.Key == ToolDiscoveryKey || definition.Name == discoveryToolName {
			return nil, ErrToolDiscoveryInvalid
		}
		if _, repeated := seen[definition.Key]; repeated {
			return nil, ErrToolDiscoveryInvalid
		}
		seen[definition.Key] = struct{}{}
		fingerprint, err := definitionFingerprint(definition)
		if err != nil {
			return nil, err
		}
		candidates = append(candidates, ToolDiscoveryCandidate{Key: definition.Key, Fingerprint: fingerprint})
	}
	slices.SortFunc(candidates, func(a, b ToolDiscoveryCandidate) int { return strings.Compare(a.Key, b.Key) })
	hash, err := candidateSnapshotHash(request.ID, strings.TrimSpace(request.Model), candidates)
	if err != nil {
		return nil, err
	}
	initial := normalizedToolKeys(request.InitialToolKeys)
	for _, key := range initial {
		if _, exists := seen[key]; !exists {
			return nil, ErrToolDiscoveryInvalid
		}
	}
	loaded := append([]string(nil), initial...)
	for _, key := range request.RequiredToolKeys {
		if _, exists := seen[key]; exists {
			loaded = append(loaded, key)
		}
	}
	loaded = normalizedToolKeys(loaded)
	return &discoveryState{
		RunID: request.ID, Model: strings.TrimSpace(request.Model),
		SnapshotHash: hash, Candidates: candidates,
		InitialToolKeys: initial, LoadedKeys: loaded,
	}, nil
}

func validDiscoveryState(state runState) bool {
	d := state.Discovery
	if d == nil {
		return true
	}
	if d.RunID == "" || d.Model != state.Model || len(d.Candidates) < discoveryMinCandidates ||
		len(d.Candidates) > discoveryMaxCandidates || len(d.SnapshotHash) != sha256.Size*2 {
		return false
	}
	allowed := make(map[string]struct{}, len(d.Candidates))
	for index, item := range d.Candidates {
		if item.Key == "" || item.Key == ToolDiscoveryKey || len(item.Fingerprint) != sha256.Size*2 ||
			!slices.Contains(state.ToolKeys, item.Key) ||
			index > 0 && d.Candidates[index-1].Key >= item.Key {
			return false
		}
		allowed[item.Key] = struct{}{}
	}
	hash, err := candidateSnapshotHash(d.RunID, d.Model, d.Candidates)
	if err != nil || hash != d.SnapshotHash {
		return false
	}
	if !validLoadedDiscoveryKeys(d.InitialToolKeys, allowed) ||
		!validLoadedDiscoveryKeys(d.LoadedKeys, allowed) {
		return false
	}
	for _, initial := range d.InitialToolKeys {
		if !slices.Contains(d.LoadedKeys, initial) {
			return false
		}
	}
	for _, required := range state.RequiredToolKeys {
		if _, exists := allowed[required]; exists && !slices.Contains(d.LoadedKeys, required) {
			return false
		}
	}
	callIDs := make(map[string]struct{}, len(d.Receipts))
	for _, receipt := range d.Receipts {
		if receipt.CallID == "" || receipt.SnapshotHash != d.SnapshotHash ||
			!validLoadedDiscoveryKeys(receipt.LoadedKeys, allowed) {
			return false
		}
		for _, key := range receipt.LoadedKeys {
			if !slices.Contains(d.LoadedKeys, key) {
				return false
			}
		}
		if _, found := callIDs[receipt.CallID]; found {
			return false
		}
		callIDs[receipt.CallID] = struct{}{}
	}
	return true
}

func validLoadedDiscoveryKeys(keys []string, allowed map[string]struct{}) bool {
	if !slices.IsSorted(keys) {
		return false
	}
	for index, key := range keys {
		if _, found := allowed[key]; !found || index > 0 && keys[index-1] == key {
			return false
		}
	}
	return true
}

func validateDiscoveryCatalog(d *discoveryState, definitions []tools.Definition) error {
	if d == nil {
		return nil
	}
	if len(definitions) != len(d.Candidates) {
		return ErrToolDiscoveryDenied
	}
	byKey := make(map[string]tools.Definition, len(definitions))
	for _, value := range definitions {
		if _, duplicate := byKey[value.Key]; duplicate {
			return ErrToolDiscoveryDenied
		}
		byKey[value.Key] = value
	}
	for _, item := range d.Candidates {
		current, ok := byKey[item.Key]
		if !ok {
			return ErrToolDiscoveryDenied
		}
		hash, err := definitionFingerprint(current)
		if err != nil || hash != item.Fingerprint {
			return ErrToolDiscoveryDenied
		}
	}
	return nil
}

// validateLoadedDiscoveryExecution closes the gap between a model response
// (which may have queued an approved Tool call) and the actual Executor call.
// An authorized candidate is not executable until it is loaded, and even a
// loaded candidate must still match its frozen definition fingerprint.
func validateLoadedDiscoveryExecution(d *discoveryState, key string, current tools.Definition) error {
	if d == nil {
		return nil
	}
	if !slices.Contains(d.LoadedKeys, key) || current.Key != key {
		return ErrToolDiscoveryDenied
	}
	for _, candidate := range d.Candidates {
		if candidate.Key != key {
			continue
		}
		fingerprint, err := definitionFingerprint(current)
		if err != nil || fingerprint != candidate.Fingerprint {
			return ErrToolDiscoveryDenied
		}
		return nil
	}
	return ErrToolDiscoveryDenied
}

func filterLoadedDiscoveryTools(definitions []tools.Definition, d *discoveryState) []tools.Definition {
	if d == nil {
		return definitions
	}
	loaded := make([]tools.Definition, 0, len(d.LoadedKeys)+1)
	for _, definition := range definitions {
		if slices.Contains(d.LoadedKeys, definition.Key) {
			loaded = append(loaded, definition)
		}
	}
	return append(loaded, tools.CloneDefinition(discoveryToolDefinition))
}

func (runner *Runner) searchPendingTool(
	ctx context.Context, snapshot kernel.Snapshot, execution pendingToolExecution,
) (kernel.Snapshot, bool, error) {
	state := execution.state
	if runner.discovery == nil || state.Discovery == nil ||
		snapshot.Run.ID != state.Discovery.RunID {
		return runner.handlePendingToolExecutionError(ctx, snapshot, state, ErrToolDiscoveryInvalid)
	}
	if err := tools.ValidateCall(discoveryToolDefinition, execution.call); err != nil {
		return runner.handlePendingToolExecutionError(ctx, snapshot, state, err)
	}
	var args struct {
		Query string `json:"query"`
	}
	if json.Unmarshal(execution.call.Arguments, &args) != nil || strings.TrimSpace(args.Query) == "" {
		return runner.handlePendingToolExecutionError(ctx, snapshot, state, ErrToolDiscoveryInvalid)
	}
	// External search is read-only but still bounded by the Tool execution
	// policy (deadline, cancellation and retry). Its result isn't a grant.
	// Search metadata comes only from freshly revalidated canonical Tool
	// definitions. It is never persisted with the discovery receipt.
	definitions, _, err := runner.resolveSelectedTools(ctx, state.ToolKeys, state.Model, snapshot.Run.DeadlineAt)
	if err == nil {
		err = validateDiscoveryCatalog(state.Discovery, definitions)
	}
	if err != nil {
		return runner.handlePendingToolExecutionError(ctx, snapshot, state, ErrToolDiscoveryDenied)
	}
	descriptors := make([]ToolDiscoveryDescriptor, 0, len(definitions))
	for _, definition := range definitions {
		descriptors = append(descriptors, ToolDiscoveryDescriptor{
			Key: definition.Key, Name: definition.Name, Description: definition.Description,
		})
	}
	request := ToolDiscoveryRequest{
		RunID: snapshot.Run.ID, Model: state.Model, Query: strings.TrimSpace(args.Query),
		Candidates:  append([]ToolDiscoveryCandidate(nil), state.Discovery.Candidates...),
		Definitions: descriptors, MaxResults: discoveryMaxResults,
	}
	result, _, err := runner.executeToolWithPolicy(ctx, execution.call.ID, snapshot.Run.DeadlineAt, func(callCtx context.Context) (tools.ExecutionResult, error) {
		keys, err := runner.discovery.Search(callCtx, request)
		if err != nil {
			return tools.ExecutionResult{}, err
		}
		if len(keys) > discoveryMaxResults {
			return tools.ExecutionResult{}, ErrToolDiscoveryDenied
		}
		allowed := make(map[string]struct{}, len(request.Candidates))
		for _, candidate := range request.Candidates {
			allowed[candidate.Key] = struct{}{}
		}
		unique := make(map[string]struct{}, len(keys))
		for _, key := range keys {
			if _, found := allowed[key]; !found || key == ToolDiscoveryKey {
				return tools.ExecutionResult{}, ErrToolDiscoveryDenied
			}
			if _, duplicate := unique[key]; duplicate {
				return tools.ExecutionResult{}, ErrToolDiscoveryDenied
			}
			unique[key] = struct{}{}
		}
		slices.Sort(keys)
		encoded, err := json.Marshal(struct {
			ToolKeys []string `json:"toolKeys"`
		}{ToolKeys: keys})
		if err != nil {
			return tools.ExecutionResult{}, err
		}
		return tools.ExecutionResult{
			Content: encoded,
			Receipt: tools.Receipt{ExecutionID: execution.call.ID, Disposition: "committed"},
		}, nil
	})
	if err != nil {
		return runner.handlePendingToolExecutionError(ctx, snapshot, state, err)
	}
	var output struct {
		ToolKeys []string `json:"toolKeys"`
	}
	if json.Unmarshal(result.Content, &output) != nil {
		return runner.handlePendingToolExecutionError(ctx, snapshot, state, ErrToolDiscoveryInvalid)
	}
	// Persist the discovered loaded set, sanitized receipt and ordinary Tool
	// result in one Kernel CAS; retry can never widen the frozen candidates.
	seen := make(map[string]struct{}, len(state.Discovery.LoadedKeys)+len(output.ToolKeys))
	for _, key := range state.Discovery.LoadedKeys {
		seen[key] = struct{}{}
	}
	for _, key := range output.ToolKeys {
		seen[key] = struct{}{}
	}
	loaded := make([]string, 0, len(seen))
	for key := range seen {
		loaded = append(loaded, key)
	}
	slices.Sort(loaded)
	next := *state.Discovery
	next.LoadedKeys = loaded
	next.Receipts = append(append([]discoveryReceipt(nil), next.Receipts...), discoveryReceipt{
		CallID: execution.call.ID, SnapshotHash: next.SnapshotHash, LoadedKeys: output.ToolKeys,
	})
	state.Discovery = &next
	execution.state = state
	return runner.persistCompletedToolCall(ctx, snapshot, execution, result)
}
