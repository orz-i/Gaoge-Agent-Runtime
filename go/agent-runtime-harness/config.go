package harness

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/agent"
	runtimecontext "github.com/orz-i/Gaoge/sdk/go/agent-runtime/context"
)

// VersionRef freezes one host-owned configuration resource identity and revision.
type VersionRef struct {
	ID       string `json:"id"`
	Revision uint64 `json:"revision"`
}

// ConfigSnapshot is one immutable execution configuration. It intentionally excludes secrets and transport endpoints.
type ConfigSnapshot struct {
	ID                    string                `json:"id"`
	TurnID                string                `json:"turnID"`
	Environment           VersionRef            `json:"environment"`
	Instructions          string                `json:"instructions,omitempty"`
	Model                 string                `json:"model,omitempty"`
	ModelOptions          json.RawMessage       `json:"modelOptions,omitempty"`
	ToolKeys              []string              `json:"toolKeys"`
	ToolPolicies          []VersionRef          `json:"toolPolicies"`
	Skills                []VersionRef          `json:"skills"`
	MemoryPolicy          string                `json:"memoryPolicy,omitempty"`
	ContextBudget         runtimecontext.Budget `json:"contextBudget"`
	ApprovalPolicyVersion uint64                `json:"approvalPolicyVersion"`
	Limits                agent.Limits          `json:"limits"`
	ContentHash           string                `json:"contentHash"`
	CreatedAt             time.Time             `json:"createdAt"`
}

type configPayload struct {
	TurnID                string                `json:"turnID"`
	Environment           VersionRef            `json:"environment"`
	Instructions          string                `json:"instructions,omitempty"`
	Model                 string                `json:"model,omitempty"`
	ModelOptions          json.RawMessage       `json:"modelOptions,omitempty"`
	ToolKeys              []string              `json:"toolKeys"`
	ToolPolicies          []VersionRef          `json:"toolPolicies"`
	Skills                []VersionRef          `json:"skills"`
	MemoryPolicy          string                `json:"memoryPolicy,omitempty"`
	ContextBudget         runtimecontext.Budget `json:"contextBudget"`
	ApprovalPolicyVersion uint64                `json:"approvalPolicyVersion"`
	Limits                agent.Limits          `json:"limits"`
}

// SealConfigSnapshot normalizes, hashes and seals one immutable configuration for a Harness Turn.
func SealConfigSnapshot(turnID string, value ConfigSnapshot, now time.Time) (ConfigSnapshot, error) {
	turnID = strings.TrimSpace(turnID)
	if turnID == "" || now.IsZero() {
		return ConfigSnapshot{}, ErrInvalidRequest
	}
	value.TurnID = turnID
	value.Environment.ID = strings.TrimSpace(value.Environment.ID)
	value.Instructions = strings.TrimSpace(value.Instructions)
	value.Model = strings.TrimSpace(value.Model)
	value.MemoryPolicy = strings.TrimSpace(value.MemoryPolicy)
	if (value.Environment.ID == "") != (value.Environment.Revision == 0) {
		return ConfigSnapshot{}, ErrInvalidRequest
	}
	var err error
	value.ModelOptions, err = canonicalJSON(value.ModelOptions)
	if err != nil {
		return ConfigSnapshot{}, err
	}
	value.ToolKeys = normalizeStrings(value.ToolKeys)
	value.ToolPolicies, err = normalizeVersionRefs(value.ToolPolicies)
	if err != nil {
		return ConfigSnapshot{}, err
	}
	value.Skills, err = normalizeVersionRefs(value.Skills)
	if err != nil {
		return ConfigSnapshot{}, err
	}
	payload := configPayload{
		TurnID: turnID, Environment: value.Environment, Instructions: value.Instructions,
		Model: value.Model, ModelOptions: value.ModelOptions, ToolKeys: value.ToolKeys,
		ToolPolicies: value.ToolPolicies, Skills: value.Skills, MemoryPolicy: value.MemoryPolicy,
		ContextBudget: value.ContextBudget, ApprovalPolicyVersion: value.ApprovalPolicyVersion, Limits: value.Limits,
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

func normalizeVersionRefs(values []VersionRef) ([]VersionRef, error) {
	result := make([]VersionRef, len(values))
	seen := make(map[string]struct{}, len(values))
	for index, value := range values {
		value.ID = strings.TrimSpace(value.ID)
		if value.ID == "" || value.Revision == 0 {
			return nil, ErrInvalidRequest
		}
		key := value.ID + "#" + strconv.FormatUint(value.Revision, 10)
		if _, ok := seen[key]; ok {
			return nil, ErrInvalidRequest
		}
		seen[key] = struct{}{}
		result[index] = value
	}
	slices.SortFunc(result, func(left, right VersionRef) int {
		if compare := strings.Compare(left.ID, right.ID); compare != 0 {
			return compare
		}
		if left.Revision < right.Revision {
			return -1
		}
		if left.Revision > right.Revision {
			return 1
		}
		return 0
	})
	return result, nil
}

func cloneConfigSnapshot(value ConfigSnapshot) ConfigSnapshot {
	value.ModelOptions = append(json.RawMessage(nil), value.ModelOptions...)
	value.ToolKeys = append([]string(nil), value.ToolKeys...)
	value.ToolPolicies = append([]VersionRef(nil), value.ToolPolicies...)
	value.Skills = append([]VersionRef(nil), value.Skills...)
	return value
}

func defaultJSON(value json.RawMessage) json.RawMessage {
	if len(bytes.TrimSpace(value)) == 0 {
		return json.RawMessage(`{}`)
	}
	return value
}
