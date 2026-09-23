package harness

import (
	"encoding/json"
	"slices"
	"strings"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
)

// RoleSnapshot freezes one environment-authorized delegation target. Host-owned
// IDs and revisions are opaque; credentials and mutable catalog lookups are not
// part of delegation execution. MemberID/MemberRevision optionally route the
// role to an external child runner while keeping one roleID-based model contract.
type RoleSnapshot struct {
	ID             string          `json:"id"`
	Revision       uint64          `json:"revision"`
	Name           string          `json:"name"`
	Description    string          `json:"description,omitempty"`
	Instructions   string          `json:"instructions,omitempty"`
	MemberID       string          `json:"memberID,omitempty"`
	MemberRevision string          `json:"memberRevision,omitempty"`
	Model          string          `json:"model,omitempty"`
	ModelOptions   json.RawMessage `json:"modelOptions,omitempty"`
	ToolKeys       []string        `json:"toolKeys"`
	Skills         []SkillSnapshot `json:"skills"`
	Limits         budget.Limits   `json:"limits"`
}

func normalizeRoleSnapshots(values []RoleSnapshot, parentTools []string) ([]RoleSnapshot, error) {
	if values == nil {
		return nil, nil
	}
	result := cloneRoleSnapshots(values)
	seen := map[string]bool{}
	for index := range result {
		value := &result[index]
		value.ID, value.Name = strings.TrimSpace(value.ID), strings.TrimSpace(value.Name)
		value.MemberID, value.MemberRevision = strings.TrimSpace(value.MemberID), strings.TrimSpace(value.MemberRevision)
		value.Model = strings.TrimSpace(value.Model)
		if value.ID == "" || len(value.ID) > 160 || value.Revision == 0 || value.Name == "" || seen[value.ID] ||
			len(value.MemberID) > 160 || len(value.MemberRevision) > 256 || value.MemberRevision != "" && value.MemberID == "" || !budget.ValidLimits(value.Limits) {
			return nil, ErrInvalidRequest
		}
		seen[value.ID] = true
		var err error
		value.ModelOptions, err = canonicalJSON(value.ModelOptions)
		if err != nil {
			return nil, err
		}
		value.Skills, err = normalizeSkillSnapshots(value.Skills)
		if err != nil {
			return nil, err
		}
		value.ToolKeys = normalizeStrings(value.ToolKeys)
		for _, key := range value.ToolKeys {
			if !slices.Contains(parentTools, key) {
				return nil, ErrInvalidRequest
			}
		}
	}
	slices.SortFunc(result, func(left, right RoleSnapshot) int { return strings.Compare(left.ID, right.ID) })
	return result, nil
}

func cloneRoleSnapshots(values []RoleSnapshot) []RoleSnapshot {
	if values == nil {
		return nil
	}
	result := append([]RoleSnapshot{}, values...)
	for index := range result {
		result[index].ModelOptions = append(json.RawMessage(nil), result[index].ModelOptions...)
		result[index].ToolKeys = append([]string{}, result[index].ToolKeys...)
		result[index].Skills = append([]SkillSnapshot{}, result[index].Skills...)
	}
	return result
}
