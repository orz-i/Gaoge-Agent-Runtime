package agent

import (
	"errors"
	"slices"
	"strings"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

// HostedToolGrant freezes a *role-scoped*, explicitly authorized provider Tool.
// It is separate from the parent Agent's enabled ToolKeys and cannot authorize
// a local Tool, expand a parent Run, or override the live Hosted catalog.
type HostedToolGrant struct {
	Key               string `json:"key"`
	DefinitionVersion string `json:"definitionVersion"`
}

// NormalizeHostedToolGrantsForRole canonicalizes a trusted Role-scoped grant
// before Harness seals it. It never resolves or grants a Tool by itself.
func NormalizeHostedToolGrantsForRole(values []HostedToolGrant) ([]HostedToolGrant, error) {
	return normalizedHostedToolGrants(values)
}

func normalizedHostedToolGrants(values []HostedToolGrant) ([]HostedToolGrant, error) {
	if len(values) == 0 {
		return nil, nil
	}
	result := make([]HostedToolGrant, len(values))
	for index, raw := range values {
		raw.Key, raw.DefinitionVersion = strings.TrimSpace(raw.Key), strings.TrimSpace(raw.DefinitionVersion)
		if raw.Key == "" || raw.DefinitionVersion == "" || len(raw.Key) > 256 ||
			len(raw.DefinitionVersion) > 256 {
			return nil, ErrInvalidRequest
		}
		result[index] = raw
	}
	slices.SortFunc(result, func(a, b HostedToolGrant) int { return strings.Compare(a.Key, b.Key) })
	for index := 1; index < len(result); index++ {
		if result[index-1].Key == result[index].Key {
			return nil, ErrInvalidRequest
		}
	}
	return result, nil
}

func validHostedGrantSnapshot(grants []HostedToolGrant, toolKeys []string) bool {
	for index, grant := range grants {
		if grant.Key == "" || grant.DefinitionVersion == "" ||
			!slices.Contains(toolKeys, grant.Key) ||
			index > 0 && grants[index-1].Key >= grant.Key {
			return false
		}
	}
	return true
}

// verifyHostedGrantVersions must run after each fresh provider-hosted catalog
// resolution, including Agent Start, model retry and resumed execution.
// Disabled, absent, model-incompatible or version-changed Tools fail closed.
func verifyHostedGrantVersions(grants []HostedToolGrant, hosted []model.HostedTool) error {
	for _, grant := range grants {
		found := false
		for _, definition := range hosted {
			if definition.Key != grant.Key {
				continue
			}
			found = true
			if strings.TrimSpace(definition.DefinitionVersion) != grant.DefinitionVersion {
				return errors.Join(ErrInvalidRequest, ErrToolFailure)
			}
			break
		}
		if !found {
			return errors.Join(ErrInvalidRequest, ErrToolFailure)
		}
	}
	return nil
}
