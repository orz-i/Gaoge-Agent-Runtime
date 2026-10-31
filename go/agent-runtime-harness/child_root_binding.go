package harness

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
)

// childRootKey reserves the immutable root and ancestor Attempt authority
// inside the existing hashed Invocation.Input. No separate table or mutable
// replay lookup may reconstruct an ancestor's former execution identity.
const childRootKey = "harnessRoot"

type childAttemptBinding struct {
	InvocationID   string `json:"invocationID"`
	ExecutionRefID string `json:"executionRefID"`
	Attempt        int    `json:"attempt"`
}

type childRootBinding struct {
	InvocationID   string `json:"invocationID"`
	ExecutionRefID string `json:"executionRefID"`
	Attempt        int    `json:"attempt"`
	// Parents includes every non-root Invocation owner in the frozen parent
	// chain, including inherited owners across Host delegation boundaries.
	Parents []childAttemptBinding `json:"parents,omitempty"`
}

func (binding childRootBinding) attempt() childAttemptBinding {
	return childAttemptBinding{
		InvocationID: binding.InvocationID, ExecutionRefID: binding.ExecutionRefID, Attempt: binding.Attempt,
	}
}

func invocationAttempt(value Invocation) childAttemptBinding {
	return childAttemptBinding{
		InvocationID: value.ID, ExecutionRefID: value.ExecutionRefID, Attempt: value.Attempt,
	}
}

func validChildAttempt(value childAttemptBinding) bool {
	return strings.TrimSpace(value.InvocationID) != "" &&
		strings.TrimSpace(value.ExecutionRefID) != "" && value.Attempt > 0
}

// An Accepted Application has not committed its Host-effect claim. Neither a
// root nor an intermediate Application can authorize descendants before that
// claim; unlike a Kernel-backed Invocation, it has no Run to prove liveness.
func unclaimedApplicationParent(parent Invocation) bool {
	return parent.ExecutionClass == ExecutionApplication && parent.Status == InvocationAccepted
}

func sealChildRootInput(input json.RawMessage, inputHash string, root Invocation, parents ...childAttemptBinding) (json.RawMessage, string, error) {
	if len(input) == 0 || hashInvocationBytes(input) != inputHash ||
		!validChildAttempt(invocationAttempt(root)) || strings.TrimSpace(root.ParentItemID) != "" ||
		!validExecutionClass(root.ExecutionClass) || !validInvocationStatus(root.Status) ||
		terminalInvocationStatus(root.Status) {
		return nil, "", ErrConflict
	}
	seen := map[string]struct{}{root.ID: {}}
	for _, parent := range parents {
		if !validChildAttempt(parent) {
			return nil, "", ErrConflict
		}
		if _, duplicate := seen[parent.InvocationID]; duplicate {
			return nil, "", ErrConflict
		}
		seen[parent.InvocationID] = struct{}{}
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(input, &fields); err != nil || fields == nil {
		return nil, "", ErrConflict
	}
	if _, reserved := fields[childRootKey]; reserved {
		return nil, "", ErrConflict
	}
	binding, err := json.Marshal(childRootBinding{
		InvocationID: root.ID, ExecutionRefID: root.ExecutionRefID, Attempt: root.Attempt, Parents: parents,
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
	if err := json.Unmarshal(raw, &binding); err != nil || !validChildAttempt(binding.attempt()) {
		return childRootBinding{}, ErrConflict
	}
	seen := map[string]struct{}{binding.InvocationID: {}}
	for _, parent := range binding.Parents {
		if !validChildAttempt(parent) {
			return childRootBinding{}, ErrConflict
		}
		if _, duplicate := seen[parent.InvocationID]; duplicate {
			return childRootBinding{}, ErrConflict
		}
		seen[parent.InvocationID] = struct{}{}
	}
	return binding, nil
}

// loadFrozenChildRoot loads the original root by indexed identity. A root
// retry rotates its exact ExecutionRefID/Attempt and cannot rebind children.
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
		invocationAttempt(root) != binding.attempt() ||
		!validExecutionClass(root.ExecutionClass) || !validInvocationStatus(root.Status) ||
		terminalInvocationStatus(root.Status) {
		return Invocation{}, ErrConflict
	}
	return root, nil
}

// requireLiveParentAttempt checks an immutable ancestor ID/Attempt, not merely
// whether its stable Invocation ID happens to be Running after a Retry.
func (runner *Runner) requireLiveParentAttempt(ctx context.Context, turnID string, bound childAttemptBinding) error {
	parent, err := runner.store.GetInvocation(ctx, bound.InvocationID)
	if err != nil {
		return errors.Join(ErrConflict, err)
	}
	if parent.TurnID != turnID || strings.TrimSpace(parent.ParentItemID) == "" ||
		invocationAttempt(parent) != bound || !validExecutionClass(parent.ExecutionClass) ||
		!validInvocationStatus(parent.Status) || terminalInvocationStatus(parent.Status) ||
		unclaimedApplicationParent(parent) {
		return ErrConflict
	}
	if parent.ExecutionClass != ExecutionApplication {
		if runner.runtime == nil {
			return ErrConflict
		}
		snapshot, loadErr := runner.runtime.Load(ctx, parent.ExecutionRefID)
		if loadErr != nil || snapshot.Run.ID != parent.ExecutionRefID ||
			terminalRuntimeStatus(snapshot.Run.Status) {
			return errors.Join(ErrConflict, loadErr)
		}
	}
	return nil
}

// requireItemOwnerAttempt verifies the physical origin recorded in each
// immutable Item, not merely its owner's stable Invocation ID. Retry changes
// the owner's ExecutionRefID while prior Tool/Message/Artifact facts remain.
// Delegation Items are special: RunID names the child, and the parent Run is
// separately frozen in the payload.
func (runner *Runner) requireItemOwnerAttempt(
	ctx context.Context, item Item, owner childAttemptBinding,
) error {
	if item.Kind == ItemDelegation {
		var payload delegationItemPayload
		if err := json.Unmarshal(item.Payload, &payload); err != nil ||
			payload.ChildRunID == "" || payload.ChildRunID != item.RunID ||
			strings.TrimSpace(payload.ParentRunID) == "" {
			return ErrConflict
		}
		return runner.requireRunDescendsFrom(ctx, payload.ParentRunID, owner.ExecutionRefID)
	}
	if item.Kind == ItemInvocation && item.RunID != "" && item.RunID != owner.ExecutionRefID {
		return ErrConflict
	}
	if item.RunID == "" {
		if item.InvocationID != "" {
			return ErrConflict // An owned Item without Run provenance cannot bind to a new Attempt.
		}
		return nil // A genuinely Host-owned Item has no Invocation or physical Run.
	}
	return runner.requireRunDescendsFrom(ctx, item.RunID, owner.ExecutionRefID)
}

// Workflow/Team/GroupChat Items can reference a legitimate nested Runtime
// Run instead of their owning Invocation's top-level Run. The immutable
// child->parent RunRelation index proves the exact originating Attempt;
// a missing index/edge may never silently turn a foreign Run into authority.
func (runner *Runner) requireRunDescendsFrom(ctx context.Context, runID, ownerRunID string) error {
	if strings.TrimSpace(runID) == "" || strings.TrimSpace(ownerRunID) == "" {
		return ErrConflict
	}
	if runID == ownerRunID {
		return nil
	}
	if runner.relationReader == nil {
		return ErrConflict
	}
	seen := make(map[string]struct{})
	for current := runID; current != ownerRunID; {
		if _, repeated := seen[current]; repeated {
			return ErrConflict
		}
		seen[current] = struct{}{}
		relation, err := runner.relationReader.GetByChild(ctx, current)
		if err != nil || relation.ChildRunID != current ||
			strings.TrimSpace(relation.ParentRunID) == "" {
			return errors.Join(ErrConflict, err)
		}
		current = relation.ParentRunID
	}
	return nil
}

// freezeChildParentAttempts traverses only indexed parent Items. Each owned
// Invocation carries its own inherited ancestor proof: a Host delegation
// Item may end its Item chain without losing the transitive owner Attempts.
func (runner *Runner) freezeChildParentAttempts(
	ctx context.Context, turnID, parentItemID string, root Invocation,
) ([]childAttemptBinding, error) {
	seenItems := make(map[string]struct{})
	seenParents := map[string]childAttemptBinding{}
	parents := []childAttemptBinding{}
	appendParent := func(bound childAttemptBinding) error {
		if !validChildAttempt(bound) || bound.InvocationID == root.ID {
			return ErrConflict
		}
		if previous, exists := seenParents[bound.InvocationID]; exists {
			if previous != bound {
				return ErrConflict
			}
			return nil
		}
		seenParents[bound.InvocationID] = bound
		parents = append(parents, bound)
		return nil
	}
	for id := parentItemID; id != ""; {
		if _, cycle := seenItems[id]; cycle {
			return nil, ErrConflict
		}
		seenItems[id] = struct{}{}
		item, err := runner.store.GetItem(ctx, id)
		if err != nil || item.ID != id || item.TurnID != turnID || item.Seq == 0 {
			return nil, errors.Join(ErrConflict, err)
		}
		if item.InvocationID != "" {
			owner, loadErr := runner.store.GetInvocation(ctx, item.InvocationID)
			if loadErr != nil || owner.ID != item.InvocationID || owner.TurnID != turnID {
				return nil, errors.Join(ErrConflict, loadErr)
			}
			if err := runner.requireItemOwnerAttempt(ctx, item, invocationAttempt(owner)); err != nil {
				return nil, err
			}
			if owner.ID != root.ID {
				proof, proofErr := frozenChildRoot(owner)
				if proofErr != nil || proof.attempt() != invocationAttempt(root) {
					return nil, ErrConflict
				}
				if err := appendParent(invocationAttempt(owner)); err != nil {
					return nil, err
				}
				for _, inherited := range proof.Parents {
					if err := appendParent(inherited); err != nil {
						return nil, err
					}
				}
			}
		}
		id = item.ParentItemID
	}
	for _, parent := range parents {
		if err := runner.requireLiveParentAttempt(ctx, turnID, parent); err != nil {
			return nil, err
		}
	}
	return parents, nil
}
