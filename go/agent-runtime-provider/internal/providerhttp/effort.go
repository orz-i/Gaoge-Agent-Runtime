package providerhttp

import (
	"slices"
	"strings"
)

// ValidEffortLevels is the conservative supported option vocabulary. The
// exact model-specific subset must be checked by each concrete adapter.
var ValidEffortLevels = []string{"none", "minimal", "low", "medium", "high", "xhigh", "max"}

func validEffort(level string) bool { return slices.Contains(ValidEffortLevels, level) }

// NormalizeEffortModels copies and validates explicit Host opt-ins for
// private/gateway models. No implicit prefix match or silent level expansion.
func NormalizeEffortModels(config map[string][]string, allowed []string) (map[string][]string, error) {
	if len(config) > 64 {
		return nil, ErrConfig
	}
	result := make(map[string][]string, len(config))
	for modelID, levels := range config {
		if modelID == "" || strings.TrimSpace(modelID) != modelID || len(modelID) > 128 ||
			len(levels) == 0 || len(levels) > len(allowed) {
			return nil, ErrConfig
		}
		copyLevels := make([]string, 0, len(levels))
		for _, level := range levels {
			if !slices.Contains(allowed, level) || slices.Contains(copyLevels, level) {
				return nil, ErrConfig
			}
			copyLevels = append(copyLevels, level)
		}
		slices.Sort(copyLevels)
		result[modelID] = copyLevels
	}
	return result, nil
}
