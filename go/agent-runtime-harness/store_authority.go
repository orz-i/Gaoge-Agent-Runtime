package harness

import (
	"bytes"
	"context"
	"strings"
)

// canonicalSealedConfig never trusts a persisted ContentHash by itself. It
// recomputes the original canonical payload, digest and derived ID, then
// returns ONLY that normalized value. Legitimate Store JSON roundtrips may
// represent an empty slice as nil, but can never change execution semantics
// or enlarge authority by substituting a stale/mismatched hash.
func canonicalSealedConfig(value ConfigSnapshot) (ConfigSnapshot, error) {
	if !validConfigSnapshot(value) {
		return ConfigSnapshot{}, ErrConflict
	}
	sealed, err := SealConfigSnapshot(value.TurnID, value, value.CreatedAt)
	if err != nil || sealed.ID != value.ID || sealed.ContentHash != value.ContentHash {
		return ConfigSnapshot{}, ErrConflict
	}
	return sealed, nil
}

// loadSealedConfig is the one trusted Harness read boundary for a frozen
// configuration, whether used for replay, Context, Budget or Host display.
func loadSealedConfig(ctx context.Context, store Store, configID string) (ConfigSnapshot, error) {
	if store == nil || strings.TrimSpace(configID) == "" {
		return ConfigSnapshot{}, ErrInvalidRequest
	}
	value, err := store.GetConfigSnapshot(ctx, configID)
	if err != nil {
		return ConfigSnapshot{}, err
	}
	if value.ID != configID {
		return ConfigSnapshot{}, ErrConflict
	}
	return canonicalSealedConfig(value)
}

// sameFrozenInvocationAuthority compares exactly the immutable original
// execution contract. A status or metadata projection can progress on replay,
// but the physical Run, owner Turn, Attempt and frozen input cannot drift.
func sameFrozenInvocationAuthority(candidate, returned Invocation) bool {
	return returned.ID == candidate.ID && returned.TurnID == candidate.TurnID &&
		returned.ParentItemID == candidate.ParentItemID &&
		returned.CapabilityKey == candidate.CapabilityKey &&
		returned.DefinitionVersion == candidate.DefinitionVersion &&
		returned.ExecutionClass == candidate.ExecutionClass &&
		bytes.Equal(returned.Input, candidate.Input) &&
		returned.InputHash == candidate.InputHash &&
		returned.ExecutionRefID == candidate.ExecutionRefID &&
		returned.Attempt == candidate.Attempt
}

// createVerifiedInvocation never treats a Store's Create result as a new
// execution-identity source. Verify the returned receipt AND the canonical
// indexed record before any Item/Grant, budget attribution or physical effect.
// The Store still owns concurrency and its atomic CAS; this read is an
// integrity fence, not an alternative execution lease.
func (runner *Runner) createVerifiedInvocation(ctx context.Context, candidate Invocation) (Invocation, bool, error) {
	if runner == nil || runner.store == nil || !validInvocation(candidate) {
		return Invocation{}, false, ErrInvalidRequest
	}
	returned, fresh, err := runner.store.CreateInvocation(ctx, candidate)
	if err != nil {
		return Invocation{}, false, err
	}
	if !validInvocation(returned) || !sameFrozenInvocationAuthority(candidate, returned) ||
		(fresh && (returned.Status != InvocationAccepted || returned.Revision != 1 ||
			len(returned.OutputRefs) != 0 || returned.ErrorCode != "" || returned.ErrorDetail != "")) {
		return Invocation{}, false, ErrConflict
	}
	canonical, err := runner.store.GetInvocation(ctx, candidate.ID)
	if err != nil || !validInvocation(canonical) || !sameFrozenInvocationAuthority(candidate, canonical) {
		return Invocation{}, false, ErrConflict
	}
	return returned, fresh, nil
}
