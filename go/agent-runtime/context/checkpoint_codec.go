package context

// CheckpointWriteProfile selects the algorithms for a newly made decision.
// A durable recipe must fix this selection independently of the current writer.
// The on-disk empty ContentHashEncoding still denotes the historical codec.
type CheckpointWriteProfile string

const (
	CheckpointWriteLegacy       CheckpointWriteProfile = "legacy-json-v0"
	CheckpointWriteLosslessJSON CheckpointWriteProfile = "lossless-json-v1"
)

func ValidCheckpointWriteProfile(profile CheckpointWriteProfile) bool {
	return profile == CheckpointWriteLegacy || profile == CheckpointWriteLosslessJSON
}

// CheckpointWriteProfile returns the concrete selection for new decisions,
// never an empty/latest alias that could change meaning during recovery.
func (manager *Manager) CheckpointWriteProfile() CheckpointWriteProfile {
	if manager == nil || manager.checkpointWriteProfile == "" {
		return CheckpointWriteLegacy
	}
	return manager.checkpointWriteProfile
}

// WithCheckpointWriteProfile scopes pure construction to one frozen profile.
// Unlike Dependencies, an empty profile here is invalid: recovery must resolve
// old missing recipe metadata explicitly, not inherit the current writer default.
func (manager *Manager) WithCheckpointWriteProfile(profile CheckpointWriteProfile) (*Manager, error) {
	if !ValidCheckpointWriteProfile(profile) {
		return nil, ErrInvalidInput
	}
	copy := Manager{checkpointWriteProfile: profile}
	if manager != nil {
		copy.counter = manager.counter
	}
	return &copy, nil
}

// sealCheckpoint is the sole writer of a new revision's encoding, hash and ID.
// No-op operations return the existing record without entering this boundary.
func (manager *Manager) sealCheckpoint(value Checkpoint) (Checkpoint, error) {
	switch manager.CheckpointWriteProfile() {
	case CheckpointWriteLegacy:
		value.ContentHashEncoding = ""
	case CheckpointWriteLosslessJSON:
		value.ContentHashEncoding = CheckpointContentHashEncodingLosslessJSON
	default:
		return Checkpoint{}, ErrInvalidInput
	}
	hash, err := checkpointWindowHash(value.Window, value.ContentHashEncoding)
	if err != nil || hash == "" {
		return Checkpoint{}, ErrInvalidInput
	}
	value.ContentHash = hash
	value.ID = checkpointID(value)
	return value, nil
}
