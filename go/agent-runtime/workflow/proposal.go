package workflow

import (
	"encoding/json"
	"errors"
	"sort"
)

// DefinitionDiff is a deterministic structural summary between two immutable revisions.
type DefinitionDiff struct {
	AddedNodeIDs        []string `json:"addedNodeIDs,omitempty"`
	RemovedNodeIDs      []string `json:"removedNodeIDs,omitempty"`
	ChangedNodeIDs      []string `json:"changedNodeIDs,omitempty"`
	InputSchemaChanged  bool     `json:"inputSchemaChanged"`
	OutputSchemaChanged bool     `json:"outputSchemaChanged"`
	LimitsChanged       bool     `json:"limitsChanged"`
}

// DefinitionPolicyImpact summarizes permission, cost and side-effect changes before publish.
type DefinitionPolicyImpact struct {
	PermissionsAdded   []string        `json:"permissionsAdded,omitempty"`
	PermissionsRemoved []string        `json:"permissionsRemoved,omitempty"`
	CostClassFrom      CostClass       `json:"costClassFrom"`
	CostClassTo        CostClass       `json:"costClassTo"`
	MaxCostUnitsDelta  int64           `json:"maxCostUnitsDelta"`
	SideEffectFrom     SideEffectClass `json:"sideEffectFrom"`
	SideEffectTo       SideEffectClass `json:"sideEffectTo"`
}

// DefinitionProposalReport is the pure compile/diff result presented for approval.
type DefinitionProposalReport struct {
	DefinitionID string                 `json:"definitionID"`
	BaseRevision int                    `json:"baseRevision"`
	BaseHash     string                 `json:"baseHash,omitempty"`
	Candidate    Definition             `json:"candidate"`
	Diff         DefinitionDiff         `json:"diff"`
	Impact       DefinitionPolicyImpact `json:"impact"`
}

// CompileDefinitionProposal compiles a candidate without mutating a Registry or active run.
func CompileDefinitionProposal(
	base *Definition,
	draft DefinitionDraft,
) (DefinitionProposalReport, error) {
	baseRevision := 0
	baseHash := ""
	baseDefinition := Definition{Policy: normalizeDefinitionPolicy(DefinitionPolicy{})}
	if base != nil {
		baseDefinition = cloneDefinition(*base)
		if err := ValidateDefinition(baseDefinition); err != nil {
			return DefinitionProposalReport{}, errors.Join(ErrInvalidDefinition, err)
		}
		if draft.ID != "" && draft.ID != baseDefinition.ID {
			return DefinitionProposalReport{}, ErrInvalidDefinition
		}
		draft.ID = baseDefinition.ID
		baseRevision = baseDefinition.Revision
		baseHash = baseDefinition.Hash
	}
	draft.Revision = baseRevision + 1
	candidate, err := CompileDefinition(draft)
	if err != nil {
		return DefinitionProposalReport{}, err
	}
	return DefinitionProposalReport{
		DefinitionID: candidate.ID,
		BaseRevision: baseRevision,
		BaseHash:     baseHash,
		Candidate:    candidate,
		Diff:         DiffDefinitions(baseDefinition, candidate),
		Impact:       DiffDefinitionPolicy(baseDefinition.Policy, candidate.Policy),
	}, nil
}

// DiffDefinitions compares canonical schemas, limits and node payloads by stable node ID.
func DiffDefinitions(base Definition, candidate Definition) DefinitionDiff {
	baseNodes := nodesByID(base.Nodes)
	candidateNodes := nodesByID(candidate.Nodes)
	diff := DefinitionDiff{
		InputSchemaChanged:  string(normalizeJSON(base.InputSchema)) != string(normalizeJSON(candidate.InputSchema)),
		OutputSchemaChanged: string(normalizeJSON(base.OutputSchema)) != string(normalizeJSON(candidate.OutputSchema)),
		LimitsChanged:       base.Limits != candidate.Limits,
	}
	for id, node := range candidateNodes {
		baseNode, exists := baseNodes[id]
		if !exists {
			diff.AddedNodeIDs = append(diff.AddedNodeIDs, id)
			continue
		}
		if canonicalNode(node) != canonicalNode(baseNode) {
			diff.ChangedNodeIDs = append(diff.ChangedNodeIDs, id)
		}
	}
	for id := range baseNodes {
		if _, exists := candidateNodes[id]; !exists {
			diff.RemovedNodeIDs = append(diff.RemovedNodeIDs, id)
		}
	}
	sort.Strings(diff.AddedNodeIDs)
	sort.Strings(diff.RemovedNodeIDs)
	sort.Strings(diff.ChangedNodeIDs)
	return diff
}

// DiffDefinitionPolicy computes the publish-time permission/cost impact.
func DiffDefinitionPolicy(base DefinitionPolicy, candidate DefinitionPolicy) DefinitionPolicyImpact {
	base = normalizeDefinitionPolicy(base)
	candidate = normalizeDefinitionPolicy(candidate)
	return DefinitionPolicyImpact{
		PermissionsAdded:   setDifference(candidate.RequiredPermissions, base.RequiredPermissions),
		PermissionsRemoved: setDifference(base.RequiredPermissions, candidate.RequiredPermissions),
		CostClassFrom:      base.CostClass,
		CostClassTo:        candidate.CostClass,
		MaxCostUnitsDelta:  candidate.MaxCostUnits - base.MaxCostUnits,
		SideEffectFrom:     base.SideEffectClass,
		SideEffectTo:       candidate.SideEffectClass,
	}
}

func nodesByID(nodes []Node) map[string]Node {
	result := make(map[string]Node, len(nodes))
	for _, node := range nodes {
		result[node.ID] = normalizeNode(node)
	}
	return result
}

func canonicalNode(node Node) string {
	encoded, _ := json.Marshal(normalizeNode(node))
	return string(encoded)
}

func setDifference(left, right []string) []string {
	rightSet := make(map[string]struct{}, len(right))
	for _, item := range right {
		rightSet[item] = struct{}{}
	}
	result := make([]string, 0)
	for _, item := range left {
		if _, exists := rightSet[item]; !exists {
			result = append(result, item)
		}
	}
	sort.Strings(result)
	return result
}
