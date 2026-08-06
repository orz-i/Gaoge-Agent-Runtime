package context

import (
	stdcontext "context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"strings"

	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/kernel"
)

const CapabilityBuilder kernel.Capability = "context.builder"

var (
	ErrInvalidInput   = errors.New("invalid context input")
	ErrBudgetExceeded = errors.New("context budget exceeded")
)

// ItemKind identifies one closed prompt item kind.
type ItemKind string

const (
	ItemMessage    ItemKind = "message"
	ItemToolCall   ItemKind = "tool_call"
	ItemToolResult ItemKind = "tool_result"
	ItemSummary    ItemKind = "summary"
)

// Role identifies one provider-neutral prompt role.
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// CountSource records whether token accounting used an exact counter.
type CountSource string

const (
	CountExact     CountSource = "exact"
	CountEstimated CountSource = "estimated"
)

// ArtifactKind identifies durable content removed from the inline prompt.
type ArtifactKind string

const (
	ArtifactToolResult ArtifactKind = "tool_result"
	ArtifactSummary    ArtifactKind = "thread_summary"
)

// Item is one ordered prompt item. TurnID is the atomic trimming boundary.
type Item struct {
	ID           string   `json:"id"`
	TurnID       string   `json:"turnID"`
	Kind         ItemKind `json:"kind"`
	Role         Role     `json:"role"`
	Content      string   `json:"content"`
	ToolCallID   string   `json:"toolCallID,omitempty"`
	ToolName     string   `json:"toolName,omitempty"`
	ProjectionID string   `json:"projectionID,omitempty"`
	Required     bool     `json:"required,omitempty"`
}

// ToolDefinition is one complete model-visible Tool contract.
type ToolDefinition struct {
	Key         string          `json:"key"`
	Description string          `json:"description,omitempty"`
	Schema      json.RawMessage `json:"schema"`
}

// Prompt is the complete self-contained request counted by Context.
type Prompt struct {
	Instructions string           `json:"instructions,omitempty"`
	Items        []Item           `json:"items"`
	Tools        []ToolDefinition `json:"tools,omitempty"`
	Options      json.RawMessage  `json:"options"`
}

// Budget is the immutable Context policy for one build.
type Budget struct {
	MaxInputTokens        int64 `json:"maxInputTokens"`
	EffectiveModelTokens  int64 `json:"effectiveModelTokens"`
	SoftLimitPercent      int   `json:"softLimitPercent"`
	EstimateSafetyPercent int   `json:"estimateSafetyPercent"`
	MaxSerializedBytes    int64 `json:"maxSerializedBytes"`
	PreserveRecentTurns   int   `json:"preserveRecentTurns"`
	MaxSummaryTokens      int   `json:"maxSummaryTokens"`
	MaxToolResultBytes    int   `json:"maxToolResultBytes"`
}

// Assessment is the content-free budget accounting result.
type Assessment struct {
	HardInputTokens       int64       `json:"hardInputTokens"`
	SoftInputTokens       int64       `json:"softInputTokens"`
	RawTokenEstimate      int64       `json:"rawTokenEstimate"`
	AdjustedTokenEstimate int64       `json:"adjustedTokenEstimate"`
	TokenCountSource      CountSource `json:"tokenCountSource"`
	SerializedBytes       int64       `json:"serializedBytes"`
}

// TrimAction records one deterministic prompt management operation.
type TrimAction struct {
	Kind        string `json:"kind"`
	TurnID      string `json:"turnID,omitempty"`
	ItemCount   int    `json:"itemCount,omitempty"`
	ArtifactID  string `json:"artifactID,omitempty"`
	ContentHash string `json:"contentHash,omitempty"`
}

// Artifact seals full content removed from the inline prompt.
type Artifact struct {
	ID            string          `json:"id"`
	Kind          ArtifactKind    `json:"kind"`
	RunID         string          `json:"runID"`
	SourceID      string          `json:"sourceID"`
	SourceTitle   string          `json:"sourceTitle,omitempty"`
	Content       string          `json:"content"`
	ContentJSON   json.RawMessage `json:"contentJSON"`
	ContentHash   string          `json:"contentHash"`
	TokenEstimate int64           `json:"tokenEstimate"`
}

// Summary records the deterministic coverage injected as untrusted history.
type Summary struct {
	ArtifactID     string `json:"artifactID"`
	CoveredTurns   int    `json:"coveredTurns"`
	CoveredItems   int    `json:"coveredItems"`
	CoveredThrough string `json:"coveredThrough"`
	TokenEstimate  int64  `json:"tokenEstimate"`
	Strategy       string `json:"strategy"`
}

// ManagementTrace is the persisted content-free management decision.
type ManagementTrace struct {
	Mode              string       `json:"mode"`
	LoadedItemCount   int          `json:"loadedItemCount"`
	RetainedItemCount int          `json:"retainedItemCount"`
	SummarizedItems   int          `json:"summarizedItems"`
	TrimmedItems      int          `json:"trimmedItems"`
	ArtifactCount     int          `json:"artifactCount"`
	Summary           *Summary     `json:"summary,omitempty"`
	Raw               Assessment   `json:"raw"`
	Managed           Assessment   `json:"managed"`
	Actions           []TrimAction `json:"actions,omitempty"`
}

// Snapshot is one immutable self-contained Context revision.
type Snapshot struct {
	ID                   string          `json:"id"`
	RunID                string          `json:"runID"`
	Revision             int             `json:"revision"`
	SupersedesSnapshotID string          `json:"supersedesSnapshotID,omitempty"`
	ThreadPathHash       string          `json:"threadPathHash"`
	Content              json.RawMessage `json:"content"`
	ContentHash          string          `json:"contentHash"`
	ArtifactIDs          []string        `json:"artifactIDs,omitempty"`
	Trace                ManagementTrace `json:"trace"`
}

// BuildRequest supplies one complete immutable prompt candidate.
type BuildRequest struct {
	RunID                string
	Revision             int
	SupersedesSnapshotID string
	ThreadPathHash       string
	CurrentTurnID        string
	Prompt               Prompt
	Budget               Budget
}

// BuildResult returns the immutable Snapshot and sealed Artifacts.
type BuildResult struct {
	Snapshot  Snapshot
	Artifacts []Artifact
}

// TokenCounter counts one complete serialized Prompt.
type TokenCounter interface {
	Count(stdcontext.Context, []byte) (int64, error)
}

// Dependencies are optional Context capabilities.
type Dependencies struct {
	Counter TokenCounter
}

// Builder deterministically manages and seals one complete Context Snapshot.
type Builder struct {
	counter TokenCounter
}

// NewBuilder creates an independent Context capability.
func NewBuilder(dependencies Dependencies) *Builder {
	return &Builder{counter: dependencies.Counter}
}

// Descriptor declares the Context capability without requiring another feature.
func (builder *Builder) Descriptor() kernel.FeatureDescriptor {
	return kernel.FeatureDescriptor{Name: "context", Provides: []kernel.Capability{CapabilityBuilder}}
}

// Build applies deterministic management and returns an immutable Snapshot revision.
func (builder *Builder) Build(ctx stdcontext.Context, request BuildRequest) (BuildResult, error) {
	normalized, err := normalizeBuildRequest(request)
	if err != nil {
		return BuildResult{}, err
	}
	raw, err := builder.assess(ctx, normalized.Prompt, normalized.Budget)
	if err != nil {
		return BuildResult{}, err
	}
	managed := clonePrompt(normalized.Prompt)
	managed, toolArtifacts, actions := compactToolResults(normalized.RunID, managed, normalized.Budget)
	managedAssessment, err := builder.assess(ctx, managed, normalized.Budget)
	if err != nil {
		return BuildResult{}, err
	}
	managed, summaryArtifact, summary, summaryActions := summarizeOldTurns(
		normalized.RunID,
		normalized.CurrentTurnID,
		managed,
		normalized.Budget,
		managedAssessment,
	)
	actions = append(actions, summaryActions...)
	artifacts := append([]Artifact(nil), toolArtifacts...)
	if summaryArtifact != nil {
		artifacts = append(artifacts, *summaryArtifact)
	}
	managed, managedAssessment, trimActions, err := builder.trimToHardBudget(
		ctx,
		normalized.CurrentTurnID,
		managed,
		normalized.Budget,
	)
	if err != nil {
		return BuildResult{}, err
	}
	actions = append(actions, trimActions...)
	content, err := canonicalJSON(managed)
	if err != nil {
		return BuildResult{}, err
	}
	contentHash := hashBytes(content)
	artifactIDs := artifactIDs(artifacts)
	trace := ManagementTrace{
		Mode: "managed", LoadedItemCount: len(normalized.Prompt.Items), RetainedItemCount: len(managed.Items),
		SummarizedItems: summarizedItemCount(actions), TrimmedItems: trimmedItemCount(actions),
		ArtifactCount: len(artifacts), Summary: summary, Raw: raw, Managed: managedAssessment,
		Actions: append([]TrimAction(nil), actions...),
	}
	snapshotID := stableID("ctxs", normalized.RunID, integerString(normalized.Revision), normalized.ThreadPathHash, contentHash)
	return BuildResult{
		Snapshot: Snapshot{
			ID: snapshotID, RunID: normalized.RunID, Revision: normalized.Revision,
			SupersedesSnapshotID: normalized.SupersedesSnapshotID, ThreadPathHash: normalized.ThreadPathHash,
			Content: content, ContentHash: contentHash, ArtifactIDs: artifactIDs, Trace: trace,
		},
		Artifacts: cloneArtifacts(artifacts),
	}, nil
}

func (builder *Builder) trimToHardBudget(
	ctx stdcontext.Context,
	currentTurnID string,
	prompt Prompt,
	budget Budget,
) (Prompt, Assessment, []TrimAction, error) {
	managed := clonePrompt(prompt)
	actions := make([]TrimAction, 0)
	for {
		assessment, err := builder.assess(ctx, managed, budget)
		if err != nil {
			return Prompt{}, Assessment{}, nil, err
		}
		if withinHardBudget(assessment, budget) {
			return managed, assessment, actions, nil
		}
		turnID, removable := oldestRemovableTurn(managed.Items, currentTurnID, budget.PreserveRecentTurns)
		if !removable {
			return Prompt{}, assessment, actions, ErrBudgetExceeded
		}
		var removed int
		managed.Items, removed = removeTurn(managed.Items, turnID)
		actions = append(actions, TrimAction{Kind: "drop_oldest_complete_turn", TurnID: turnID, ItemCount: removed})
	}
}

func (builder *Builder) assess(ctx stdcontext.Context, prompt Prompt, budget Budget) (Assessment, error) {
	serialized, err := canonicalJSON(prompt)
	if err != nil {
		return Assessment{}, err
	}
	hard := hardTokenBudget(budget)
	soft := softTokenBudget(hard, budget.SoftLimitPercent)
	if builder != nil && builder.counter != nil {
		count, countErr := builder.counter.Count(ctx, serialized)
		if countErr == nil && count >= 0 {
			return Assessment{
				HardInputTokens: hard, SoftInputTokens: soft, RawTokenEstimate: count,
				AdjustedTokenEstimate: count, TokenCountSource: CountExact, SerializedBytes: int64(len(serialized)),
			}, nil
		}
	}
	raw := estimatedTokens(serialized)
	adjusted := applySafetyMargin(raw, budget.EstimateSafetyPercent)
	return Assessment{
		HardInputTokens: hard, SoftInputTokens: soft, RawTokenEstimate: raw,
		AdjustedTokenEstimate: adjusted, TokenCountSource: CountEstimated, SerializedBytes: int64(len(serialized)),
	}, nil
}

func normalizeBuildRequest(request BuildRequest) (BuildRequest, error) {
	request.RunID = strings.TrimSpace(request.RunID)
	request.SupersedesSnapshotID = strings.TrimSpace(request.SupersedesSnapshotID)
	request.ThreadPathHash = strings.TrimSpace(request.ThreadPathHash)
	request.CurrentTurnID = strings.TrimSpace(request.CurrentTurnID)
	request.Budget = normalizeBudget(request.Budget)
	request.Prompt = normalizePrompt(request.Prompt)
	if request.RunID == "" || request.Revision <= 0 || request.ThreadPathHash == "" ||
		request.CurrentTurnID == "" || !validBudget(request.Budget) || !validPrompt(request.Prompt, request.CurrentTurnID) {
		return BuildRequest{}, ErrInvalidInput
	}
	return request, nil
}

func normalizeBudget(budget Budget) Budget {
	if budget.SoftLimitPercent <= 0 || budget.SoftLimitPercent >= 100 {
		budget.SoftLimitPercent = 80
	}
	if budget.EstimateSafetyPercent < 0 {
		budget.EstimateSafetyPercent = 0
	}
	if budget.EstimateSafetyPercent == 0 {
		budget.EstimateSafetyPercent = 15
	}
	if budget.MaxSerializedBytes <= 0 {
		budget.MaxSerializedBytes = 4 << 20
	}
	if budget.PreserveRecentTurns <= 0 {
		budget.PreserveRecentTurns = 8
	}
	if budget.MaxSummaryTokens <= 0 {
		budget.MaxSummaryTokens = 1024
	}
	if budget.MaxToolResultBytes <= 0 {
		budget.MaxToolResultBytes = 2048
	}
	return budget
}

func normalizePrompt(prompt Prompt) Prompt {
	prompt.Instructions = strings.TrimSpace(prompt.Instructions)
	prompt.Items = cloneItems(prompt.Items)
	for index := range prompt.Items {
		prompt.Items[index] = normalizeItem(prompt.Items[index])
	}
	prompt.Tools = cloneTools(prompt.Tools)
	for index := range prompt.Tools {
		prompt.Tools[index].Key = strings.TrimSpace(prompt.Tools[index].Key)
		prompt.Tools[index].Description = strings.TrimSpace(prompt.Tools[index].Description)
		prompt.Tools[index].Schema = normalizeRawJSON(prompt.Tools[index].Schema, json.RawMessage(`true`))
	}
	sort.Slice(prompt.Tools, func(left int, right int) bool { return prompt.Tools[left].Key < prompt.Tools[right].Key })
	prompt.Options = normalizeRawJSON(prompt.Options, json.RawMessage(`{}`))
	return prompt
}

func normalizeItem(item Item) Item {
	item.ID = strings.TrimSpace(item.ID)
	item.TurnID = strings.TrimSpace(item.TurnID)
	item.Content = strings.TrimSpace(item.Content)
	item.ToolCallID = strings.TrimSpace(item.ToolCallID)
	item.ToolName = strings.TrimSpace(item.ToolName)
	item.ProjectionID = strings.TrimSpace(item.ProjectionID)
	return item
}

func validBudget(budget Budget) bool {
	return hardTokenBudget(budget) > 0 && budget.MaxSerializedBytes > 0 &&
		budget.PreserveRecentTurns > 0 && budget.MaxSummaryTokens > 0 && budget.MaxToolResultBytes >= 256
}

func validPrompt(prompt Prompt, currentTurnID string) bool {
	if !json.Valid(prompt.Options) || len(prompt.Items) == 0 {
		return false
	}
	return validPromptItems(prompt.Items, currentTurnID) && validToolDefinitions(prompt.Tools) && validToolPairs(prompt.Items)
}

func validPromptItems(items []Item, currentTurnID string) bool {
	currentFound := false
	seenItems := make(map[string]struct{}, len(items))
	for _, item := range items {
		if !validItem(item) {
			return false
		}
		if _, duplicate := seenItems[item.ID]; duplicate {
			return false
		}
		seenItems[item.ID] = struct{}{}
		currentFound = currentFound || item.TurnID == currentTurnID
	}
	return currentFound
}

func validToolDefinitions(tools []ToolDefinition) bool {
	seenTools := make(map[string]struct{}, len(tools))
	for _, tool := range tools {
		if tool.Key == "" || !json.Valid(tool.Schema) {
			return false
		}
		if _, duplicate := seenTools[tool.Key]; duplicate {
			return false
		}
		seenTools[tool.Key] = struct{}{}
	}
	return true
}

type toolPair struct {
	turnID      string
	callIndex   int
	resultIndex int
}

func validToolPairs(items []Item) bool {
	pairs := make(map[string]toolPair)
	for index, item := range items {
		if item.Kind != ItemToolCall && item.Kind != ItemToolResult {
			continue
		}
		pair, ok := updateToolPair(pairs[item.ToolCallID], item, index)
		if !ok {
			return false
		}
		pairs[item.ToolCallID] = pair
	}
	for _, pair := range pairs {
		if !completeToolPair(pair) {
			return false
		}
	}
	return true
}

func updateToolPair(pair toolPair, item Item, index int) (toolPair, bool) {
	if pair.turnID != "" && pair.turnID != item.TurnID {
		return toolPair{}, false
	}
	pair.turnID = item.TurnID
	if item.Kind == ItemToolCall {
		if pair.callIndex != 0 {
			return toolPair{}, false
		}
		pair.callIndex = index + 1
		return pair, true
	}
	if pair.resultIndex != 0 {
		return toolPair{}, false
	}
	pair.resultIndex = index + 1
	return pair, true
}

func completeToolPair(pair toolPair) bool {
	return pair.callIndex > 0 && pair.resultIndex > 0 && pair.callIndex < pair.resultIndex
}

func validItem(item Item) bool {
	if item.ID == "" || item.TurnID == "" || item.Content == "" {
		return false
	}
	switch item.Kind {
	case ItemMessage:
		return validMessageItem(item)
	case ItemToolCall:
		return validToolCallItem(item)
	case ItemToolResult:
		return validToolResultItem(item)
	case ItemSummary:
		return validSummaryItem(item)
	default:
		return false
	}
}

func validMessageItem(item Item) bool {
	return item.Role == RoleSystem || item.Role == RoleUser || item.Role == RoleAssistant
}

func validToolCallItem(item Item) bool {
	return item.Role == RoleAssistant && item.ToolCallID != "" && item.ToolName != ""
}

func validToolResultItem(item Item) bool {
	return item.Role == RoleTool && item.ToolCallID != "" && item.ToolName != ""
}

func validSummaryItem(item Item) bool { return item.Role == RoleSystem }

func hardTokenBudget(budget Budget) int64 {
	configured := budget.MaxInputTokens
	model := budget.EffectiveModelTokens
	if configured > 0 && (model <= 0 || configured < model) {
		return configured
	}
	return model
}

func softTokenBudget(hard int64, percent int) int64 {
	if hard <= 0 {
		return 0
	}
	soft := hard * int64(percent) / 100
	if soft < 1 {
		return 1
	}
	return soft
}

func withinHardBudget(assessment Assessment, budget Budget) bool {
	return assessment.AdjustedTokenEstimate <= assessment.HardInputTokens &&
		assessment.SerializedBytes <= budget.MaxSerializedBytes
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

func oldestRemovableTurn(items []Item, currentTurnID string, preserveRecent int) (string, bool) {
	protected := recentTurnSet(items, preserveRecent)
	for _, turnID := range orderedTurnIDs(items) {
		if turnID == currentTurnID || turnRequired(items, turnID) || turnID == summaryTurnID {
			continue
		}
		if _, keep := protected[turnID]; keep {
			continue
		}
		return turnID, true
	}
	return "", false
}

func orderedTurnIDs(items []Item) []string {
	result := make([]string, 0)
	seen := make(map[string]struct{})
	for _, item := range items {
		if _, exists := seen[item.TurnID]; exists {
			continue
		}
		seen[item.TurnID] = struct{}{}
		result = append(result, item.TurnID)
	}
	return result
}

func turnRequired(items []Item, turnID string) bool {
	for _, item := range items {
		if item.TurnID == turnID && item.Required {
			return true
		}
	}
	return false
}

func removeTurn(items []Item, turnID string) ([]Item, int) {
	result := make([]Item, 0, len(items))
	removed := 0
	for _, item := range items {
		if item.TurnID == turnID {
			removed++
			continue
		}
		result = append(result, item)
	}
	return result, removed
}

func artifactIDs(artifacts []Artifact) []string {
	result := make([]string, 0, len(artifacts))
	for _, artifact := range artifacts {
		result = append(result, artifact.ID)
	}
	sort.Strings(result)
	return result
}

func summarizedItemCount(actions []TrimAction) int {
	return actionItemCount(actions, "summarize_complete_turns")
}

func trimmedItemCount(actions []TrimAction) int {
	return actionItemCount(actions, "drop_oldest_complete_turn")
}

func actionItemCount(actions []TrimAction, kind string) int {
	total := 0
	for _, action := range actions {
		if action.Kind == kind {
			total += action.ItemCount
		}
	}
	return total
}

func canonicalJSON(value any) (json.RawMessage, error) {
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

func normalizeRawJSON(value json.RawMessage, fallback json.RawMessage) json.RawMessage {
	if len(value) == 0 {
		value = fallback
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

func hashBytes(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}

func stableID(prefix string, values ...string) string {
	joined := strings.Join(values, "\x1f")
	return prefix + "_" + hashBytes([]byte(joined))[:32]
}

func integerString(value int) string { return strconv.Itoa(value) }

func clonePrompt(prompt Prompt) Prompt {
	prompt.Items = cloneItems(prompt.Items)
	prompt.Tools = cloneTools(prompt.Tools)
	prompt.Options = cloneJSON(prompt.Options)
	return prompt
}

func cloneItems(items []Item) []Item {
	return append([]Item(nil), items...)
}

func cloneTools(tools []ToolDefinition) []ToolDefinition {
	result := append([]ToolDefinition(nil), tools...)
	for index := range result {
		result[index].Schema = cloneJSON(result[index].Schema)
	}
	return result
}

func cloneArtifacts(artifacts []Artifact) []Artifact {
	result := append([]Artifact(nil), artifacts...)
	for index := range result {
		result[index].ContentJSON = cloneJSON(result[index].ContentJSON)
	}
	return result
}

func cloneJSON(value json.RawMessage) json.RawMessage {
	return append(json.RawMessage(nil), value...)
}
