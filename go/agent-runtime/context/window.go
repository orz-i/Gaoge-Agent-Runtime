// Package context owns provider-neutral Context Window lineage, budgeting and rollover contracts.
package context

import (
	stdcontext "context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/model"
)

const CapabilityWindow kernel.Capability = "context.window"

var (
	ErrInvalidInput       = errors.New("invalid context window input")
	ErrLineageConflict    = errors.New("context window lineage conflict")
	ErrModelWindowUnknown = errors.New("model context window is unknown")
	ErrBudgetExceeded     = errors.New("context window budget exceeded")
)

// CountSource records whether request token accounting used an exact tokenizer.
type CountSource string

const (
	CountExact     CountSource = "exact"
	CountEstimated CountSource = "estimated"
)

// ArtifactKind identifies durable payload removed from the active model window.
type ArtifactKind string

const (
	ArtifactCompaction ArtifactKind = "compaction"
	ArtifactToolResult ArtifactKind = "tool_result"
)

// Entry is one exact provider-neutral transcript message plus Context lineage metadata.
// SourceID is set only for host transcript facts; runtime Tool transcript entries leave it empty.
type Entry struct {
	ID       string        `json:"id"`
	TurnID   string        `json:"turnID,omitempty"`
	SourceID string        `json:"sourceID,omitempty"`
	Required bool          `json:"required,omitempty"`
	Message  model.Message `json:"message"`
}

// Window is the exact active model history for one checkpoint generation.
// Instructions are materialized as the first system message and remain stable until an explicit rollover.
type Window struct {
	Instructions string  `json:"instructions,omitempty"`
	Entries      []Entry `json:"entries"`
}

// Policy is server-owned Context Window policy. MaxInputTokens is an optional service ceiling;
// it is never a substitute for the routed model's real context window.
type Policy struct {
	MaxInputTokens        int64 `json:"maxInputTokens,omitempty"`
	SoftLimitPercent      int   `json:"softLimitPercent"`
	EstimateSafetyPercent int   `json:"estimateSafetyPercent"`
	MaxSerializedBytes    int64 `json:"maxSerializedBytes"`
	PreserveRecentTurns   int   `json:"preserveRecentTurns"`
	MaxCompactionTokens   int   `json:"maxCompactionTokens"`
	MaxToolResultBytes    int   `json:"maxToolResultBytes"`
}

// ModelWindow is the routed model's provider-neutral context capability.
type ModelWindow struct {
	ContextTokens        int64 `json:"contextTokens"`
	MaxContextTokens     int64 `json:"maxContextTokens,omitempty"`
	EffectivePercent     int   `json:"effectivePercent,omitempty"`
	ReservedOutputTokens int64 `json:"reservedOutputTokens,omitempty"`
}

// Assessment is the budget result for one actual model sampling request.
type Assessment struct {
	HardInputTokens       int64       `json:"hardInputTokens"`
	SoftInputTokens       int64       `json:"softInputTokens"`
	RawTokenEstimate      int64       `json:"rawTokenEstimate"`
	AdjustedTokenEstimate int64       `json:"adjustedTokenEstimate"`
	TokenCountSource      CountSource `json:"tokenCountSource"`
	SerializedBytes       int64       `json:"serializedBytes"`
}

// Artifact seals durable content removed from the inline active window.
type Artifact struct {
	ID            string          `json:"id"`
	Kind          ArtifactKind    `json:"kind"`
	ScopeID       string          `json:"scopeID"`
	Generation    int             `json:"generation"`
	SourceID      string          `json:"sourceID,omitempty"`
	Content       string          `json:"content,omitempty"`
	ContentJSON   json.RawMessage `json:"contentJSON,omitempty"`
	ContentHash   string          `json:"contentHash"`
	TokenEstimate int64           `json:"tokenEstimate,omitempty"`
}

// Trace records content-free Context Window evolution facts.
type Trace struct {
	Reason              string      `json:"reason"`
	SourceEntryCount    int         `json:"sourceEntryCount"`
	ActiveEntryCount    int         `json:"activeEntryCount"`
	AppendedEntryCount  int         `json:"appendedEntryCount,omitempty"`
	CompactedEntryCount int         `json:"compactedEntryCount,omitempty"`
	ArtifactCount       int         `json:"artifactCount,omitempty"`
	LastAssessment      *Assessment `json:"lastAssessment,omitempty"`
}

// Checkpoint is one immutable active Context Window revision. Generation changes only on an
// explicit reset/rollover; Revision advances while the stable prefix is append-only.
type Checkpoint struct {
	ID                     string   `json:"id"`
	ScopeID                string   `json:"scopeID"`
	Generation             int      `json:"generation"`
	Revision               int      `json:"revision"`
	ParentCheckpointID     string   `json:"parentCheckpointID,omitempty"`
	LineageHash            string   `json:"lineageHash"`
	CoveredThroughSourceID string   `json:"coveredThroughSourceID"`
	CoveredPathHash        string   `json:"coveredPathHash"`
	StaticFingerprint      string   `json:"staticFingerprint"`
	ModelWindowFingerprint string   `json:"modelWindowFingerprint,omitempty"`
	ContentHash            string   `json:"contentHash"`
	Window                 Window   `json:"window"`
	ArtifactIDs            []string `json:"artifactIDs,omitempty"`
	Trace                  Trace    `json:"trace"`
}

// OpenRequest advances a durable host transcript onto the active Context Window.
// SourcePath is the complete current branch ancestry identity path; it has no message-count cap.
type OpenRequest struct {
	ScopeID           string
	StaticFingerprint string
	SourcePath        []string
	Entries           []Entry
	Instructions      string
	Previous          *Checkpoint
}

// CaptureRequest seals one actual model request after verifying that it extends the current
// active window without mutating its stable prefix.
type CaptureRequest struct {
	Previous          Checkpoint
	StaticFingerprint string
	RunID             string
	Messages          []model.Message
	Assessment        *Assessment
}

// RolloverRequest creates a new generation after an explicit compaction/reset boundary.
type RolloverRequest struct {
	Previous               Checkpoint
	Window                 Window
	Artifacts              []Artifact
	Reason                 string
	ModelWindowFingerprint string
	Assessment             *Assessment
}

// TokenCounter counts one complete canonical model request.
type TokenCounter interface {
	Count(stdcontext.Context, []byte) (int64, error)
}

// Dependencies are optional Context Window capabilities.
type Dependencies struct {
	Counter TokenCounter
}

// Manager owns pure Context Window evolution and request budget accounting.
type Manager struct {
	counter TokenCounter
}

func NewManager(dependencies Dependencies) *Manager { return &Manager{counter: dependencies.Counter} }

func (manager *Manager) Descriptor() kernel.FeatureDescriptor {
	return kernel.FeatureDescriptor{Name: "context", Provides: []kernel.Capability{CapabilityWindow}}
}

// NormalizePolicy applies non-model defaults only. It intentionally never invents a model token limit.
func NormalizePolicy(value Policy) Policy {
	if value.SoftLimitPercent <= 0 || value.SoftLimitPercent >= 100 {
		value.SoftLimitPercent = 80
	}
	if value.EstimateSafetyPercent <= 0 {
		value.EstimateSafetyPercent = 15
	}
	if value.MaxSerializedBytes <= 0 {
		value.MaxSerializedBytes = 4 << 20
	}
	if value.PreserveRecentTurns <= 0 {
		value.PreserveRecentTurns = 8
	}
	if value.MaxCompactionTokens <= 0 {
		value.MaxCompactionTokens = 1024
	}
	if value.MaxToolResultBytes <= 0 {
		value.MaxToolResultBytes = 2048
	}
	return value
}

// Open reuses the previous checkpoint only when its covered source path and static prefix are
// still ancestors of the current branch. Otherwise it performs an explicit generation reset.
func (manager *Manager) Open(_ stdcontext.Context, request OpenRequest) (Checkpoint, error) {
	normalized, err := normalizeOpenRequest(request)
	if err != nil {
		return Checkpoint{}, err
	}
	if normalized.Previous == nil {
		return newCheckpoint(normalized, 1, 1, "", normalized.Entries, "open", 0), nil
	}
	previous := CloneCheckpoint(*normalized.Previous)
	if !ValidCheckpoint(previous) {
		return Checkpoint{}, ErrInvalidInput
	}
	delta, reusable := sourceDelta(previous, normalized)
	if !reusable {
		return newCheckpoint(normalized, previous.Generation+1, 1, "", normalized.Entries, "lineage_reset", 0), nil
	}
	if len(delta) == 0 && normalized.Instructions == previous.Window.Instructions &&
		normalized.StaticFingerprint == previous.StaticFingerprint &&
		LineageHash(normalized.SourcePath...) == previous.LineageHash {
		return previous, nil
	}
	entries := append(CloneEntries(previous.Window.Entries), CloneEntries(delta)...)
	return newCheckpoint(
		normalized, previous.Generation, previous.Revision+1, previous.ID,
		entries, "append_source_delta", len(delta),
	), nil
}

// Capture verifies the Codex-style stable-prefix invariant for one real model sampling request.
// It advances only the runtime tail; a prefix mutation must be handled by an explicit Rollover.
func (manager *Manager) Capture(_ stdcontext.Context, request CaptureRequest) (Checkpoint, error) {
	previous := CloneCheckpoint(request.Previous)
	if !ValidCheckpoint(previous) || strings.TrimSpace(request.RunID) == "" ||
		strings.TrimSpace(request.StaticFingerprint) == "" || request.StaticFingerprint != previous.StaticFingerprint {
		return Checkpoint{}, ErrInvalidInput
	}
	current := Materialize(previous.Window)
	incoming := model.CloneMessages(request.Messages)
	if !messagesPrefix(current, incoming) {
		return Checkpoint{}, ErrLineageConflict
	}
	if len(incoming) == len(current) {
		result := previous
		result.Trace.LastAssessment = cloneAssessment(request.Assessment)
		return result, nil
	}
	entries := CloneEntries(previous.Window.Entries)
	for index, message := range incoming[len(current):] {
		entries = append(entries, runtimeEntry(request.RunID, previous, len(entries)+index, message))
	}
	next := previous
	next.Revision++
	next.ParentCheckpointID = previous.ID
	next.Window.Entries = entries
	next.ContentHash = windowHash(next.Window)
	next.Trace = Trace{
		Reason: "append_runtime_tail", SourceEntryCount: previous.Trace.SourceEntryCount,
		ActiveEntryCount: len(entries), AppendedEntryCount: len(incoming) - len(current),
		ArtifactCount: len(next.ArtifactIDs), LastAssessment: cloneAssessment(request.Assessment),
	}
	next.ID = checkpointID(next)
	return next, nil
}

// Rollover creates the only legal stable-prefix rewrite boundary.
func (manager *Manager) Rollover(_ stdcontext.Context, request RolloverRequest) (Checkpoint, error) {
	previous := CloneCheckpoint(request.Previous)
	window := normalizeWindow(request.Window)
	reason := strings.TrimSpace(request.Reason)
	if !ValidCheckpoint(previous) || reason == "" || len(window.Entries) == 0 {
		return Checkpoint{}, ErrInvalidInput
	}
	artifacts := CloneArtifacts(request.Artifacts)
	for index := range artifacts {
		if err := normalizeArtifact(&artifacts[index], previous.ScopeID, previous.Generation+1); err != nil {
			return Checkpoint{}, err
		}
	}
	artifactIDs := make([]string, 0, len(artifacts))
	for _, artifact := range artifacts {
		artifactIDs = append(artifactIDs, artifact.ID)
	}
	sort.Strings(artifactIDs)
	next := Checkpoint{
		ScopeID: previous.ScopeID, Generation: previous.Generation + 1, Revision: 1,
		ParentCheckpointID: previous.ID, LineageHash: previous.LineageHash,
		CoveredThroughSourceID: previous.CoveredThroughSourceID, CoveredPathHash: previous.CoveredPathHash,
		StaticFingerprint: previous.StaticFingerprint, ModelWindowFingerprint: strings.TrimSpace(request.ModelWindowFingerprint),
		Window: window, ArtifactIDs: artifactIDs,
		Trace: Trace{
			Reason: reason, SourceEntryCount: previous.Trace.SourceEntryCount,
			ActiveEntryCount: len(window.Entries), CompactedEntryCount: maxInt(len(previous.Window.Entries)-len(window.Entries), 0),
			ArtifactCount: len(artifactIDs), LastAssessment: cloneAssessment(request.Assessment),
		},
	}
	next.ContentHash = windowHash(next.Window)
	next.ID = checkpointID(next)
	return next, nil
}

// AssessModelRequest accounts the exact request at a model sampling boundary.
func (manager *Manager) AssessModelRequest(
	ctx stdcontext.Context,
	request model.Request,
	modelWindow ModelWindow,
	policy Policy,
) (Assessment, error) {
	policy = NormalizePolicy(policy)
	hard, err := EffectiveInputLimit(modelWindow, policy.MaxInputTokens)
	if err != nil {
		return Assessment{}, err
	}
	serialized, err := canonicalJSON(model.CloneRequest(request))
	if err != nil {
		return Assessment{}, err
	}
	soft := hard * int64(policy.SoftLimitPercent) / 100
	if soft < 1 {
		soft = 1
	}
	assessment := Assessment{HardInputTokens: hard, SoftInputTokens: soft, SerializedBytes: int64(len(serialized))}
	if manager != nil && manager.counter != nil {
		if count, countErr := manager.counter.Count(ctx, serialized); countErr == nil && count >= 0 {
			assessment.RawTokenEstimate = count
			assessment.AdjustedTokenEstimate = count
			assessment.TokenCountSource = CountExact
			return assessment, nil
		}
	}
	assessment.RawTokenEstimate = estimatedTokens(serialized)
	assessment.AdjustedTokenEstimate = applySafetyMargin(assessment.RawTokenEstimate, policy.EstimateSafetyPercent)
	assessment.TokenCountSource = CountEstimated
	return assessment, nil
}

func EffectiveInputLimit(modelWindow ModelWindow, serviceCeiling int64) (int64, error) {
	contextTokens := modelWindow.ContextTokens
	if modelWindow.MaxContextTokens > 0 && (contextTokens <= 0 || modelWindow.MaxContextTokens < contextTokens) {
		contextTokens = modelWindow.MaxContextTokens
	}
	if contextTokens <= 0 {
		return 0, ErrModelWindowUnknown
	}
	percent := modelWindow.EffectivePercent
	if percent <= 0 || percent > 100 {
		percent = 100
	}
	limit := contextTokens * int64(percent) / 100
	if modelWindow.ReservedOutputTokens > 0 {
		limit -= modelWindow.ReservedOutputTokens
	}
	if serviceCeiling > 0 && serviceCeiling < limit {
		limit = serviceCeiling
	}
	if limit <= 0 {
		return 0, ErrModelWindowUnknown
	}
	return limit, nil
}

func WithinHardBudget(assessment Assessment, policy Policy) bool {
	policy = NormalizePolicy(policy)
	return assessment.HardInputTokens > 0 && assessment.AdjustedTokenEstimate <= assessment.HardInputTokens &&
		assessment.SerializedBytes <= policy.MaxSerializedBytes
}

func Materialize(window Window) []model.Message {
	window = normalizeWindow(window)
	result := make([]model.Message, 0, len(window.Entries)+1)
	if window.Instructions != "" {
		result = append(result, model.Message{Role: model.RoleSystem, Content: window.Instructions})
	}
	for _, entry := range window.Entries {
		result = append(result, cloneMessage(entry.Message))
	}
	return result
}

func LineageHash(parts ...string) string {
	normalized := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			normalized = append(normalized, part)
		}
	}
	return hashBytes([]byte(strings.Join(normalized, "\x00")))
}

func StaticFingerprint(values ...string) string {
	normalized := make([]string, len(values))
	for index, value := range values {
		normalized[index] = strings.TrimSpace(value)
	}
	return hashBytes([]byte(strings.Join(normalized, "\x1f")))
}

func ModelWindowFingerprint(value ModelWindow) string {
	encoded, _ := canonicalJSON(value)
	return hashBytes(encoded)
}

func NewArtifact(kind ArtifactKind, scopeID string, generation int, sourceID string, content string, contentJSON json.RawMessage) (Artifact, error) {
	artifact := Artifact{
		Kind: kind, ScopeID: strings.TrimSpace(scopeID), Generation: generation,
		SourceID: strings.TrimSpace(sourceID), Content: content, ContentJSON: cloneJSON(contentJSON),
	}
	if err := normalizeArtifact(&artifact, artifact.ScopeID, generation); err != nil {
		return Artifact{}, err
	}
	return artifact, nil
}

func ValidArtifact(value Artifact) bool {
	clone := CloneArtifacts([]Artifact{value})[0]
	expectedID := strings.TrimSpace(clone.ID)
	expectedHash := strings.TrimSpace(clone.ContentHash)
	expectedTokens := clone.TokenEstimate
	if normalizeArtifact(&clone, clone.ScopeID, clone.Generation) != nil {
		return false
	}
	return expectedID != "" && expectedHash != "" && clone.ID == expectedID &&
		clone.ContentHash == expectedHash && clone.TokenEstimate == expectedTokens
}

func ValidCheckpoint(value Checkpoint) bool {
	if strings.TrimSpace(value.ID) == "" || strings.TrimSpace(value.ScopeID) == "" || value.Generation <= 0 || value.Revision <= 0 ||
		strings.TrimSpace(value.LineageHash) == "" || strings.TrimSpace(value.CoveredThroughSourceID) == "" ||
		strings.TrimSpace(value.CoveredPathHash) == "" || strings.TrimSpace(value.StaticFingerprint) == "" ||
		strings.TrimSpace(value.ContentHash) == "" || len(value.Window.Entries) == 0 {
		return false
	}
	return value.ContentHash == windowHash(value.Window) && value.ID == checkpointID(value)
}

func CloneCheckpoint(value Checkpoint) Checkpoint {
	value.Window = CloneWindow(value.Window)
	value.ArtifactIDs = append([]string(nil), value.ArtifactIDs...)
	value.Trace.LastAssessment = cloneAssessment(value.Trace.LastAssessment)
	return value
}

func CloneWindow(value Window) Window {
	value.Entries = CloneEntries(value.Entries)
	return value
}

func CloneEntries(values []Entry) []Entry {
	result := make([]Entry, len(values))
	for index, entry := range values {
		result[index] = entry
		result[index].Message = cloneMessage(entry.Message)
	}
	return result
}

func CloneArtifacts(values []Artifact) []Artifact {
	result := append([]Artifact(nil), values...)
	for index := range result {
		result[index].ContentJSON = cloneJSON(result[index].ContentJSON)
	}
	return result
}

func normalizeOpenRequest(request OpenRequest) (OpenRequest, error) {
	request.ScopeID = strings.TrimSpace(request.ScopeID)
	request.StaticFingerprint = strings.TrimSpace(request.StaticFingerprint)
	request.Instructions = strings.TrimSpace(request.Instructions)
	request.SourcePath = normalizeStrings(request.SourcePath)
	request.Entries = CloneEntries(request.Entries)
	for index := range request.Entries {
		request.Entries[index] = normalizeEntry(request.Entries[index])
	}
	if request.Previous != nil {
		clone := CloneCheckpoint(*request.Previous)
		request.Previous = &clone
	}
	if request.ScopeID == "" || request.StaticFingerprint == "" || len(request.SourcePath) == 0 || len(request.Entries) == 0 {
		return OpenRequest{}, ErrInvalidInput
	}
	if !validSourceEntries(request.SourcePath, request.Entries) {
		return OpenRequest{}, ErrInvalidInput
	}
	return request, nil
}

func validSourceEntries(sourcePath []string, entries []Entry) bool {
	if len(sourcePath) != len(entries) {
		return false
	}
	seen := make(map[string]struct{}, len(sourcePath))
	for index, sourceID := range sourcePath {
		entry := entries[index]
		if sourceID == "" || entry.SourceID != sourceID || entry.ID == "" || !validMessage(entry.Message) {
			return false
		}
		if _, duplicate := seen[sourceID]; duplicate {
			return false
		}
		seen[sourceID] = struct{}{}
	}
	return validToolTranscriptMessages(sourceMessages(entries))
}

func sourceDelta(previous Checkpoint, request OpenRequest) ([]Entry, bool) {
	if previous.ScopeID != request.ScopeID || previous.StaticFingerprint != request.StaticFingerprint {
		return nil, false
	}
	coveredIndex := -1
	for index, sourceID := range request.SourcePath {
		if sourceID == previous.CoveredThroughSourceID {
			coveredIndex = index
			break
		}
	}
	if coveredIndex < 0 || LineageHash(request.SourcePath[:coveredIndex+1]...) != previous.CoveredPathHash {
		return nil, false
	}
	return CloneEntries(request.Entries[coveredIndex+1:]), true
}

func newCheckpoint(request OpenRequest, generation int, revision int, parentID string, entries []Entry, reason string, appended int) Checkpoint {
	covered := request.SourcePath[len(request.SourcePath)-1]
	window := normalizeWindow(Window{Instructions: request.Instructions, Entries: entries})
	checkpoint := Checkpoint{
		ScopeID: request.ScopeID, Generation: generation, Revision: revision, ParentCheckpointID: strings.TrimSpace(parentID),
		LineageHash: LineageHash(request.SourcePath...), CoveredThroughSourceID: covered,
		CoveredPathHash: LineageHash(request.SourcePath...), StaticFingerprint: request.StaticFingerprint,
		Window: window,
		Trace:  Trace{Reason: reason, SourceEntryCount: len(request.SourcePath), ActiveEntryCount: len(window.Entries), AppendedEntryCount: appended},
	}
	checkpoint.ContentHash = windowHash(window)
	checkpoint.ID = checkpointID(checkpoint)
	return checkpoint
}

func checkpointID(value Checkpoint) string {
	return stableID(
		"ctxc", value.ScopeID, strconv.Itoa(value.Generation), strconv.Itoa(value.Revision),
		value.ParentCheckpointID, value.LineageHash, value.StaticFingerprint,
		value.ModelWindowFingerprint, value.ContentHash, strings.Join(value.ArtifactIDs, ","),
	)
}

func windowHash(window Window) string {
	encoded, err := canonicalJSON(normalizeWindow(window))
	if err != nil {
		return ""
	}
	return hashBytes(encoded)
}

func runtimeEntry(runID string, previous Checkpoint, index int, message model.Message) Entry {
	encoded, _ := canonicalJSON(message)
	id := stableID("ctxe", previous.ScopeID, strconv.Itoa(previous.Generation), runID, strconv.Itoa(index), hashBytes(encoded))
	return Entry{ID: id, TurnID: strings.TrimSpace(runID), Message: cloneMessage(message)}
}

func normalizeEntry(value Entry) Entry {
	value.ID = strings.TrimSpace(value.ID)
	value.TurnID = strings.TrimSpace(value.TurnID)
	value.SourceID = strings.TrimSpace(value.SourceID)
	value.Message.Role = model.Role(strings.TrimSpace(string(value.Message.Role)))
	value.Message.Content = strings.TrimSpace(value.Message.Content)
	value.Message.ToolCallID = strings.TrimSpace(value.Message.ToolCallID)
	value.Message.ToolCalls = cloneMessage(value.Message).ToolCalls
	return value
}

func normalizeWindow(value Window) Window {
	value.Instructions = strings.TrimSpace(value.Instructions)
	value.Entries = CloneEntries(value.Entries)
	for index := range value.Entries {
		value.Entries[index] = normalizeEntry(value.Entries[index])
	}
	return value
}

func validMessage(message model.Message) bool {
	switch message.Role {
	case model.RoleSystem, model.RoleUser:
		return strings.TrimSpace(message.Content) != "" && len(message.ToolCalls) == 0 && message.ToolCallID == ""
	case model.RoleAssistant:
		return strings.TrimSpace(message.Content) != "" || len(message.ToolCalls) > 0
	case model.RoleTool:
		return strings.TrimSpace(message.ToolCallID) != "" && strings.TrimSpace(message.Content) != "" && len(message.ToolCalls) == 0
	default:
		return false
	}
}

func validToolTranscriptMessages(messages []model.Message) bool {
	pending := make(map[string]struct{})
	for _, message := range messages {
		if message.Role == model.RoleAssistant && len(message.ToolCalls) > 0 {
			for _, call := range message.ToolCalls {
				id := strings.TrimSpace(call.ID)
				if id == "" {
					return false
				}
				pending[id] = struct{}{}
			}
			continue
		}
		if message.Role != model.RoleTool {
			continue
		}
		if _, ok := pending[message.ToolCallID]; !ok {
			return false
		}
		delete(pending, message.ToolCallID)
	}
	return len(pending) == 0
}

func sourceMessages(entries []Entry) []model.Message {
	result := make([]model.Message, len(entries))
	for index, entry := range entries {
		result[index] = cloneMessage(entry.Message)
	}
	return result
}

func messagesPrefix(prefix, complete []model.Message) bool {
	if len(prefix) > len(complete) {
		return false
	}
	for index := range prefix {
		if !sameMessage(prefix[index], complete[index]) {
			return false
		}
	}
	return true
}

func sameMessage(left, right model.Message) bool {
	left = canonicalMessage(left)
	right = canonicalMessage(right)
	leftJSON, leftErr := canonicalJSON(left)
	rightJSON, rightErr := canonicalJSON(right)
	return leftErr == nil && rightErr == nil && string(leftJSON) == string(rightJSON)
}

func canonicalMessage(value model.Message) model.Message {
	value = cloneMessage(value)
	value.Role = model.Role(strings.TrimSpace(string(value.Role)))
	value.Content = strings.TrimSpace(value.Content)
	value.ToolCallID = strings.TrimSpace(value.ToolCallID)
	if len(value.ToolCalls) == 0 {
		value.ToolCalls = nil
	}
	return value
}

func normalizeArtifact(value *Artifact, scopeID string, generation int) error {
	if value == nil {
		return ErrInvalidInput
	}
	value.ScopeID = strings.TrimSpace(value.ScopeID)
	value.SourceID = strings.TrimSpace(value.SourceID)
	if value.ScopeID == "" {
		value.ScopeID = strings.TrimSpace(scopeID)
	}
	if value.Generation <= 0 {
		value.Generation = generation
	}
	if value.ScopeID == "" || value.Generation <= 0 || (value.Kind != ArtifactCompaction && value.Kind != ArtifactToolResult) {
		return ErrInvalidInput
	}
	value.ContentJSON = normalizeRawJSON(value.ContentJSON)
	payload, err := canonicalJSON(struct {
		Content     string          `json:"content,omitempty"`
		ContentJSON json.RawMessage `json:"contentJSON,omitempty"`
	}{value.Content, value.ContentJSON})
	if err != nil {
		return err
	}
	value.ContentHash = hashBytes(payload)
	value.TokenEstimate = estimatedTokens(payload)
	value.ID = stableID("ctxa", value.ScopeID, strconv.Itoa(value.Generation), string(value.Kind), value.SourceID, value.ContentHash)
	return nil
}

func cloneMessage(value model.Message) model.Message {
	return model.CloneMessages([]model.Message{value})[0]
}

func cloneAssessment(value *Assessment) *Assessment {
	if value == nil {
		return nil
	}
	clone := *value
	return &clone
}

func canonicalJSON(value any) ([]byte, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, errors.Join(ErrInvalidInput, err)
	}
	var normalized any
	if err = json.Unmarshal(encoded, &normalized); err != nil {
		return nil, errors.Join(ErrInvalidInput, err)
	}
	encoded, err = json.Marshal(normalized)
	if err != nil {
		return nil, errors.Join(ErrInvalidInput, err)
	}
	return encoded, nil
}

func normalizeRawJSON(value json.RawMessage) json.RawMessage {
	if len(value) == 0 {
		return nil
	}
	var normalized any
	if err := json.Unmarshal(value, &normalized); err != nil {
		return cloneJSON(value)
	}
	encoded, err := json.Marshal(normalized)
	if err != nil {
		return cloneJSON(value)
	}
	return encoded
}

func cloneJSON(value json.RawMessage) json.RawMessage { return append(json.RawMessage(nil), value...) }

func normalizeStrings(values []string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			result = append(result, value)
		}
	}
	return result
}

func hashBytes(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}

func stableID(prefix string, values ...string) string {
	joined := strings.Join(values, "\x1f")
	return prefix + "_" + hashBytes([]byte(joined))[:32]
}

func estimatedTokens(serialized []byte) int64 {
	if len(serialized) == 0 {
		return 0
	}
	return int64((len(serialized) + 3) / 4)
}

func applySafetyMargin(tokens int64, percent int) int64 {
	if tokens <= 0 || percent <= 0 {
		return tokens
	}
	return (tokens*int64(100+percent) + 99) / 100
}

func maxInt(left, right int) int {
	if left > right {
		return left
	}
	return right
}

func DebugCheckpoint(value Checkpoint) string {
	return fmt.Sprintf("%s generation=%d revision=%d covered=%s", value.ID, value.Generation, value.Revision, value.CoveredThroughSourceID)
}
