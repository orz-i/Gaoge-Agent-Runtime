package harness

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"strings"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	runtimecontext "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/context"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/jsoncontract"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

// VersionRef freezes one host-owned configuration resource identity and revision.
type VersionRef struct {
	ID       string `json:"id"`
	Revision uint64 `json:"revision"`
}

const (
	harnessToolDefinitionVersion = "harness-v1"
	toolRiskLevelLow             = "low"
	toolIdempotencyRequestKey    = "request_key"
)

func normalizeToolPolicies(values []ToolPolicySnapshot) ([]ToolPolicySnapshot, error) {
	result := make([]ToolPolicySnapshot, len(values))
	seen := make(map[string]struct{}, len(values))
	for index, value := range values {
		value.Key = strings.TrimSpace(value.Key)
		value.DefinitionVersion = strings.TrimSpace(value.DefinitionVersion)
		value.ExecutionMode = strings.TrimSpace(value.ExecutionMode)
		value.ApprovalCapability = strings.TrimSpace(value.ApprovalCapability)
		value.ApprovalMode = strings.TrimSpace(value.ApprovalMode)
		value.RiskLevel = strings.TrimSpace(value.RiskLevel)
		value.SideEffectLevel = strings.TrimSpace(value.SideEffectLevel)
		value.IdempotencyMode = strings.TrimSpace(value.IdempotencyMode)
		if value.Key == "" || value.DefinitionVersion == "" {
			return nil, ErrInvalidRequest
		}
		if _, duplicate := seen[value.Key]; duplicate {
			return nil, ErrInvalidRequest
		}
		seen[value.Key] = struct{}{}
		result[index] = value
	}
	slices.SortFunc(result, func(left, right ToolPolicySnapshot) int { return strings.Compare(left.Key, right.Key) })
	return result, nil
}

func normalizeSkillSnapshots(values []SkillSnapshot) ([]SkillSnapshot, error) {
	result := make([]SkillSnapshot, len(values))
	seen := make(map[string]struct{}, len(values))
	for index, value := range values {
		value.ID = strings.TrimSpace(value.ID)
		value.Title = strings.TrimSpace(value.Title)
		value.Trigger = strings.TrimSpace(value.Trigger)
		value.Description = strings.TrimSpace(value.Description)
		value.Markdown = strings.TrimSpace(value.Markdown)
		value.ContentHash = strings.TrimSpace(value.ContentHash)
		if value.ID == "" || value.Revision == 0 || value.Title == "" || value.Markdown == "" {
			return nil, ErrInvalidRequest
		}
		hash := sha256.Sum256([]byte(value.Markdown))
		wantHash := hex.EncodeToString(hash[:])
		if value.ContentHash == "" {
			value.ContentHash = wantHash
		} else if value.ContentHash != wantHash {
			return nil, ErrInvalidRequest
		}
		if _, duplicate := seen[value.ID]; duplicate {
			return nil, ErrInvalidRequest
		}
		seen[value.ID] = struct{}{}
		result[index] = value
	}
	slices.SortFunc(result, func(left, right SkillSnapshot) int { return strings.Compare(left.ID, right.ID) })
	return result, nil
}

// ToolPolicySnapshot freezes one selected Tool's policy and provider-definition version.
// It intentionally contains no credentials, provider endpoint or executable implementation detail.
type ToolPolicySnapshot struct {
	Key                string `json:"key"`
	DefinitionVersion  string `json:"definitionVersion"`
	ExecutionMode      string `json:"executionMode,omitempty"`
	ApprovalCapability string `json:"approvalCapability,omitempty"`
	ApprovalMode       string `json:"approvalMode,omitempty"`
	RiskLevel          string `json:"riskLevel,omitempty"`
	SideEffectLevel    string `json:"sideEffectLevel,omitempty"`
	IdempotencyMode    string `json:"idempotencyMode,omitempty"`
}

// SkillSnapshot freezes one selected Skill revision and its progressive-disclosure content.
// The full markdown is durable Harness configuration, not mutable host lookup state.
type SkillSnapshot struct {
	ID          string `json:"id"`
	Revision    uint64 `json:"revision"`
	Title       string `json:"title"`
	Trigger     string `json:"trigger,omitempty"`
	Description string `json:"description,omitempty"`
	Markdown    string `json:"markdown"`
	ContentHash string `json:"contentHash"`
}

// DelegationPolicySnapshot freezes the delegation boundary for one Harness Turn.
// MaxDepth counts delegation edges below the root Agent. New Turns resolve zero
// from Runner execution policy; zero remains readable only for persisted legacy
// Turns created before runtime-owned delegation defaults.
type DelegationPolicySnapshot struct {
	MaxDepth int `json:"maxDepth,omitempty"`
	// LocalToolGrants authorize a named, versioned member to receive a tool
	// outside the root's callable set. They share the immutable ToolPolicies.
	LocalToolGrants []DelegatedLocalToolGrant `json:"localToolGrants,omitempty"`
	// NamedAgents are Host-selected Agent execution inputs, not another
	// executable Role/Feature interpreter. Their model, local/hosted Tools,
	// private Skills and per-call limits are frozen with the original Turn.
	NamedAgents []NamedAgentSnapshot `json:"namedAgents,omitempty"`
}

// AgentSpec is the sole immutable Host-selected execution capability for both
// the Root Agent and each named child. The Host owns selection and grants;
// the native Agent Runtime owns execution and recovery. ConfigSnapshot carries
// Turn/Context/approval identity; member snapshots carry member identity only.
// Registered Tool handlers/credentials never enter this durable value.
type AgentSpec struct {
	Model                string                  `json:"model,omitempty"`
	ModelOptions         json.RawMessage         `json:"modelOptions,omitempty"`
	Instructions         string                  `json:"instructions,omitempty"`
	OutputContract       *model.OutputContract   `json:"outputContract,omitempty"`
	MaxModelOutputTokens int64                   `json:"maxModelOutputTokens,omitempty"`
	ToolKeys             []string                `json:"toolKeys,omitempty"`
	InitialToolKeys      []string                `json:"initialToolKeys,omitempty"`
	HostedToolGrants     []agent.HostedToolGrant `json:"hostedToolGrants,omitempty"`
	Skills               []SkillSnapshot         `json:"skills,omitempty"`
	Limits               agent.Limits            `json:"limits,omitempty"`
}

// NamedAgentSnapshot freezes one member identity and the same executable
// AgentSpec used by Root. There is no second definition of Agent capabilities.
type NamedAgentSnapshot struct {
	MemberID       string `json:"memberID"`
	MemberRevision string `json:"memberRevision"`
	AgentSpec
}

// normalizeAgentSpec is the sole capability normalization/authorization-shape
// boundary for Root, child selection, frozen Handoff and resumed execution.
// Callers still enforce their own Host-specific registration/grant constraints.
func normalizeAgentSpec(value AgentSpec) (AgentSpec, error) {
	value.Model = strings.TrimSpace(value.Model)
	value.Instructions = strings.TrimSpace(value.Instructions)
	if value.MaxModelOutputTokens < 0 || !enforceableHarnessBudgetLimits(value.Limits) ||
		model.ValidateOutputContract(value.OutputContract) != nil {
		return AgentSpec{}, ErrInvalidRequest
	}
	var err error
	value.ModelOptions, err = canonicalJSON(value.ModelOptions)
	if err != nil {
		return AgentSpec{}, err
	}
	value.OutputContract = model.CloneOutputContract(value.OutputContract)
	value.ToolKeys = normalizeStrings(value.ToolKeys)
	value.InitialToolKeys = normalizeStrings(value.InitialToolKeys)
	value.HostedToolGrants, err = agent.NormalizeHostedToolGrantsForRole(value.HostedToolGrants)
	if err != nil {
		return AgentSpec{}, ErrInvalidRequest
	}
	for _, grant := range value.HostedToolGrants {
		if slices.Contains(value.ToolKeys, grant.Key) {
			return AgentSpec{}, ErrInvalidRequest
		}
	}
	for _, key := range value.InitialToolKeys {
		if !slices.Contains(value.ToolKeys, key) && !containsHostedGrant(value.HostedToolGrants, key) {
			return AgentSpec{}, ErrInvalidRequest
		}
	}
	value.Skills, err = normalizeSkillSnapshots(value.Skills)
	if err != nil {
		return AgentSpec{}, err
	}
	return value, nil
}

func containsHostedGrant(grants []agent.HostedToolGrant, key string) bool {
	for _, grant := range grants {
		if grant.Key == key {
			return true
		}
	}
	return false
}

func cloneAgentSpec(value AgentSpec) AgentSpec {
	value.ModelOptions = append(json.RawMessage(nil), value.ModelOptions...)
	value.OutputContract = model.CloneOutputContract(value.OutputContract)
	value.ToolKeys = slices.Clone(value.ToolKeys)
	value.InitialToolKeys = slices.Clone(value.InitialToolKeys)
	value.HostedToolGrants = slices.Clone(value.HostedToolGrants)
	value.Skills = slices.Clone(value.Skills)
	return value
}

// startRequest maps one frozen Host AgentSpec into the native Agent Runtime.
// Runtime-owned Run identity, authorization and original execution allowances
// are filled only by the caller at its original protected admission boundary.
func (value AgentSpec) startRequest() agent.StartRequest {
	return agent.StartRequest{
		Model: value.Model, ModelOptions: append(json.RawMessage(nil), value.ModelOptions...),
		Instructions: value.Instructions, OutputContract: model.CloneOutputContract(value.OutputContract),
		MaxModelOutputTokens: value.MaxModelOutputTokens,
		ToolKeys:             slices.Clone(value.ToolKeys), InitialToolKeys: slices.Clone(value.InitialToolKeys),
		HostedToolGrants: slices.Clone(value.HostedToolGrants), Limits: value.Limits,
	}
}

func normalizeNamedAgentSnapshots(values []NamedAgentSnapshot) ([]NamedAgentSnapshot, error) {
	if len(values) == 0 {
		return nil, nil
	}
	normalized := make([]NamedAgentSnapshot, len(values))
	seen := make(map[string]struct{}, len(values))
	for i, value := range values {
		value.MemberID = strings.TrimSpace(value.MemberID)
		value.MemberRevision = strings.TrimSpace(value.MemberRevision)
		if value.MemberID == "" || value.MemberRevision == "" || len(value.MemberID) > 160 ||
			len(value.MemberRevision) > 256 || strings.TrimSpace(value.Model) == "" {
			return nil, ErrInvalidRequest
		}
		if _, duplicate := seen[value.MemberID]; duplicate {
			return nil, ErrInvalidRequest
		}
		seen[value.MemberID] = struct{}{}
		var err error
		value.AgentSpec, err = normalizeAgentSpec(value.AgentSpec)
		if err != nil {
			return nil, err
		}
		normalized[i] = value
	}
	slices.SortFunc(normalized, func(a, b NamedAgentSnapshot) int { return strings.Compare(a.MemberID, b.MemberID) })
	return normalized, nil
}

func cloneNamedAgentSnapshots(values []NamedAgentSnapshot) []NamedAgentSnapshot {
	if len(values) == 0 {
		return nil
	}
	copied := make([]NamedAgentSnapshot, len(values))
	for i, value := range values {
		value.AgentSpec = cloneAgentSpec(value.AgentSpec)
		copied[i] = value
	}
	return copied
}

func namedAgentForMember(config ConfigSnapshot, memberID, revision string) (NamedAgentSnapshot, bool) {
	for _, selected := range config.DelegationPolicy.NamedAgents {
		if selected.MemberID == memberID && selected.MemberRevision == revision {
			return selected, true
		}
	}
	return NamedAgentSnapshot{}, false
}

type DelegatedLocalToolGrant struct {
	MemberID       string `json:"memberID"`
	MemberRevision string `json:"memberRevision"`
	ToolKey        string `json:"toolKey"`
}

// ConfigSnapshot is one immutable execution configuration. It intentionally excludes secrets and transport endpoints.
type ConfigSnapshot struct {
	ID          string     `json:"id"`
	TurnID      string     `json:"turnID"`
	Environment VersionRef `json:"environment"`
	AgentSpec
	// Turn/Host authority is not a second Agent capability definition.
	ToolPolicies          []ToolPolicySnapshot             `json:"toolPolicies"`
	ContextPolicy         runtimecontext.Policy            `json:"contextPolicy"`
	ApprovalPolicyVersion uint64                           `json:"approvalPolicyVersion"`
	SharedBudget          *budget.Limits                   `json:"sharedBudget,omitempty"`
	DelegationPolicy      DelegationPolicySnapshot         `json:"delegationPolicy,omitempty"`
	ContentHash           string                           `json:"contentHash"`
	CreatedAt             time.Time                        `json:"createdAt"`
	AdmissionHash         string                           `json:"admissionHash,omitempty"`
	InputMessage          *HostRef                         `json:"inputMessage,omitempty"`
	OutputMessage         *HostRef                         `json:"outputMessage,omitempty"`
	ContextBase           *ContextBaseBinding              `json:"contextBase,omitempty"`
	WorkingStateSummary   *agent.WorkingStateSummaryPolicy `json:"workingStateSummary,omitempty"`
}

type configPayload struct {
	TurnID      string     `json:"turnID"`
	Environment VersionRef `json:"environment"`
	AgentSpec
	ToolPolicies          []ToolPolicySnapshot             `json:"toolPolicies"`
	ContextPolicy         runtimecontext.Policy            `json:"contextPolicy"`
	ApprovalPolicyVersion uint64                           `json:"approvalPolicyVersion"`
	SharedBudget          *budget.Limits                   `json:"sharedBudget,omitempty"`
	DelegationPolicy      DelegationPolicySnapshot         `json:"delegationPolicy,omitempty"`
	AdmissionHash         string                           `json:"admissionHash,omitempty"`
	InputMessage          *HostRef                         `json:"inputMessage,omitempty"`
	OutputMessage         *HostRef                         `json:"outputMessage,omitempty"`
	ContextBase           *ContextBaseBinding              `json:"contextBase,omitempty"`
	WorkingStateSummary   *agent.WorkingStateSummaryPolicy `json:"workingStateSummary,omitempty"`
}

// SealConfigSnapshot normalizes, hashes and seals one immutable configuration for a Harness Turn.
func SealConfigSnapshot(turnID string, value ConfigSnapshot, now time.Time) (ConfigSnapshot, error) {
	// Enforce canonical original bytes before any string normalization or
	// JSON hash can collapse distinct submitted identities to U+FFFD.
	if jsoncontract.ValidateCanonicalUTF8(turnID) != nil || jsoncontract.ValidateCanonicalUTF8(value) != nil {
		return ConfigSnapshot{}, ErrInvalidRequest
	}
	turnID = strings.TrimSpace(turnID)
	if turnID == "" || now.IsZero() {
		return ConfigSnapshot{}, ErrInvalidRequest
	}
	value.TurnID = turnID
	value.Environment.ID = strings.TrimSpace(value.Environment.ID)
	value.ContextPolicy = runtimecontext.NormalizePolicy(value.ContextPolicy)
	value.AdmissionHash = strings.TrimSpace(value.AdmissionHash)
	if value.AdmissionHash != "" {
		if len(value.AdmissionHash) != 64 {
			return ConfigSnapshot{}, ErrInvalidRequest
		}
		if _, err := hex.DecodeString(value.AdmissionHash); err != nil {
			return ConfigSnapshot{}, ErrInvalidRequest
		}
	}
	var refErr error
	value.InputMessage, refErr = cloneAdmissionHostRef(value.InputMessage)
	if refErr != nil {
		return ConfigSnapshot{}, refErr
	}
	value.OutputMessage, refErr = cloneAdmissionHostRef(value.OutputMessage)
	if refErr != nil {
		return ConfigSnapshot{}, refErr
	}
	if (value.Environment.ID == "") != (value.Environment.Revision == 0) {
		return ConfigSnapshot{}, ErrInvalidRequest
	}
	var err error
	value.AgentSpec, err = normalizeAgentSpec(value.AgentSpec)
	if err != nil {
		return ConfigSnapshot{}, err
	}
	if agent.ValidateWorkingStateSummaryPolicy(value.WorkingStateSummary) != nil ||
		(value.WorkingStateSummary != nil && (value.MaxModelOutputTokens <= 0 || value.SharedBudget != nil)) {
		return ConfigSnapshot{}, ErrInvalidRequest
	}
	value.WorkingStateSummary = agent.CloneWorkingStateSummaryPolicy(value.WorkingStateSummary)
	if err := validateContextBaseBinding(value.ContextBase); err != nil {
		return ConfigSnapshot{}, err
	}
	value.ContextBase = cloneContextBaseBinding(value.ContextBase)
	value.ToolPolicies, err = normalizeToolPolicies(value.ToolPolicies)
	if err != nil {
		return ConfigSnapshot{}, err
	}
	value.DelegationPolicy.LocalToolGrants, err = normalizeDelegatedLocalToolGrants(value)
	if err != nil {
		return ConfigSnapshot{}, err
	}
	value.DelegationPolicy.NamedAgents, err = normalizeNamedAgentSnapshots(value.DelegationPolicy.NamedAgents)
	if err != nil || (len(value.DelegationPolicy.NamedAgents) != 0 && value.DelegationPolicy.MaxDepth <= 0) {
		return ConfigSnapshot{}, ErrInvalidRequest
	}
	if value.SharedBudget != nil {
		if !enforceableHarnessBudgetLimits(*value.SharedBudget) {
			return ConfigSnapshot{}, ErrInvalidRequest
		}
		// The sealed hash and later execution must use the same owned value.
		copied := *value.SharedBudget
		value.SharedBudget = &copied
	}
	if value.DelegationPolicy.MaxDepth < 0 || value.DelegationPolicy.MaxDepth > 32 {
		return ConfigSnapshot{}, ErrInvalidRequest
	}
	payload := configPayload{
		TurnID: turnID, Environment: value.Environment, AgentSpec: value.AgentSpec,
		ToolPolicies: value.ToolPolicies, ContextPolicy: value.ContextPolicy,
		ApprovalPolicyVersion: value.ApprovalPolicyVersion, SharedBudget: value.SharedBudget,
		DelegationPolicy: value.DelegationPolicy, AdmissionHash: value.AdmissionHash,
		InputMessage: value.InputMessage, OutputMessage: value.OutputMessage,
		ContextBase: value.ContextBase, WorkingStateSummary: value.WorkingStateSummary,
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return ConfigSnapshot{}, err
	}
	hash := sha256.Sum256(raw)
	value.ContentHash = hex.EncodeToString(hash[:])
	value.ID = "hcfg_" + value.ContentHash[:24]
	value.CreatedAt = now.UTC()
	return value, nil
}

func canonicalJSON(value json.RawMessage) (json.RawMessage, error) {
	trimmed := bytes.TrimSpace(value)
	if len(trimmed) == 0 {
		return nil, nil
	}
	// Validate original bytes before json.Unmarshal can erase duplicates or
	// Unicode/case-fold aliases for Host-supplied execution options.
	if err := jsoncontract.ValidateUniqueJSONObject(trimmed); err != nil {
		return nil, ErrInvalidRequest
	}
	var decoded any
	if err := json.Unmarshal(trimmed, &decoded); err != nil {
		return nil, ErrInvalidRequest
	}
	encoded, err := json.Marshal(decoded)
	if err != nil {
		return nil, ErrInvalidRequest
	}
	return encoded, nil
}

func normalizeStrings(values []string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			result = append(result, value)
		}
	}
	slices.Sort(result)
	return slices.Compact(result)
}

// The Harness SharedBudget meter has no authoritative cost or state-byte
// consumption signal. Never admit these as hard ceilings: storing an unused
// limit in a durable ledger is not enforcement. Other Features may support
// these budget dimensions independently.
func enforceableHarnessBudgetLimits(value budget.Limits) bool {
	return budget.ValidLimits(value) && value.MaxCostUnits == 0 && value.MaxStateBytes == 0
}

func cloneConfigSnapshot(value ConfigSnapshot) ConfigSnapshot {
	value.ContextBase = cloneContextBaseBinding(value.ContextBase)
	value.WorkingStateSummary = agent.CloneWorkingStateSummaryPolicy(value.WorkingStateSummary)
	if value.InputMessage != nil {
		copied := *value.InputMessage
		value.InputMessage = &copied
	}
	if value.OutputMessage != nil {
		copied := *value.OutputMessage
		value.OutputMessage = &copied
	}
	if value.SharedBudget != nil {
		copied := *value.SharedBudget
		value.SharedBudget = &copied
	}
	value.AgentSpec = cloneAgentSpec(value.AgentSpec)
	value.ToolPolicies = append([]ToolPolicySnapshot(nil), value.ToolPolicies...)
	value.DelegationPolicy.LocalToolGrants = slices.Clone(value.DelegationPolicy.LocalToolGrants)
	value.DelegationPolicy.NamedAgents = cloneNamedAgentSnapshots(value.DelegationPolicy.NamedAgents)
	return value
}

func sameWorkingStateSummaryPolicy(left, right *agent.WorkingStateSummaryPolicy) bool {
	if left == nil || right == nil {
		return left == right
	}
	return *left == *right
}

func defaultJSON(value json.RawMessage) json.RawMessage {
	if len(bytes.TrimSpace(value)) == 0 {
		return json.RawMessage(`{}`)
	}
	return value
}
