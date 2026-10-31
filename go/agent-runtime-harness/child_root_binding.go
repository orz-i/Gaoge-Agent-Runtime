package harness

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
)

// childRootKey is reserved for Harness-owned, immutable child-admission
// authority inside the existing hashed Invocation.Input. No separate SQL
// column, sidecar Item or post-crash mutable lookup can supply this proof.
const childRootKey = "harnessRoot"

type childRootBinding struct {
	InvocationID   string `json:"invocationID"`
	ExecutionRefID string `json:"executionRefID"`
	Attempt        int    `json:"attempt"`
}

func sealChildRootInput(input json.RawMessage, inputHash string, root Invocation) (json.RawMessage, string, error) {
	if len(input) == 0 || hashInvocationBytes(input) != inputHash ||
		strings.TrimSpace(root.ID) == "" || strings.TrimSpace(root.ExecutionRefID) == "" ||
		root.Attempt <= 0 || strings.TrimSpace(root.ParentItemID) != "" ||
		!validExecutionClass(root.ExecutionClass) || !validInvocationStatus(root.Status) ||
		terminalInvocationStatus(root.Status) {
		return nil, "", ErrConflict
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(input, &fields); err != nil || fields == nil {
		return nil, "", ErrConflict
	}
	if _, reserved := fields[childRootKey]; reserved {
		return nil, "", ErrConflict
	}
	binding, err := json.Marshal(childRootBinding{
		InvocationID: root.ID, ExecutionRefID: root.ExecutionRefID, Attempt: root.Attempt,
	})
	if err != nil {
		return nil, "", err
	}
	fields[childRootKey] = binding
	encoded, err := json.Marshal(fields)
	if err != nil {
		return nil, "", err
	}
	return encoded, hashInvocationBytes(encoded), nil
}

func frozenChildRoot(invocation Invocation) (childRootBinding, error) {
	if strings.TrimSpace(invocation.ParentItemID) == "" || len(invocation.Input) == 0 ||
		hashInvocationBytes(invocation.Input) != invocation.InputHash {
		return childRootBinding{}, ErrConflict
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(invocation.Input, &fields); err != nil {
		return childRootBinding{}, ErrConflict
	}
	raw := fields[childRootKey]
	if len(raw) == 0 {
		return childRootBinding{}, ErrConflict
	}
	var binding childRootBinding
	if err := json.Unmarshal(raw, &binding); err != nil ||
		strings.TrimSpace(binding.InvocationID) == "" ||
		strings.TrimSpace(binding.ExecutionRefID) == "" || binding.Attempt <= 0 {
		return childRootBinding{}, ErrConflict
	}
	return binding, nil
}

// loadFrozenChildRoot uses the exact indexed Invocation ID committed with the
// child's first admission. A later root Retry rotates ExecutionRefID/Attempt;
// it cannot retroactively confer authority to this already-accepted child.
func (runner *Runner) loadFrozenChildRoot(ctx context.Context, child Invocation) (Invocation, error) {
	binding, err := frozenChildRoot(child)
	if err != nil {
		return Invocation{}, err
	}
	root, err := runner.store.GetInvocation(ctx, binding.InvocationID)
	if err != nil {
		return Invocation{}, errors.Join(ErrConflict, err)
	}
	if root.ID != binding.InvocationID || root.ID == child.ID ||
		root.TurnID != child.TurnID || strings.TrimSpace(root.ParentItemID) != "" ||
		root.ExecutionRefID != binding.ExecutionRefID || root.Attempt != binding.Attempt ||
		!validExecutionClass(root.ExecutionClass) || !validInvocationStatus(root.Status) ||
		terminalInvocationStatus(root.Status) {
		return Invocation{}, ErrConflict
	}
	return root, nil
}
