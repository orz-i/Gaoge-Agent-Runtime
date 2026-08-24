package workflow

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strings"
)

var (
	ErrInvalidDefinition = errors.New("invalid workflow definition")
	ErrDefinitionHash    = errors.New("workflow definition hash mismatch")
)

// NodeType is one member of the closed minimal Workflow node union.
type NodeType string

const (
	NodeEffect NodeType = "effect"
	NodeWait   NodeType = "wait"
	NodeReturn NodeType = "return"
)

// CostClass is a provider-neutral maximum billing impact declared by a Definition.
type CostClass string

const (
	CostNone     CostClass = "none"
	CostLow      CostClass = "low"
	CostMedium   CostClass = "medium"
	CostHigh     CostClass = "high"
	CostExternal CostClass = "external_billing"
)

// SideEffectClass is the maximum effect severity a Definition may dispatch.
type SideEffectClass string

const (
	SideEffectNone        SideEffectClass = "none"
	SideEffectRead        SideEffectClass = "read"
	SideEffectWrite       SideEffectClass = "write"
	SideEffectDestructive SideEffectClass = "destructive"
)

// DefinitionPolicy freezes publish-time permission, cost and side-effect impact.
type DefinitionPolicy struct {
	RequiredPermissions []string        `json:"requiredPermissions,omitempty"`
	CostClass           CostClass       `json:"costClass"`
	MaxCostUnits        int64           `json:"maxCostUnits"`
	SideEffectClass     SideEffectClass `json:"sideEffectClass"`
}

// Limits are the frozen hard bounds for one Workflow execution.
type Limits struct {
	MaxNodeActivations       int `json:"maxNodeActivations"`
	MaxEffects               int `json:"maxEffects"`
	MaxSegments              int `json:"maxSegments"`
	MaxActivationsPerSegment int `json:"maxActivationsPerSegment"`
	MaxStateBytes            int `json:"maxStateBytes"`
}

// EffectNode creates one durable external Effect intent.
type EffectNode struct {
	Kind      string          `json:"kind"`
	Input     json.RawMessage `json:"input,omitempty"`
	FromInput bool            `json:"fromInput,omitempty"`
}

// WaitNode creates one explicit host-resolved Wait checkpoint.
type WaitNode struct {
	Kind    string          `json:"kind"`
	Payload json.RawMessage `json:"payload"`
}

// ReturnNode completes the Workflow with canonical JSON.
type ReturnNode struct {
	Value    json.RawMessage `json:"value,omitempty"`
	FromNode string          `json:"fromNode,omitempty"`
}

// Node is a strict data-only union. Exactly one payload must match Type.
type Node struct {
	ID     string      `json:"id"`
	Type   NodeType    `json:"type"`
	Effect *EffectNode `json:"effect,omitempty"`
	Wait   *WaitNode   `json:"wait,omitempty"`
	Return *ReturnNode `json:"return,omitempty"`
}

// DefinitionDraft is the mutable input accepted by CompileDefinition.
type DefinitionDraft struct {
	ID           string           `json:"id"`
	Revision     int              `json:"revision"`
	Name         string           `json:"name"`
	InputSchema  json.RawMessage  `json:"inputSchema"`
	OutputSchema json.RawMessage  `json:"outputSchema"`
	Nodes        []Node           `json:"nodes"`
	Limits       Limits           `json:"limits"`
	Policy       DefinitionPolicy `json:"policy"`
}

// Definition is one compiled immutable Workflow revision.
type Definition struct {
	ID           string           `json:"id"`
	Revision     int              `json:"revision"`
	Name         string           `json:"name"`
	InputSchema  json.RawMessage  `json:"inputSchema"`
	OutputSchema json.RawMessage  `json:"outputSchema"`
	Nodes        []Node           `json:"nodes"`
	Limits       Limits           `json:"limits"`
	Policy       DefinitionPolicy `json:"policy"`
	Hash         string           `json:"hash"`
}

type definitionHashMaterial struct {
	ID           string           `json:"id"`
	Revision     int              `json:"revision"`
	Name         string           `json:"name"`
	InputSchema  json.RawMessage  `json:"inputSchema"`
	OutputSchema json.RawMessage  `json:"outputSchema"`
	Nodes        []Node           `json:"nodes"`
	Limits       Limits           `json:"limits"`
	Policy       DefinitionPolicy `json:"policy"`
}

// CompileDefinition validates, normalizes and hashes one immutable revision.
func CompileDefinition(draft DefinitionDraft) (Definition, error) {
	normalized := normalizeDefinitionDraft(draft)
	if err := validateDefinitionDraft(normalized); err != nil {
		return Definition{}, err
	}
	definition := Definition{
		ID: normalized.ID, Revision: normalized.Revision, Name: normalized.Name,
		InputSchema: cloneJSON(normalized.InputSchema), OutputSchema: cloneJSON(normalized.OutputSchema),
		Nodes: cloneNodes(normalized.Nodes), Limits: normalized.Limits,
		Policy: cloneDefinitionPolicy(normalized.Policy),
	}
	hash, err := definitionHash(definition)
	if err != nil {
		return Definition{}, err
	}
	definition.Hash = hash
	return definition, nil
}

// ValidateDefinition proves that a compiled Definition still matches its frozen hash.
func ValidateDefinition(definition Definition) error {
	draft := normalizeDefinitionDraft(DefinitionDraft{
		ID: definition.ID, Revision: definition.Revision, Name: definition.Name,
		InputSchema: definition.InputSchema, OutputSchema: definition.OutputSchema,
		Nodes: definition.Nodes, Limits: definition.Limits, Policy: definition.Policy,
	})
	if err := validateDefinitionDraft(draft); err != nil {
		return err
	}
	hash, err := definitionHash(Definition{
		ID: draft.ID, Revision: draft.Revision, Name: draft.Name,
		InputSchema: draft.InputSchema, OutputSchema: draft.OutputSchema,
		Nodes: draft.Nodes, Limits: draft.Limits, Policy: draft.Policy,
	})
	if err != nil {
		return err
	}
	if hash != strings.TrimSpace(definition.Hash) {
		return ErrDefinitionHash
	}
	return nil
}

func normalizeDefinitionDraft(draft DefinitionDraft) DefinitionDraft {
	draft.ID = strings.TrimSpace(draft.ID)
	draft.Name = strings.TrimSpace(draft.Name)
	if len(draft.InputSchema) == 0 {
		draft.InputSchema = json.RawMessage(`true`)
	} else {
		draft.InputSchema = normalizeJSON(draft.InputSchema)
	}
	if len(draft.OutputSchema) == 0 {
		draft.OutputSchema = json.RawMessage(`true`)
	} else {
		draft.OutputSchema = normalizeJSON(draft.OutputSchema)
	}
	if draft.Limits.MaxNodeActivations <= 0 {
		draft.Limits.MaxNodeActivations = 256
	}
	if draft.Limits.MaxEffects <= 0 {
		draft.Limits.MaxEffects = 64
	}
	if draft.Limits.MaxSegments <= 0 {
		draft.Limits.MaxSegments = 256
	}
	if draft.Limits.MaxActivationsPerSegment <= 0 {
		draft.Limits.MaxActivationsPerSegment = 32
	}
	if draft.Limits.MaxStateBytes <= 0 {
		draft.Limits.MaxStateBytes = 1 << 20
	}
	draft.Nodes = cloneNodes(draft.Nodes)
	for index := range draft.Nodes {
		draft.Nodes[index] = normalizeNode(draft.Nodes[index])
	}
	draft.Policy = normalizeDefinitionPolicy(draft.Policy)
	return draft
}

func normalizeNode(node Node) Node {
	node.ID = strings.TrimSpace(node.ID)
	if node.Effect != nil {
		effect := *node.Effect
		effect.Kind = strings.TrimSpace(effect.Kind)
		effect.Input = normalizeJSON(effect.Input)
		node.Effect = &effect
	}
	if node.Wait != nil {
		wait := *node.Wait
		wait.Kind = strings.TrimSpace(wait.Kind)
		wait.Payload = normalizeJSON(wait.Payload)
		node.Wait = &wait
	}
	if node.Return != nil {
		terminal := *node.Return
		terminal.Value = normalizeJSON(terminal.Value)
		terminal.FromNode = strings.TrimSpace(terminal.FromNode)
		node.Return = &terminal
	}
	return node
}

func validateDefinitionDraft(draft DefinitionDraft) error {
	if !validDefinitionEnvelope(draft) {
		return ErrInvalidDefinition
	}
	return validateDefinitionNodes(draft.Nodes)
}

func validDefinitionEnvelope(draft DefinitionDraft) bool {
	return draft.ID != "" && draft.Revision > 0 && draft.Name != "" &&
		json.Valid(draft.InputSchema) && json.Valid(draft.OutputSchema) &&
		len(draft.Nodes) > 0 && validLimits(draft.Limits) && validDefinitionPolicy(draft.Policy)
}

func validateDefinitionNodes(nodes []Node) error {
	seen := make(map[string]struct{}, len(nodes))
	for index, node := range nodes {
		if !validNode(node, index == len(nodes)-1) {
			return ErrInvalidDefinition
		}
		if _, duplicate := seen[node.ID]; duplicate {
			return ErrInvalidDefinition
		}
		seen[node.ID] = struct{}{}
	}
	return nil
}

func validLimits(limits Limits) bool {
	return limits.MaxNodeActivations > 0 && limits.MaxEffects > 0 && limits.MaxSegments > 0 &&
		limits.MaxActivationsPerSegment > 0 &&
		limits.MaxActivationsPerSegment <= limits.MaxNodeActivations && limits.MaxStateBytes >= 1024
}

func validNode(node Node, last bool) bool {
	if node.ID == "" {
		return false
	}
	switch node.Type {
	case NodeEffect:
		return validEffectNode(node, last)
	case NodeWait:
		return validWaitNode(node, last)
	case NodeReturn:
		return validReturnNode(node, last)
	default:
		return false
	}
}

func validEffectNode(node Node, last bool) bool {
	if last || node.Effect == nil || node.Wait != nil || node.Return != nil || node.Effect.Kind == "" {
		return false
	}
	hasInput := len(node.Effect.Input) > 0 && json.Valid(node.Effect.Input)
	return hasInput != node.Effect.FromInput
}

func validWaitNode(node Node, last bool) bool {
	return !last && node.Effect == nil && node.Wait != nil && node.Return == nil &&
		node.Wait.Kind != "" && json.Valid(node.Wait.Payload)
}

func validReturnNode(node Node, last bool) bool {
	if !last || node.Effect != nil || node.Wait != nil || node.Return == nil {
		return false
	}
	hasValue := len(node.Return.Value) > 0 && json.Valid(node.Return.Value)
	hasSource := strings.TrimSpace(node.Return.FromNode) != ""
	return hasValue != hasSource
}

func definitionHash(definition Definition) (string, error) {
	material := definitionHashMaterial{
		ID: definition.ID, Revision: definition.Revision, Name: definition.Name,
		InputSchema: cloneJSON(definition.InputSchema), OutputSchema: cloneJSON(definition.OutputSchema),
		Nodes: cloneNodes(definition.Nodes), Limits: definition.Limits,
		Policy: cloneDefinitionPolicy(definition.Policy),
	}
	encoded, err := json.Marshal(material)
	if err != nil {
		return "", errors.Join(ErrInvalidDefinition, err)
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

func cloneDefinition(definition Definition) Definition {
	definition.InputSchema = cloneJSON(definition.InputSchema)
	definition.OutputSchema = cloneJSON(definition.OutputSchema)
	definition.Nodes = cloneNodes(definition.Nodes)
	definition.Policy = cloneDefinitionPolicy(definition.Policy)
	return definition
}

func cloneNodes(nodes []Node) []Node {
	result := make([]Node, len(nodes))
	for index, node := range nodes {
		result[index] = normalizeNode(node)
	}
	return result
}

func cloneJSON(value json.RawMessage) json.RawMessage {
	return append(json.RawMessage(nil), value...)
}

func normalizeDefinitionPolicy(policy DefinitionPolicy) DefinitionPolicy {
	policy.CostClass = CostClass(strings.TrimSpace(string(policy.CostClass)))
	if policy.CostClass == "" {
		policy.CostClass = CostNone
	}
	policy.SideEffectClass = SideEffectClass(strings.TrimSpace(string(policy.SideEffectClass)))
	if policy.SideEffectClass == "" {
		policy.SideEffectClass = SideEffectNone
	}
	permissions := make([]string, 0, len(policy.RequiredPermissions))
	seen := make(map[string]struct{}, len(policy.RequiredPermissions))
	for _, permission := range policy.RequiredPermissions {
		permission = strings.TrimSpace(permission)
		if permission == "" {
			continue
		}
		if _, duplicate := seen[permission]; duplicate {
			continue
		}
		seen[permission] = struct{}{}
		permissions = append(permissions, permission)
	}
	sort.Strings(permissions)
	policy.RequiredPermissions = permissions
	return policy
}

func cloneDefinitionPolicy(policy DefinitionPolicy) DefinitionPolicy {
	policy.RequiredPermissions = append([]string(nil), policy.RequiredPermissions...)
	return policy
}

func validDefinitionPolicy(policy DefinitionPolicy) bool {
	validCost := policy.CostClass == CostNone || policy.CostClass == CostLow ||
		policy.CostClass == CostMedium || policy.CostClass == CostHigh || policy.CostClass == CostExternal
	validEffect := policy.SideEffectClass == SideEffectNone || policy.SideEffectClass == SideEffectRead ||
		policy.SideEffectClass == SideEffectWrite || policy.SideEffectClass == SideEffectDestructive
	return validCost && validEffect && policy.MaxCostUnits >= 0
}

func normalizeJSON(value json.RawMessage) json.RawMessage {
	if len(value) == 0 {
		return nil
	}
	if !json.Valid(value) {
		return cloneJSON(value)
	}
	decoder := json.NewDecoder(bytes.NewReader(value))
	decoder.UseNumber()
	var decoded any
	if err := decoder.Decode(&decoded); err != nil {
		return cloneJSON(value)
	}
	canonical, err := json.Marshal(decoded)
	if err != nil {
		return cloneJSON(value)
	}
	return canonical
}
