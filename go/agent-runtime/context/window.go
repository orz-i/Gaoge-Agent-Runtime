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
	"unicode/utf8"

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

// CompactToolPressure creates a next-generation rollover candidate without removing user or
// assistant turns. Every Tool result whose durable marker is smaller than the inline payload is
// sealed into an Artifact and replaced by that marker. This provides a safe pressure-relief path
// for a first/early user turn where PreserveRecentTurns intentionally forbids prefix compaction.
func (manager *Manager) CompactToolPressure(
	previous Checkpoint,
	runID string,
	messages []model.Message,
) (ToolPressureCompaction, error) {
	previous = CloneCheckpoint(previous)
	runID = strings.TrimSpace(runID)
	messages = model.CloneMessages(messages)
	if !ValidCheckpoint(previous) || runID == "" || len(messages) == 0 {
		return ToolPressureCompaction{}, ErrInvalidInput
	}
	if !messagesPrefix(Materialize(previous.Window), messages) {
		return ToolPressureCompaction{}, ErrLineageConflict
	}

	artifacts := make([]Artifact, 0)
	for index := range messages {
		message := &messages[index]
		if message.Role != model.RoleTool || strings.TrimSpace(message.ToolCallID) == "" || message.Content == "" {
			continue
		}
		artifact, err := NewArtifact(
			ArtifactToolResult,
			previous.ScopeID,
			previous.Generation+1,
			strings.TrimSpace(message.ToolCallID),
			message.Content,
			nil,
		)
		if err != nil {
			return ToolPressureCompaction{}, err
		}
		marker := compactedToolResultMarker(artifact)
		if len([]byte(marker)) >= len([]byte(message.Content)) {
			continue
		}
		message.Content = marker
		artifacts = append(artifacts, artifact)
	}
	if len(artifacts) == 0 {
		return ToolPressureCompaction{}, ErrBudgetExceeded
	}

	instructions, body := splitWindowInstructions(previous.Window.Instructions, messages)
	if !validToolTranscriptMessages(body) {
		return ToolPressureCompaction{}, ErrLineageConflict
	}
	entries := make([]Entry, 0, len(body))
	for index, message := range body {
		if !validMessage(message) {
			return ToolPressureCompaction{}, ErrLineageConflict
		}
		encoded, _ := canonicalJSON(message)
		entries = append(entries, Entry{
			ID: stableID(
				"ctxe", previous.ScopeID, strconv.Itoa(previous.Generation+1), runID,
				"tool_pressure", strconv.Itoa(index), hashBytes(encoded),
			),
			TurnID: runID, Message: cloneMessage(message),
		})
	}
	return ToolPressureCompaction{
		Window:    Window{Instructions: instructions, Entries: entries},
		Artifacts: CloneArtifacts(artifacts), CompactedResults: len(artifacts),
	}, nil
}

// CompactToolResults compacts only Tool output messages that exceed the configured byte limit.
// The replacement is deterministic, so re-evaluating the same transcript is idempotent.
func (manager *Manager) CompactToolResults(
	scopeID string,
	generation int,
	messages []model.Message,
	maxBytes int,
) (ToolCompactionResult, error) {
	scopeID = strings.TrimSpace(scopeID)
	if scopeID == "" || generation <= 0 || maxBytes < 256 {
		return ToolCompactionResult{}, ErrInvalidInput
	}
	result := ToolCompactionResult{Messages: model.CloneMessages(messages)}
	for index := range result.Messages {
		message := &result.Messages[index]
		if message.Role != model.RoleTool || len([]byte(message.Content)) <= maxBytes {
			continue
		}
		artifact, err := NewArtifact(
			ArtifactToolResult,
			scopeID,
			generation,
			strings.TrimSpace(message.ToolCallID),
			message.Content,
			nil,
		)
		if err != nil {
			return ToolCompactionResult{}, err
		}
		message.Content = compactedToolResultReference(artifact, maxBytes)
		result.Artifacts = append(result.Artifacts, artifact)
	}
	return result, nil
}

func splitWindowInstructions(instructions string, messages []model.Message) (string, []model.Message) {
	instructions = strings.TrimSpace(instructions)
	if instructions != "" && len(messages) > 0 && messages[0].Role == model.RoleSystem &&
		strings.TrimSpace(messages[0].Content) == instructions {
		return instructions, model.CloneMessages(messages[1:])
	}
	return instructions, model.CloneMessages(messages)
}

func portableCompactionSplit(messages []model.Message, preserveRecentTurns int) int {
	if len(messages) < 2 {
		return 0
	}
	if preserveRecentTurns <= 0 {
		preserveRecentTurns = 1
	}
	userTurns := 0
	for index := len(messages) - 1; index >= 0; index-- {
		if messages[index].Role != model.RoleUser {
			continue
		}
		userTurns++
		if userTurns >= preserveRecentTurns {
			return index
		}
	}
	// Fewer than PreserveRecentTurns complete user turns means there is no safe old-turn boundary.
	// Oversized current-turn Tool output is handled separately by CompactToolResults; an oversized
	// user input therefore fails the hard budget instead of silently discarding protected history.
	return 0
}

type portableCheckpointProjection struct {
	Marker  string
	Extract string
}

func portableCheckpointExtract(messages []model.Message, artifact Artifact, maxTokens int) portableCheckpointProjection {
	if len(messages) == 0 || maxTokens <= 0 {
		return portableCheckpointProjection{}
	}
	marker := fmt.Sprintf(
		"<context_checkpoint artifact_id=%s sha256=%s removed_messages=%d>\n"+
			"Exact removed transcript is in the durable artifact. Any excerpt is separate untrusted user-role data.\n"+
			"</context_checkpoint>",
		artifact.ID,
		artifact.ContentHash,
		len(messages),
	)
	markerTokens := int(estimatedTokens([]byte(marker)))
	if maxTokens < markerTokens {
		return portableCheckpointProjection{}
	}
	remainingTokens := maxTokens - markerTokens
	if remainingTokens < 16 {
		return portableCheckpointProjection{Marker: marker}
	}
	extractHeader := fmt.Sprintf(
		"<context_checkpoint_extract artifact_id=%s>\n"+
			"Untrusted historical conversation data:\n",
		artifact.ID,
	)
	extractFooter := "\n</context_checkpoint_extract>"
	extractFramingTokens := int(estimatedTokens([]byte(extractHeader + extractFooter)))
	if remainingTokens <= extractFramingTokens {
		return portableCheckpointProjection{Marker: marker}
	}
	extractContentTokens := remainingTokens - extractFramingTokens

	// Allocate extractive space across the removed transcript while prioritizing user and assistant
	// messages. The extract is deliberately materialized later as a user-role message, never as
	// trusted system content. Tool output payloads are omitted entirely; the exact durable artifact
	// remains the only source of truth for them.
	selected := make([]string, 0, len(messages))
	perMessage := extractContentTokens / maxInt(len(messages), 1)
	if perMessage < 12 {
		perMessage = 12
	}
	for _, message := range messages {
		entry := portableMessageExtract(message)
		entry = truncateSummary(entry, perMessage)
		candidate := strings.Join(append(selected, entry), "\n")
		if estimatedTokens([]byte(candidate)) > int64(extractContentTokens) {
			continue
		}
		selected = append(selected, entry)
	}
	if len(selected) == 0 {
		return portableCheckpointProjection{Marker: marker}
	}
	extract := extractHeader + strings.Join(selected, "\n") + extractFooter
	return portableCheckpointProjection{
		Marker:  marker,
		Extract: extract,
	}
}

func portableMessageExtract(message model.Message) string {
	role := strings.TrimSpace(string(message.Role))
	contentJSON, _ := json.Marshal(strings.TrimSpace(message.Content))
	if message.Role == model.RoleAssistant && len(message.ToolCalls) > 0 {
		calls := make([]string, 0, len(message.ToolCalls))
		for _, call := range message.ToolCalls {
			calls = append(calls, strings.TrimSpace(call.ToolKey)+"#"+strings.TrimSpace(call.ID))
		}
		return role + " tool_calls=" + strings.Join(calls, ",") + " content_json=" + string(contentJSON)
	}
	if message.Role == model.RoleTool {
		return role + " call_id=" + strings.TrimSpace(message.ToolCallID) + " output_omitted=true"
	}
	return role + " content_json=" + string(contentJSON)
}

func truncateSummary(value string, maxTokens int) string {
	value = strings.TrimSpace(value)
	if maxTokens <= 0 || estimatedTokens([]byte(value)) <= int64(maxTokens) {
		return value
	}
	maxBytes := maxTokens * 4
	if maxBytes <= 1 {
		return "…"
	}
	return validUTF8Prefix(value, maxBytes-1) + "…"
}

func compactedToolResultReference(artifact Artifact, maxBytes int) string {
	header := compactedToolResultMarker(artifact)
	const headOpen = "<head>\n"
	const headCloseTailOpen = "\n</head>\n<tail>\n"
	const tailClose = "\n</tail>"
	overhead := len(header) + len(headOpen) + len(headCloseTailOpen) + len(tailClose)
	if overhead > maxBytes {
		return validUTF8Prefix(header, maxBytes)
	}
	remaining := maxBytes - overhead
	headBytes := remaining / 2
	tailBytes := remaining - headBytes
	head := validUTF8Prefix(artifact.Content, headBytes)
	tail := validUTF8Suffix(artifact.Content, tailBytes)
	return header + headOpen + head + headCloseTailOpen + tail + tailClose
}

func compactedToolResultMarker(artifact Artifact) string {
	return fmt.Sprintf(
		"[tool_result_compacted artifact_id=%s sha256=%s bytes=%d]\n",
		artifact.ID,
		artifact.ContentHash,
		len([]byte(artifact.Content)),
	)
}

func validUTF8Prefix(value string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	if len(value) <= maxBytes {
		return value
	}
	end := maxBytes
	for end > 0 && !utf8.ValidString(value[:end]) {
		end--
	}
	return value[:end]
}

func validUTF8Suffix(value string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	if len(value) <= maxBytes {
		return value
	}
	start := len(value) - maxBytes
	for start < len(value) && !utf8.ValidString(value[start:]) {
		start++
	}
	return value[start:]
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
	ContextTokens        int64             `json:"contextTokens"`
	MaxContextTokens     int64             `json:"maxContextTokens,omitempty"`
	EffectivePercent     int               `json:"effectivePercent,omitempty"`
	ReservedOutputTokens int64             `json:"reservedOutputTokens,omitempty"`
	TokenCountContext    TokenCountContext `json:"-"`
}

// TokenCountContext identifies the provider/model tokenizer contract for one routed request.
// It is deliberately excluded from the model-window fingerprint: route identity is tracked by
// the host, while this metadata exists only to prevent an exact TokenCounter from guessing which
// tokenizer or provider-native count endpoint should be used.
type TokenCountContext struct {
	Protocol string
	Model    string
}

// Assessment is the budget result for one actual model sampling request.
type Assessment struct {
	HardInputTokens       int64       `json:"hardInputTokens"`
	SoftInputTokens       int64       `json:"softInputTokens"`
	RawTokenEstimate      int64       `json:"rawTokenEstimate"`
	AdjustedTokenEstimate int64       `json:"adjustedTokenEstimate"`
	HardTokenEstimate     int64       `json:"hardTokenEstimate"`
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
	CacheIdentity          string   `json:"cacheIdentity"`
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
	ScopeID            string
	StaticFingerprint  string
	SourcePath         []string
	Entries            []Entry
	Instructions       string
	SourceDelta        bool
	ResetCacheIdentity bool
	Previous           *Checkpoint
}

// CaptureRequest seals one actual model request after verifying that it extends the current
// active window without mutating its stable prefix.
type CaptureRequest struct {
	Previous          Checkpoint
	StaticFingerprint string
	RunID             string
	Messages          []model.Message
	Artifacts         []Artifact
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

// PortableCompactionRequest creates a provider-neutral rollover candidate from the exact
// model-visible transcript. It preserves complete recent user turns and seals the removed
// transcript as a durable Artifact before replacing it with a bounded extractive checkpoint.
type PortableCompactionRequest struct {
	Previous    Checkpoint
	RunID       string
	Messages    []model.Message
	Policy      Policy
	OmitExtract bool
}

// PortableCompaction is one compacted active window plus the exact removed transcript Artifact.
type PortableCompaction struct {
	Window          Window
	Artifact        Artifact
	RemovedMessages int
}

// ToolCompactionResult is the exact transcript after oversized Tool outputs were sealed as
// durable Artifacts and replaced inline with bounded head/tail references.
type ToolCompactionResult struct {
	Messages  []model.Message
	Artifacts []Artifact
}

// ToolPressureCompaction is an explicit rollover candidate used when the active transcript is
// under window pressure but there is no safe old user-turn prefix to remove yet. It preserves the
// complete message sequence and only replaces completed Tool result payloads with durable markers.
type ToolPressureCompaction struct {
	Window           Window
	Artifacts        []Artifact
	CompactedResults int
}

// TokenCountRequest carries both the canonical provider-visible request bytes and the routed
// protocol/model identity. Implementations may report an exact count only when they understand
// that concrete provider contract; otherwise they must return an error so the conservative hard
// upper bound remains authoritative.
type TokenCountRequest struct {
	Context TokenCountContext
	Payload []byte
}

// TokenCounter counts one complete routed model request with provider/model awareness.
type TokenCounter interface {
	Count(stdcontext.Context, TokenCountRequest) (int64, error)
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
		if normalized.SourceDelta {
			return Checkpoint{}, ErrInvalidInput
		}
		return newCheckpoint(normalized, 1, 1, "", normalized.Entries, "open", 0), nil
	}
	previous := CloneCheckpoint(*normalized.Previous)
	if !ValidCheckpoint(previous) {
		return Checkpoint{}, ErrInvalidInput
	}
	if normalized.SourceDelta {
		return openSourceDelta(previous, normalized)
	}
	delta, reusable := sourceDelta(previous, normalized)
	if normalized.ResetCacheIdentity || !reusable {
		next := newCheckpoint(
			normalized, previous.Generation+1, 1, previous.ID,
			normalized.Entries, "lineage_reset", 0,
		)
		next.CacheIdentity = resetCacheIdentity(
			previous, next.StaticFingerprint, next.LineageHash, next.Generation,
		)
		next.ID = checkpointID(next)
		return next, nil
	}
	if len(delta) == 0 && normalized.Instructions == previous.Window.Instructions &&
		normalized.StaticFingerprint == previous.StaticFingerprint &&
		LineageHash(normalized.SourcePath...) == previous.LineageHash {
		return previous, nil
	}
	entries := append(CloneEntries(previous.Window.Entries), CloneEntries(delta)...)
	next := newCheckpoint(
		normalized, previous.Generation, previous.Revision+1, previous.ID,
		entries, "append_source_delta", len(delta),
	)
	next.CacheIdentity = previous.CacheIdentity
	next.ModelWindowFingerprint = previous.ModelWindowFingerprint
	next.ArtifactIDs = append([]string(nil), previous.ArtifactIDs...)
	next.Trace.ArtifactCount = len(next.ArtifactIDs)
	next.Trace.LastAssessment = cloneAssessment(previous.Trace.LastAssessment)
	next.ID = checkpointID(next)
	return next, nil
}

func openSourceDelta(previous Checkpoint, request OpenRequest) (Checkpoint, error) {
	if previous.ScopeID != request.ScopeID || previous.StaticFingerprint != request.StaticFingerprint ||
		previous.Window.Instructions != request.Instructions {
		return Checkpoint{}, ErrLineageConflict
	}
	if len(request.SourcePath) == 0 && !request.ResetCacheIdentity {
		return previous, nil
	}
	lineageHash := ExtendLineageHash(previous.CoveredPathHash, request.SourcePath...)
	entries := append(CloneEntries(previous.Window.Entries), CloneEntries(request.Entries)...)
	next := previous
	next.Revision++
	next.ParentCheckpointID = previous.ID
	next.LineageHash = lineageHash
	next.CoveredPathHash = lineageHash
	next.Window = normalizeWindow(Window{Instructions: request.Instructions, Entries: entries})
	next.ContentHash = windowHash(next.Window)
	next.Trace = Trace{
		Reason: "append_source_delta", SourceEntryCount: previous.Trace.SourceEntryCount + len(request.SourcePath),
		ActiveEntryCount: len(next.Window.Entries), AppendedEntryCount: len(request.SourcePath),
		ArtifactCount: len(previous.ArtifactIDs), LastAssessment: cloneAssessment(previous.Trace.LastAssessment),
	}
	if len(request.SourcePath) != 0 {
		next.CoveredThroughSourceID = request.SourcePath[len(request.SourcePath)-1]
	}
	if request.ResetCacheIdentity {
		next.Generation = previous.Generation + 1
		next.Revision = 1
		next.Trace.Reason = "lineage_reset"
		next.CacheIdentity = resetCacheIdentity(
			previous, request.StaticFingerprint, lineageHash, next.Generation,
		)
	}
	next.ID = checkpointID(next)
	return next, nil
}

func resetCacheIdentity(previous Checkpoint, staticFingerprint string, lineageHash string, generation int) string {
	return stableID(
		"ctxk", previous.ScopeID, strings.TrimSpace(staticFingerprint), strings.TrimSpace(lineageHash),
		previous.CacheIdentity, strconv.Itoa(generation),
	)
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
	artifactIDs, err := captureArtifactIDs(previous, request.Artifacts)
	if err != nil {
		return Checkpoint{}, err
	}
	if len(incoming) == len(current) {
		if slicesEqualStrings(artifactIDs, previous.ArtifactIDs) {
			return previous, nil
		}
		next := previous
		next.Revision++
		next.ParentCheckpointID = previous.ID
		next.ArtifactIDs = artifactIDs
		next.Trace = previous.Trace
		next.Trace.Reason = "append_runtime_artifacts"
		next.Trace.ArtifactCount = len(artifactIDs)
		next.Trace.LastAssessment = cloneAssessment(request.Assessment)
		next.ID = checkpointID(next)
		return next, nil
	}
	entries := CloneEntries(previous.Window.Entries)
	for index, message := range incoming[len(current):] {
		entries = append(entries, runtimeEntry(request.RunID, previous, len(entries)+index, message))
	}
	next := previous
	next.Revision++
	next.ParentCheckpointID = previous.ID
	next.Window.Entries = entries
	next.ArtifactIDs = artifactIDs
	next.ContentHash = windowHash(next.Window)
	next.Trace = Trace{
		Reason: "append_runtime_tail", SourceEntryCount: previous.Trace.SourceEntryCount,
		ActiveEntryCount: len(entries), AppendedEntryCount: len(incoming) - len(current),
		ArtifactCount: len(artifactIDs), LastAssessment: cloneAssessment(request.Assessment),
	}
	next.ID = checkpointID(next)
	return next, nil
}

func captureArtifactIDs(previous Checkpoint, artifacts []Artifact) ([]string, error) {
	ids := append([]string(nil), previous.ArtifactIDs...)
	for _, artifact := range CloneArtifacts(artifacts) {
		if err := normalizeArtifact(&artifact, previous.ScopeID, previous.Generation); err != nil {
			return nil, err
		}
		if artifact.ScopeID != previous.ScopeID || artifact.Generation != previous.Generation {
			return nil, ErrLineageConflict
		}
		ids = append(ids, artifact.ID)
	}
	return sortedUniqueStrings(ids), nil
}

func sortedUniqueStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	result := append([]string(nil), values...)
	sort.Strings(result)
	write := 0
	for _, value := range result {
		if write > 0 && result[write-1] == value {
			continue
		}
		result[write] = value
		write++
	}
	return result[:write]
}

func slicesEqualStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
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
	artifactIDs := append([]string(nil), previous.ArtifactIDs...)
	for _, artifact := range artifacts {
		artifactIDs = append(artifactIDs, artifact.ID)
	}
	artifactIDs = sortedUniqueStrings(artifactIDs)
	next := Checkpoint{
		ScopeID: previous.ScopeID, CacheIdentity: previous.CacheIdentity, Generation: previous.Generation + 1, Revision: 1,
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

// BindModelWindow records the routed model-window fingerprint without rewriting the active prompt.
// The first binding advances the checkpoint revision; a changed non-empty fingerprint creates an
// explicit new generation boundary because provider/model cache identity may no longer be valid.
func (manager *Manager) BindModelWindow(
	ctx stdcontext.Context,
	previous Checkpoint,
	fingerprint string,
) (Checkpoint, error) {
	previous = CloneCheckpoint(previous)
	fingerprint = strings.TrimSpace(fingerprint)
	if !ValidCheckpoint(previous) || fingerprint == "" {
		return Checkpoint{}, ErrInvalidInput
	}
	if previous.ModelWindowFingerprint == fingerprint {
		return previous, nil
	}
	if previous.ModelWindowFingerprint != "" {
		return manager.Rollover(ctx, RolloverRequest{
			Previous: previous, Window: previous.Window, Reason: "model_window_changed",
			ModelWindowFingerprint: fingerprint,
		})
	}
	next := previous
	next.Revision++
	next.ParentCheckpointID = previous.ID
	next.ModelWindowFingerprint = fingerprint
	next.Trace = previous.Trace
	next.Trace.Reason = "model_window_bound"
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
	return manager.AssessRequest(ctx, model.CloneRequest(request), modelWindow, policy)
}

// AssessRequest accounts the canonical provider-neutral request that will actually be sent at a
// sampling boundary. Hosts should call this after model option/tool/hosted-tool materialization.
func (manager *Manager) AssessRequest(
	ctx stdcontext.Context,
	request any,
	modelWindow ModelWindow,
	policy Policy,
) (Assessment, error) {
	policy = NormalizePolicy(policy)
	hard, err := EffectiveInputLimit(modelWindow, policy.MaxInputTokens)
	if err != nil {
		return Assessment{}, err
	}
	serialized, err := canonicalJSON(request)
	if err != nil {
		return Assessment{}, err
	}
	soft := hard * int64(policy.SoftLimitPercent) / 100
	if soft < 1 {
		soft = 1
	}
	assessment := Assessment{HardInputTokens: hard, SoftInputTokens: soft, SerializedBytes: int64(len(serialized))}
	countContext := normalizeTokenCountContext(modelWindow.TokenCountContext)
	if manager != nil && manager.counter != nil && countContext.Protocol != "" && countContext.Model != "" {
		countRequest := TokenCountRequest{
			Context: countContext,
			Payload: append([]byte(nil), serialized...),
		}
		if count, countErr := manager.counter.Count(ctx, countRequest); countErr == nil && count >= 0 {
			assessment.RawTokenEstimate = count
			assessment.AdjustedTokenEstimate = count
			assessment.HardTokenEstimate = count
			assessment.TokenCountSource = CountExact
			return assessment, nil
		}
	}
	assessment.RawTokenEstimate = estimatedTokens(serialized)
	assessment.AdjustedTokenEstimate = applySafetyMargin(assessment.RawTokenEstimate, policy.EstimateSafetyPercent)
	assessment.HardTokenEstimate = hardTokenUpperBound(serialized, policy.EstimateSafetyPercent)
	assessment.TokenCountSource = CountEstimated
	return assessment, nil
}

// CompactPortable creates one explicit rollover candidate. Unlike the removed V1 deterministic
// summary path, this method never claims that a truncated extract semantically covers the removed
// transcript: the exact removed messages are sealed in the returned Artifact and the inline
// checkpoint identifies that Artifact and hash explicitly.
func (manager *Manager) CompactPortable(request PortableCompactionRequest) (PortableCompaction, error) {
	previous := CloneCheckpoint(request.Previous)
	policy := NormalizePolicy(request.Policy)
	messages := model.CloneMessages(request.Messages)
	runID := strings.TrimSpace(request.RunID)
	if !ValidCheckpoint(previous) || runID == "" || len(messages) == 0 {
		return PortableCompaction{}, ErrInvalidInput
	}

	instructions, body := splitWindowInstructions(previous.Window.Instructions, messages)
	split := portableCompactionSplit(body, policy.PreserveRecentTurns)
	if split <= 0 || split >= len(body) {
		return PortableCompaction{}, ErrBudgetExceeded
	}
	removed := model.CloneMessages(body[:split])
	retained := model.CloneMessages(body[split:])
	removedJSON, err := canonicalJSON(removed)
	if err != nil {
		return PortableCompaction{}, err
	}
	artifact, err := NewArtifact(
		ArtifactCompaction,
		previous.ScopeID,
		previous.Generation+1,
		previous.CoveredThroughSourceID,
		"",
		json.RawMessage(removedJSON),
	)
	if err != nil {
		return PortableCompaction{}, err
	}
	projection := portableCheckpointExtract(removed, artifact, policy.MaxCompactionTokens)
	if strings.TrimSpace(projection.Marker) == "" {
		return PortableCompaction{}, ErrBudgetExceeded
	}
	if request.OmitExtract {
		projection.Extract = ""
	}
	entries := make([]Entry, 0, len(retained)+2)
	entries = append(entries, Entry{
		ID:     stableID("ctxe", previous.ScopeID, strconv.Itoa(previous.Generation+1), artifact.ID),
		TurnID: "context_rollover", Required: true,
		Message: model.Message{Role: model.RoleSystem, Content: projection.Marker},
	})
	if strings.TrimSpace(projection.Extract) != "" {
		entries = append(entries, Entry{
			ID: stableID(
				"ctxe", previous.ScopeID, strconv.Itoa(previous.Generation+1), artifact.ID, "extract",
			),
			TurnID: "context_rollover", Required: true,
			Message: model.Message{Role: model.RoleUser, Content: projection.Extract},
		})
	}
	for index, message := range retained {
		encoded, _ := canonicalJSON(message)
		entries = append(entries, Entry{
			ID: stableID(
				"ctxe", previous.ScopeID, strconv.Itoa(previous.Generation+1), runID,
				strconv.Itoa(index), hashBytes(encoded),
			),
			TurnID: runID, Message: cloneMessage(message),
		})
	}
	return PortableCompaction{
		Window:   Window{Instructions: instructions, Entries: entries},
		Artifact: artifact, RemovedMessages: len(removed),
	}, nil
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
	return assessment.HardInputTokens > 0 && assessment.HardTokenEstimate > 0 &&
		assessment.HardTokenEstimate <= assessment.HardInputTokens &&
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
	return ExtendLineageHash("", parts...)
}

// ExtendLineageHash advances one ancestry hash without requiring the already-covered source IDs.
// This makes checkpoint + source-delta loading equivalent to hashing the complete ancestry.
func ExtendLineageHash(previous string, parts ...string) string {
	current := strings.TrimSpace(previous)
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		current = hashBytes([]byte(current + "\x00" + part))
	}
	return current
}

// SourceAlignedCheckpoint reports whether the active Window ends exactly at its durable host
// source boundary. Such checkpoints are safe ancestry-reuse candidates across branch switches;
// checkpoints with runtime-only Tool/assistant tail entries remain valid active heads but are not
// used as historical branch anchors.
func SourceAlignedCheckpoint(value Checkpoint) bool {
	if !ValidCheckpoint(value) || len(value.Window.Entries) == 0 {
		return false
	}
	last := value.Window.Entries[len(value.Window.Entries)-1]
	return strings.TrimSpace(last.SourceID) == strings.TrimSpace(value.CoveredThroughSourceID)
}

// CheckpointSourceIndex returns the checkpoint's covered source position in one complete ancestry
// only when both the source identity and incremental lineage hash match. It rejects checkpoints
// from another branch even when a source ID happens to be reused by malformed external data.
func CheckpointSourceIndex(value Checkpoint, sourcePath []string) int {
	if !SourceAlignedCheckpoint(value) {
		return -1
	}
	current := ""
	for index, sourceID := range normalizeStrings(sourcePath) {
		current = ExtendLineageHash(current, sourceID)
		if sourceID == value.CoveredThroughSourceID && current == value.CoveredPathHash {
			return index
		}
	}
	return -1
}

func StaticFingerprint(values ...string) string {
	normalized := make([]string, len(values))
	for index, value := range values {
		normalized[index] = strings.TrimSpace(value)
	}
	return hashBytes([]byte(strings.Join(normalized, "\x1f")))
}

func ModelWindowFingerprint(value ModelWindow) string {
	value.TokenCountContext = TokenCountContext{}
	encoded, _ := canonicalJSON(value)
	return hashBytes(encoded)
}

func normalizeTokenCountContext(value TokenCountContext) TokenCountContext {
	value.Protocol = strings.TrimSpace(strings.ToLower(value.Protocol))
	value.Model = strings.TrimSpace(value.Model)
	return value
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
	if strings.TrimSpace(value.ID) == "" || strings.TrimSpace(value.ScopeID) == "" || strings.TrimSpace(value.CacheIdentity) == "" ||
		value.Generation <= 0 || value.Revision <= 0 ||
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
	if request.ScopeID == "" || request.StaticFingerprint == "" || len(request.SourcePath) != len(request.Entries) {
		return OpenRequest{}, ErrInvalidInput
	}
	if !request.SourceDelta && (len(request.SourcePath) == 0 || len(request.Entries) == 0) {
		return OpenRequest{}, ErrInvalidInput
	}
	if request.SourceDelta && request.Previous == nil {
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
		ScopeID:       request.ScopeID,
		CacheIdentity: stableID("ctxk", request.ScopeID, request.StaticFingerprint, LineageHash(request.SourcePath...)),
		Generation:    generation, Revision: revision, ParentCheckpointID: strings.TrimSpace(parentID),
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
		value.ParentCheckpointID, value.CacheIdentity, value.LineageHash,
		value.CoveredThroughSourceID, value.CoveredPathHash, value.StaticFingerprint,
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

// hardTokenUpperBound is the fail-safe fallback when no exact provider/model tokenizer is
// available. A provider tokenizer cannot consume more non-empty prompt tokens than there are
// bytes in the canonical provider-neutral request representation: every token must account for at
// least one input byte. Charging one token per serialized byte therefore avoids treating a
// language-specific bytes/token heuristic as a hard proof. The safety margin additionally covers
// bounded provider framing differences between this canonical request and the final wire payload.
//
// This intentionally sacrifices usable window capacity until a host supplies an exact
// TokenCounter; soft-limit decisions continue to use the less conservative bytes/4 estimate.
func hardTokenUpperBound(serialized []byte, safetyPercent int) int64 {
	return applySafetyMargin(int64(len(serialized)), safetyPercent)
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
