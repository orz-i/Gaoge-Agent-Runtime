package providerhttp

import (
	"slices"
	"strings"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

// NormalizeModelProfiles takes exactly one Host-owned model-catalog snapshot
// at Provider construction. Model IDs are DATA; neither this function nor
// any Provider adapter guesses capabilities from prefixes or embedded lists.
//
// Invalid/ambiguous Host authority fails closed before an HTTP request or
// durable Provider binding is created.
func NormalizeModelProfiles(source model.ModelProfileSource, allowedEffort []string) (map[string]model.Capabilities, error) {
	if source == nil {
		return nil, nil
	}
	raw := source.SnapshotModelProfiles()
	if len(raw) > 256 {
		return nil, ErrConfig
	}
	if len(raw) == 0 {
		return nil, nil
	}
	profiles := make(map[string]model.Capabilities, len(raw))
	for id, profile := range raw {
		if id == "" || len(id) > 128 || strings.TrimSpace(id) != id ||
			strings.ContainsAny(id, "\x00\r\n\t") || len(profile.EffortLevels) > len(allowedEffort) {
			return nil, ErrConfig
		}
		levels := make([]string, 0, len(profile.EffortLevels))
		for _, level := range profile.EffortLevels {
			if !slices.Contains(allowedEffort, level) || slices.Contains(levels, level) {
				return nil, ErrConfig
			}
			levels = append(levels, level)
		}
		slices.Sort(levels)
		profile.EffortLevels = levels
		profiles[id] = profile
	}
	return profiles, nil
}
