package harness

import (
	"context"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	runtimecontext "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/context"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/jsoncontract"
)

// ContextBaseBinding freezes the existing Context open plan with its original
// Config. Source describes the actual Open.Previous (also for a full-path reset,
// where it may have a different static fingerprint). Nil Source freezes that no
// previous checkpoint existed; nil ContextBase on Config denotes legacy data.
// ExpectedHeadCheckpointID is only the original Store CAS condition, not an
// alternative source of Context or execution authority.
type ContextBaseBinding struct {
	Source                   *runtimecontext.WorkingStateSource `json:"source,omitempty"`
	SourceDelta              bool                               `json:"sourceDelta,omitempty"`
	ResetCacheIdentity       bool                               `json:"resetCacheIdentity,omitempty"`
	ExpectedHeadCheckpointID string                             `json:"expectedHeadCheckpointID,omitempty"`
}

func cloneContextBaseBinding(value *ContextBaseBinding) *ContextBaseBinding {
	if value == nil {
		return nil
	}
	copy := *value
	if value.Source != nil {
		source := *value.Source
		copy.Source = &source
	}
	return &copy
}

func validContextBaseDigest(value string, size int) bool {
	if len(value) != size {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && hex.EncodeToString(decoded) == value
}

func validContextBaseCheckpointID(value string) bool {
	return strings.HasPrefix(value, "ctxc_") && validContextBaseDigest(strings.TrimPrefix(value, "ctxc_"), 32)
}

func validateContextBaseBinding(value *ContextBaseBinding) error {
	if value == nil {
		return nil
	}
	if jsoncontract.ValidateCanonicalUTF8(value) != nil ||
		(value.ExpectedHeadCheckpointID != "" && !validContextBaseCheckpointID(value.ExpectedHeadCheckpointID)) {
		return ErrInvalidRequest
	}
	if value.Source == nil {
		if value.SourceDelta || value.ExpectedHeadCheckpointID != "" {
			return ErrInvalidRequest
		}
		return nil
	}
	source := value.Source
	// Source IDs retain the existing unrestricted Context identity length. The
	// WorkingState payload's separate text-size limits do not apply here.
	if !validContextBaseCheckpointID(source.CheckpointID) || !validContextBaseDigest(source.ContentHash, 64) ||
		!validContextBaseDigest(source.CoveredPathHash, 64) || source.CoveredThroughSourceID == "" ||
		source.CoveredThroughSourceID != strings.TrimSpace(source.CoveredThroughSourceID) {
		return ErrInvalidRequest
	}
	return nil
}

// Host admission is checked without derived Context data. New admissions then
// seal one selected open plan; retries use the original sealed plan rather than
// deriving another one from today's active head or WorkingState version.
func (runner *Runner) sealContextAdmission(ctx context.Context, scopeID, turnID string, config ConfigSnapshot, seed *ContextSeed, now time.Time) (ConfigSnapshot, error) {
	config.ContextBase = nil
	hostConfig, err := SealConfigSnapshot(turnID, config, now)
	if err != nil {
		return ConfigSnapshot{}, err
	}
	// Only a missing Turn means new admission. A missing original Session or
	// Config after the Turn exists must never be mistaken for a fresh request.
	existing, err := loadVerifiedTurnIdentity(ctx, runner.store, turnID)
	if err == nil {
		if existing.SessionID != scopeID {
			return ConfigSnapshot{}, ErrConflict
		}
		if _, err := loadVerifiedSession(ctx, runner.store, existing.SessionID); err != nil {
			return ConfigSnapshot{}, errors.Join(ErrConflict, err)
		}
		original, loadErr := loadSealedConfig(ctx, runner.store, existing.ConfigSnapshotID)
		if loadErr != nil || original.TurnID != turnID {
			return ConfigSnapshot{}, errors.Join(ErrConflict, loadErr)
		}
		withoutBinding := cloneConfigSnapshot(original)
		withoutBinding.ContextBase = nil
		originalHost, sealErr := SealConfigSnapshot(turnID, withoutBinding, original.CreatedAt)
		if sealErr != nil || originalHost.ContentHash != hostConfig.ContentHash {
			return ConfigSnapshot{}, ErrConflict
		}
		return original, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return ConfigSnapshot{}, err
	}
	if seed == nil {
		return hostConfig, nil
	}
	config.ContextBase, err = runner.contextAdmissionBase(ctx, scopeID, hostConfig, seed)
	if err != nil {
		return ConfigSnapshot{}, err
	}
	return SealConfigSnapshot(turnID, config, now)
}

func (runner *Runner) contextAdmissionBase(ctx context.Context, scopeID string, config ConfigSnapshot, seed *ContextSeed) (*ContextBaseBinding, error) {
	if runner.context == nil {
		return nil, ErrInvalidRequest
	}
	normalized, err := normalizeContextSeed(seed)
	if err != nil || normalized == nil {
		return nil, ErrInvalidRequest
	}
	fingerprint, err := contextStaticFingerprint(config, normalized, runner.catalog)
	if err != nil {
		return nil, err
	}
	active, headID, err := runner.loadActiveContextHead(ctx, scopeID)
	if err != nil {
		return nil, err
	}
	if active != nil && (!validContextCheckpoint(*active) || active.ScopeID != scopeID) {
		return nil, ErrConflict
	}
	plan, err := runner.contextOpenRequest(ctx, scopeID, fingerprint, config, normalized, active)
	if err != nil {
		return nil, err
	}
	if _, err := runner.context.Open(ctx, plan); err != nil {
		return nil, err
	}
	binding := &ContextBaseBinding{SourceDelta: plan.SourceDelta, ResetCacheIdentity: plan.ResetCacheIdentity, ExpectedHeadCheckpointID: headID}
	if previous := plan.Previous; previous != nil {
		binding.Source = &runtimecontext.WorkingStateSource{
			CheckpointID: previous.ID, ContentHash: previous.ContentHash,
			CoveredThroughSourceID: previous.CoveredThroughSourceID, CoveredPathHash: previous.CoveredPathHash,
		}
	}
	return binding, validateContextBaseBinding(binding)
}

func (runner *Runner) boundContextOpenRequest(ctx context.Context, scopeID, fingerprint string, config ConfigSnapshot, seed *ContextSeed) (runtimecontext.OpenRequest, error) {
	binding := config.ContextBase
	if binding == nil || validateContextBaseBinding(binding) != nil {
		return runtimecontext.OpenRequest{}, ErrConflict
	}
	var previous *runtimecontext.Checkpoint
	if source := binding.Source; source != nil {
		loaded, err := runner.store.GetContextCheckpoint(ctx, source.CheckpointID)
		if err != nil {
			return runtimecontext.OpenRequest{}, err
		}
		if !validContextCheckpoint(loaded) || loaded.ID != source.CheckpointID || loaded.ScopeID != scopeID ||
			loaded.ContentHash != source.ContentHash || loaded.CoveredThroughSourceID != source.CoveredThroughSourceID ||
			loaded.CoveredPathHash != source.CoveredPathHash || (binding.SourceDelta && loaded.StaticFingerprint != fingerprint) {
			return runtimecontext.OpenRequest{}, ErrConflict
		}
		previous = &loaded
	}
	instructions := strings.TrimSpace(strings.Join([]string{config.Instructions, seed.Instructions}, "\n\n"))
	if binding.SourceDelta {
		if seed.SourceDelta {
			if seed.BaseCheckpointID != previous.ID {
				return runtimecontext.OpenRequest{}, ErrConflict
			}
		} else {
			index := runtimecontext.CheckpointSourceIndex(*previous, seed.SourcePath)
			if index < 0 {
				return runtimecontext.OpenRequest{}, ErrConflict
			}
			seed = sliceContextSeedAfter(seed, index)
		}
		plan := contextDeltaOpenRequest(scopeID, fingerprint, instructions, seed, previous)
		plan.ResetCacheIdentity = binding.ResetCacheIdentity
		return plan, nil
	}
	if seed.SourceDelta {
		return runtimecontext.OpenRequest{}, ErrConflict
	}
	return runtimecontext.OpenRequest{
		ScopeID: scopeID, StaticFingerprint: fingerprint, SourcePath: append([]string(nil), seed.SourcePath...),
		Entries: runtimecontext.CloneEntries(seed.Entries), Instructions: instructions, Previous: previous,
		ResetCacheIdentity: binding.ResetCacheIdentity, WorkingState: runtimecontext.CloneWorkingState(seed.WorkingState),
	}, nil
}

// Admission has already proved this source was the selected safe base. Keep
// that exact immutable source on retry, even if another Turn moved the active
// head before this Turn attached its first checkpoint. A source update never
// pins an arbitrary runtime-tail checkpoint or changes the original authority.
func (runner *Runner) frozenWorkingStateOpenRequest(ctx context.Context, scopeID, fingerprint string, config ConfigSnapshot, seed *ContextSeed, active *runtimecontext.Checkpoint) (runtimecontext.OpenRequest, error) {
	base, err := runner.loadContextDeltaBase(ctx, scopeID, fingerprint, seed.WorkingState.Source.CheckpointID)
	if err != nil {
		return runtimecontext.OpenRequest{}, err
	}
	index := runtimecontext.CheckpointSourceIndex(*base, seed.SourcePath)
	if index < 0 {
		return runtimecontext.OpenRequest{}, ErrConflict
	}
	delta := sliceContextSeedAfter(seed, index)
	if !delta.ResetCacheIdentity && active != nil && base.ID != active.ID {
		diverged, err := runner.contextBaseIsActiveAncestor(ctx, base, active)
		if err != nil {
			return runtimecontext.OpenRequest{}, err
		}
		delta.ResetCacheIdentity = diverged
	}
	instructions := strings.TrimSpace(strings.Join([]string{config.Instructions, seed.Instructions}, "\n\n"))
	return contextDeltaOpenRequest(scopeID, fingerprint, instructions, delta, base), nil
}
