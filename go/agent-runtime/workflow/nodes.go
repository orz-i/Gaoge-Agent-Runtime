package workflow

import (
	"encoding/json"
	"sort"
	"strings"
)

// ValueSourceKind is one pure-data input source available to the interpreter.
type ValueSourceKind string

const (
	ValueLiteral       ValueSourceKind = "literal"
	ValueWorkflowInput ValueSourceKind = "workflow_input"
	ValueNodeOutput    ValueSourceKind = "node_output"
	ValueWaitResponse  ValueSourceKind = "wait_response"
	ValueMapItem       ValueSourceKind = "map_item"
	ValueMapIndex      ValueSourceKind = "map_index"
)

// ValueSource selects JSON without executing code. Pointer is an RFC 6901 JSON Pointer.
type ValueSource struct {
	Kind    ValueSourceKind `json:"kind"`
	Value   json.RawMessage `json:"value,omitempty"`
	NodeID  string          `json:"nodeID,omitempty"`
	Pointer string          `json:"pointer,omitempty"`
}

// EffectClass selects one statically composed executor class.
type EffectClass string

const (
	EffectClassGeneric      EffectClass = "effect"
	EffectClassAgent        EffectClass = "agent.task"
	EffectClassApplication  EffectClass = "application.effect"
	EffectClassMedia        EffectClass = "media.effect"
	EffectClassSubworkflow  EffectClass = "subworkflow"
	EffectClassCompensation EffectClass = "compensation"
)

// RetryPolicy bounds retries of one stable, idempotent Effect intent.
type RetryPolicy struct {
	MaxAttempts         int      `json:"maxAttempts"`
	RetryableErrorCodes []string `json:"retryableErrorCodes,omitempty"`
}

// EffectCall is one closed, data-only external call description.
type EffectCall struct {
	Class        EffectClass          `json:"class"`
	Kind         string               `json:"kind"`
	Revision     string               `json:"revision,omitempty"`
	Input        ValueSource          `json:"input"`
	Definition   *DefinitionReference `json:"definition,omitempty"`
	MaxCostUnits int64                `json:"maxCostUnits"`
	Retry        RetryPolicy          `json:"retry"`
}

// AgentTaskNode delegates one bounded creative task.
type AgentTaskNode struct {
	AgentKey     string      `json:"agentKey"`
	Revision     string      `json:"revision"`
	Input        ValueSource `json:"input"`
	MaxCostUnits int64       `json:"maxCostUnits"`
	Retry        RetryPolicy `json:"retry"`
}

// ApplicationEffectNode calls one exact application capability revision.
type ApplicationEffectNode struct {
	CapabilityKey string      `json:"capabilityKey"`
	Revision      string      `json:"revision"`
	Input         ValueSource `json:"input"`
	MaxCostUnits  int64       `json:"maxCostUnits"`
	Retry         RetryPolicy `json:"retry"`
}

// MediaEffectNode calls one exact provider-neutral media capability revision.
type MediaEffectNode struct {
	CapabilityKey string      `json:"capabilityKey"`
	Revision      string      `json:"revision"`
	Input         ValueSource `json:"input"`
	MaxCostUnits  int64       `json:"maxCostUnits"`
	Retry         RetryPolicy `json:"retry"`
}

// IfNode chooses one forward-only target from a boolean ValueSource.
type IfNode struct {
	Condition  ValueSource `json:"condition"`
	ThenNodeID string      `json:"thenNodeID"`
	ElseNodeID string      `json:"elseNodeID"`
}

// ParallelBranch is one named effect lane inside a bounded barrier.
type ParallelBranch struct {
	ID   string     `json:"id"`
	Call EffectCall `json:"call"`
}

// ParallelNode dispatches bounded independent calls and emits definition-order results.
type ParallelNode struct {
	Branches       []ParallelBranch `json:"branches"`
	MaxConcurrency int              `json:"maxConcurrency"`
}

// MapNode applies one bounded call to each item and preserves input order.
type MapNode struct {
	Items          ValueSource `json:"items"`
	Call           EffectCall  `json:"call"`
	MaxConcurrency int         `json:"maxConcurrency"`
}

// SubworkflowNode starts one exact immutable child Workflow revision.
type SubworkflowNode struct {
	Definition   DefinitionReference `json:"definition"`
	Input        ValueSource         `json:"input"`
	MaxCostUnits int64               `json:"maxCostUnits"`
	Retry        RetryPolicy         `json:"retry"`
}

// CompensationNode registers Undo only after Do completes successfully.
type CompensationNode struct {
	Do   EffectCall `json:"do"`
	Undo EffectCall `json:"undo"`
}

func normalizeAdvancedNode(node Node) Node {
	if node.AgentTask != nil {
		value := *node.AgentTask
		value.AgentKey = strings.TrimSpace(value.AgentKey)
		value.Revision = strings.TrimSpace(value.Revision)
		value.Input = normalizeValueSource(value.Input)
		value.Retry = normalizeRetryPolicy(value.Retry)
		node.AgentTask = &value
	}
	if node.ApplicationEffect != nil {
		value := *node.ApplicationEffect
		value.CapabilityKey = strings.TrimSpace(value.CapabilityKey)
		value.Revision = strings.TrimSpace(value.Revision)
		value.Input = normalizeValueSource(value.Input)
		value.Retry = normalizeRetryPolicy(value.Retry)
		node.ApplicationEffect = &value
	}
	if node.MediaEffect != nil {
		value := *node.MediaEffect
		value.CapabilityKey = strings.TrimSpace(value.CapabilityKey)
		value.Revision = strings.TrimSpace(value.Revision)
		value.Input = normalizeValueSource(value.Input)
		value.Retry = normalizeRetryPolicy(value.Retry)
		node.MediaEffect = &value
	}
	if node.If != nil {
		value := *node.If
		value.Condition = normalizeValueSource(value.Condition)
		value.ThenNodeID = strings.TrimSpace(value.ThenNodeID)
		value.ElseNodeID = strings.TrimSpace(value.ElseNodeID)
		node.If = &value
	}
	if node.Parallel != nil {
		value := *node.Parallel
		value.Branches = append([]ParallelBranch(nil), value.Branches...)
		for index := range value.Branches {
			value.Branches[index].ID = strings.TrimSpace(value.Branches[index].ID)
			value.Branches[index].Call = normalizeEffectCall(value.Branches[index].Call)
		}
		node.Parallel = &value
	}
	if node.Map != nil {
		value := *node.Map
		value.Items = normalizeValueSource(value.Items)
		value.Call = normalizeEffectCall(value.Call)
		node.Map = &value
	}
	if node.Subworkflow != nil {
		value := *node.Subworkflow
		value.Definition.ID = strings.TrimSpace(value.Definition.ID)
		value.Definition.Hash = strings.TrimSpace(value.Definition.Hash)
		value.Input = normalizeValueSource(value.Input)
		value.Retry = normalizeRetryPolicy(value.Retry)
		node.Subworkflow = &value
	}
	if node.Compensation != nil {
		value := *node.Compensation
		value.Do = normalizeEffectCall(value.Do)
		value.Undo = normalizeEffectCall(value.Undo)
		node.Compensation = &value
	}
	return node
}

func normalizeEffectCall(call EffectCall) EffectCall {
	call.Class = EffectClass(strings.TrimSpace(string(call.Class)))
	call.Kind = strings.TrimSpace(call.Kind)
	call.Revision = strings.TrimSpace(call.Revision)
	call.Input = normalizeValueSource(call.Input)
	call.Retry = normalizeRetryPolicy(call.Retry)
	if call.Definition != nil {
		reference := *call.Definition
		reference.ID = strings.TrimSpace(reference.ID)
		reference.Hash = strings.TrimSpace(reference.Hash)
		call.Definition = &reference
	}
	return call
}

func normalizeRetryPolicy(policy RetryPolicy) RetryPolicy {
	if policy.MaxAttempts <= 0 {
		policy.MaxAttempts = 1
	}
	codes := make([]string, 0, len(policy.RetryableErrorCodes))
	seen := make(map[string]struct{}, len(policy.RetryableErrorCodes))
	for _, code := range policy.RetryableErrorCodes {
		code = strings.TrimSpace(code)
		if code == "" {
			continue
		}
		if _, duplicate := seen[code]; duplicate {
			continue
		}
		seen[code] = struct{}{}
		codes = append(codes, code)
	}
	sort.Strings(codes)
	policy.RetryableErrorCodes = codes
	return policy
}

func normalizeValueSource(source ValueSource) ValueSource {
	source.Kind = ValueSourceKind(strings.TrimSpace(string(source.Kind)))
	source.Value = normalizeJSON(source.Value)
	source.NodeID = strings.TrimSpace(source.NodeID)
	source.Pointer = strings.TrimSpace(source.Pointer)
	return source
}

func cloneValueSource(source *ValueSource) *ValueSource {
	if source == nil {
		return nil
	}
	cloned := normalizeValueSource(*source)
	return &cloned
}

func validateDefinitionNodes(nodes []Node, limits Limits) error {
	indices := make(map[string]int, len(nodes))
	returnCount := 0
	for index, node := range nodes {
		if node.ID == "" {
			return ErrInvalidDefinition
		}
		if _, duplicate := indices[node.ID]; duplicate {
			return ErrInvalidDefinition
		}
		indices[node.ID] = index
		if node.Type == NodeReturn {
			returnCount++
		}
	}
	if returnCount != 1 || nodes[len(nodes)-1].Type != NodeReturn {
		return ErrInvalidDefinition
	}
	for index, node := range nodes {
		if !validNode(node, index, indices, limits) {
			return ErrInvalidDefinition
		}
	}
	return nil
}

func validNode(node Node, index int, indices map[string]int, limits Limits) bool {
	last := index == len(indices)-1
	if !validForwardTarget(node.Next, index, indices, true) || nodePayloadCount(node) != 1 {
		return false
	}
	switch node.Type {
	case NodeEffect:
		return !last && validEffectNode(node.Effect, index, indices, limits)
	case NodeAgentTask:
		return !last && node.AgentTask != nil && node.AgentTask.AgentKey != "" &&
			node.AgentTask.Revision != "" && node.AgentTask.MaxCostUnits >= 0 &&
			validRetryPolicy(node.AgentTask.Retry, limits) &&
			validValueSource(node.AgentTask.Input, index, indices, false, false)
	case NodeApplicationEffect:
		return !last && node.ApplicationEffect != nil && node.ApplicationEffect.CapabilityKey != "" &&
			node.ApplicationEffect.Revision != "" && node.ApplicationEffect.MaxCostUnits >= 0 &&
			validRetryPolicy(node.ApplicationEffect.Retry, limits) &&
			validValueSource(node.ApplicationEffect.Input, index, indices, false, false)
	case NodeMediaEffect:
		return !last && node.MediaEffect != nil && node.MediaEffect.CapabilityKey != "" &&
			node.MediaEffect.Revision != "" && node.MediaEffect.MaxCostUnits >= 0 &&
			validRetryPolicy(node.MediaEffect.Retry, limits) &&
			validValueSource(node.MediaEffect.Input, index, indices, false, false)
	case NodeWait:
		return !last && validWaitNode(node.Wait, index, indices)
	case NodeIf:
		return !last && node.Next == "" && node.If != nil &&
			validValueSource(node.If.Condition, index, indices, false, false) &&
			validForwardTarget(node.If.ThenNodeID, index, indices, false) &&
			validForwardTarget(node.If.ElseNodeID, index, indices, false)
	case NodeParallel:
		return !last && validParallelNode(node.Parallel, index, indices, limits)
	case NodeMap:
		return !last && validMapNode(node.Map, index, indices, limits)
	case NodeSubworkflow:
		return !last && validSubworkflowNode(node.Subworkflow, index, indices, limits)
	case NodeCompensation:
		return !last && validCompensationNode(node.Compensation, node.ID, index, indices, limits)
	case NodeReturn:
		return last && node.Next == "" && validReturnNode(node.Return, index, indices)
	default:
		return false
	}
}

func nodePayloadCount(node Node) int {
	count := 0
	for _, present := range []bool{
		node.Effect != nil, node.AgentTask != nil, node.ApplicationEffect != nil,
		node.MediaEffect != nil, node.Wait != nil, node.If != nil, node.Parallel != nil,
		node.Map != nil, node.Subworkflow != nil, node.Compensation != nil, node.Return != nil,
	} {
		if present {
			count++
		}
	}
	return count
}

func validEffectNode(node *EffectNode, index int, indices map[string]int, limits Limits) bool {
	if node == nil || node.Kind == "" || node.MaxCostUnits < 0 || !validRetryPolicy(node.Retry, limits) {
		return false
	}
	hasInput := len(node.Input) > 0 && json.Valid(node.Input)
	hasSource := node.Source != nil && validValueSource(*node.Source, index, indices, false, false)
	count := 0
	for _, present := range []bool{hasInput, node.FromInput, hasSource} {
		if present {
			count++
		}
	}
	return count == 1
}

func validWaitNode(node *WaitNode, index int, indices map[string]int) bool {
	if node == nil || node.Kind == "" {
		return false
	}
	hasPayload := len(node.Payload) > 0 && json.Valid(node.Payload)
	hasSource := node.Source != nil && validValueSource(*node.Source, index, indices, false, false)
	return hasPayload != hasSource
}

func validReturnNode(node *ReturnNode, index int, indices map[string]int) bool {
	if node == nil {
		return false
	}
	hasValue := len(node.Value) > 0 && json.Valid(node.Value)
	hasFromNode := node.FromNode != "" && indices[node.FromNode] < index
	hasSource := node.Source != nil && validValueSource(*node.Source, index, indices, false, false)
	count := 0
	for _, present := range []bool{hasValue, hasFromNode, hasSource} {
		if present {
			count++
		}
	}
	return count == 1
}

func validParallelNode(node *ParallelNode, index int, indices map[string]int, limits Limits) bool {
	if node == nil || len(node.Branches) == 0 || len(node.Branches) > limits.MaxFanOut ||
		node.MaxConcurrency <= 0 || node.MaxConcurrency > limits.MaxConcurrency ||
		node.MaxConcurrency > len(node.Branches) {
		return false
	}
	seen := make(map[string]struct{}, len(node.Branches))
	for _, branch := range node.Branches {
		if branch.ID == "" || !validEffectCall(branch.Call, index, indices, false, false, limits) {
			return false
		}
		if _, duplicate := seen[branch.ID]; duplicate {
			return false
		}
		seen[branch.ID] = struct{}{}
	}
	return true
}

func validMapNode(node *MapNode, index int, indices map[string]int, limits Limits) bool {
	return node != nil && node.MaxConcurrency > 0 && node.MaxConcurrency <= limits.MaxConcurrency &&
		validValueSource(node.Items, index, indices, false, false) &&
		validEffectCall(node.Call, index, indices, true, false, limits)
}

func validSubworkflowNode(node *SubworkflowNode, index int, indices map[string]int, limits Limits) bool {
	return node != nil && node.MaxCostUnits >= 0 && validDefinitionReference(node.Definition) &&
		validRetryPolicy(node.Retry, limits) &&
		validValueSource(node.Input, index, indices, false, false)
}

func validCompensationNode(
	node *CompensationNode,
	nodeID string,
	index int,
	indices map[string]int,
	limits Limits,
) bool {
	if node == nil || !validEffectCall(node.Do, index, indices, false, false, limits) ||
		node.Do.Class == EffectClassCompensation || node.Do.Class == EffectClassSubworkflow {
		return false
	}
	return validEffectCall(node.Undo, index, indices, false, true, limits) &&
		node.Undo.Class != EffectClassAgent && node.Undo.Class != EffectClassCompensation &&
		(node.Undo.Input.NodeID == "" || node.Undo.Input.NodeID == nodeID || indices[node.Undo.Input.NodeID] < index)
}

func validEffectCall(
	call EffectCall,
	index int,
	indices map[string]int,
	allowMap bool,
	allowCurrent bool,
	limits Limits,
) bool {
	if call.Kind == "" || call.MaxCostUnits < 0 ||
		!validRetryPolicy(call.Retry, limits) ||
		!validValueSource(call.Input, index, indices, allowMap, allowCurrent) {
		return false
	}
	switch call.Class {
	case EffectClassGeneric, EffectClassCompensation:
		return call.Revision == "" && call.Definition == nil
	case EffectClassAgent, EffectClassApplication, EffectClassMedia:
		return call.Revision != "" && call.Definition == nil
	case EffectClassSubworkflow:
		return call.Revision == "" && call.Definition != nil && validDefinitionReference(*call.Definition)
	default:
		return false
	}
}

func validRetryPolicy(policy RetryPolicy, limits Limits) bool {
	return policy.MaxAttempts > 0 && policy.MaxAttempts <= limits.MaxAttemptsPerEffect &&
		(policy.MaxAttempts == 1 || len(policy.RetryableErrorCodes) > 0)
}

func validDefinitionReference(reference DefinitionReference) bool {
	return reference.ID != "" && reference.Revision > 0 && reference.Hash != ""
}

func validValueSource(
	source ValueSource,
	index int,
	indices map[string]int,
	allowMap bool,
	allowCurrent bool,
) bool {
	if source.Pointer != "" && !strings.HasPrefix(source.Pointer, "/") {
		return false
	}
	switch source.Kind {
	case ValueLiteral:
		return len(source.Value) > 0 && json.Valid(source.Value) && source.NodeID == ""
	case ValueWorkflowInput:
		return len(source.Value) == 0 && source.NodeID == ""
	case ValueNodeOutput, ValueWaitResponse:
		target, exists := indices[source.NodeID]
		return exists && len(source.Value) == 0 && (target < index || allowCurrent && target == index)
	case ValueMapItem, ValueMapIndex:
		return allowMap && len(source.Value) == 0 && source.NodeID == ""
	default:
		return false
	}
}

func validForwardTarget(target string, index int, indices map[string]int, optional bool) bool {
	if target == "" {
		return optional
	}
	targetIndex, exists := indices[target]
	return exists && targetIndex > index
}
