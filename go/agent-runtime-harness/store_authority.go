package harness

import (
	"bytes"
	"context"
	"strings"
)

// loadVerifiedSession never accepts a Store read as a new Session owner.
// The caller-supplied key remains the authority for the read, including
// on Host presentation, cancellation and replay paths.
func loadVerifiedSession(ctx context.Context, store Store, sessionID string) (Session, error) {
	if store == nil || strings.TrimSpace(sessionID) == "" {
		return Session{}, ErrInvalidRequest
	}
	value, err := store.GetSession(ctx, sessionID)
	if err != nil {
		return Session{}, err
	}
	if !validSession(value) || value.ID != sessionID {
		return Session{}, ErrConflict
	}
	// Host-derived Session IDs carry the original HostThread identity.
	// Independently persisted Store records may use their own IDs, but a
	// Host-derived key must never be rebound to another HostThread.
	if hostDerivedID(sessionID, "hs") {
		derived, deriveErr := SessionID(value.HostThread)
		if deriveErr != nil || derived != sessionID {
			return Session{}, ErrConflict
		}
	}
	return value, nil
}

// loadVerifiedTurnIdentity proves the exact requested Turn and its original
// Host-derived Session/HostTurn binding without requiring ancillary Session or
// Config reads. Only protective cancellation may consume this narrower proof:
// it must still verify the frozen Invocation and physical Run before acting.
// All new execution, recovery and Host-visible reads use loadVerifiedTurn.
func loadVerifiedTurnIdentity(ctx context.Context, store Store, turnID string) (Turn, error) {
	if store == nil || strings.TrimSpace(turnID) == "" {
		return Turn{}, ErrInvalidRequest
	}
	value, err := store.GetTurn(ctx, turnID)
	if err != nil {
		return Turn{}, err
	}
	if !validTurn(value) || value.ID != turnID {
		return Turn{}, ErrConflict
	}
	if hostDerivedID(turnID, "ht") {
		derived, deriveErr := TurnID(value.SessionID, value.HostTurn)
		if deriveErr != nil || derived != turnID {
			return Turn{}, ErrConflict
		}
	}
	return value, nil
}

// loadVerifiedTurn additionally proves the Session and frozen Config links.
// A different, otherwise valid Turn may never stand in for the original owner
// before new execution, recovery or Host-visible projection.
func loadVerifiedTurn(ctx context.Context, store Store, turnID string) (Turn, error) {
	value, err := loadVerifiedTurnIdentity(ctx, store, turnID)
	if err != nil {
		return Turn{}, err
	}
	if hostDerivedID(turnID, "ht") {
		if _, err := loadVerifiedSession(ctx, store, value.SessionID); err != nil {
			return Turn{}, err
		}
		config, err := loadSealedConfig(ctx, store, value.ConfigSnapshotID)
		if err != nil {
			return Turn{}, err
		}
		if config.TurnID != turnID {
			return Turn{}, ErrConflict
		}
	}
	return value, nil
}

// Only IDs with the exact lowercase 24-hex digest form belong to the
// Host-derived namespace. Standalone callers may use similarly prefixed IDs
// containing non-hex characters; they still require exact key/shape checks.
func hostDerivedID(value, prefix string) bool {
	if len(value) != len(prefix)+1+24 || !strings.HasPrefix(value, prefix+"_") {
		return false
	}
	for i := len(prefix) + 1; i < len(value); i++ {
		c := value[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

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
