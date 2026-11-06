package model

// ModelProfileSource supplies a complete immutable-in-use snapshot of
// Host-certified model features. Implementations may import these facts from
// files, an API, a database or another external discovery service BEFORE
// constructing a Provider adapter. The SDK never embeds vendor model IDs.
//
// The adapter snapshots, validates and fingerprints these facts on creation.
// To apply a changed catalog, build a NEW adapter/Router; a pending durable
// ModelInvocation remains bound to its original Provider fingerprint.
type ModelProfileSource interface {
	SnapshotModelProfiles() map[string]Capabilities
}

// ModelProfileSourceFunc adapts an external loader's already-materialized
// snapshot without requiring a registry, network client or separate SDK mode.
type ModelProfileSourceFunc func() map[string]Capabilities

func (source ModelProfileSourceFunc) SnapshotModelProfiles() map[string]Capabilities {
	if source == nil {
		return nil // No capabilities are authorized by an absent source.
	}
	return source()
}

// StaticModelProfiles is the small, copy-on-snapshot adapter for Host-owned
// model catalogs (including maps decoded from JSON or YAML by Host code).
// The source and returned values may be mutated freely after New: Provider
// adapters keep their own immutable validated copies.
type StaticModelProfiles map[string]Capabilities

func (source StaticModelProfiles) SnapshotModelProfiles() map[string]Capabilities {
	if len(source) == 0 {
		return nil
	}
	result := make(map[string]Capabilities, len(source))
	for id, profile := range source {
		profile.EffortLevels = append([]string(nil), profile.EffortLevels...)
		result[id] = profile
	}
	return result
}
