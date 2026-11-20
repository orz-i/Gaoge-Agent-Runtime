package agent

import (
	"strings"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

// pendingToolCatalogFromInvocation copies original Catalog evidence, not the
// possibly projected model schema or the live Catalog at receipt consumption.
// The batch retains these hashes while calls move through deferred/receipt
// recovery slots; the next model-created batch replaces the bounded mapping.
func pendingToolCatalogFromInvocation(definitions []tools.Definition, fingerprints []string, calls []tools.Call) (map[string]string, error) {
	if !validCatalogFingerprints(definitions, fingerprints) {
		return nil, ErrToolDiscoveryDenied
	}
	byKey := make(map[string]string, len(definitions))
	for index, definition := range definitions {
		byKey[definition.Key] = fingerprints[index]
	}
	proof := make(map[string]string, len(calls))
	for _, call := range calls {
		fingerprint, found := byKey[call.ToolKey]
		if !found || call.ID == "" {
			return nil, ErrToolDiscoveryDenied
		}
		proof[call.ID] = fingerprint
	}
	return proof, nil
}

func validPendingToolCatalog(state runState) bool {
	if state.PendingToolCatalog == nil {
		// Pre-ceiling persisted Runs did not bind eager Tool batches to a
		// Catalog snapshot. Keep that legacy zero-ceiling recovery contract;
		// newly bounded Runs must never downgrade to it on missing evidence.
		return state.MaxModelOutputTokens == 0 ||
			(len(state.PendingCalls) == 0 && len(state.DeferredChildren) == 0 && len(state.RecoveredTools) == 0)
	}
	for id, fingerprint := range state.PendingToolCatalog {
		if id == "" || id != strings.TrimSpace(id) || !validBindingFingerprint(fingerprint) {
			return false
		}
	}
	contains := func(call tools.Call) bool {
		_, found := state.PendingToolCatalog[call.ID]
		return found
	}
	for _, call := range state.PendingCalls {
		if !contains(call) {
			return false
		}
	}
	for _, child := range state.DeferredChildren {
		if !contains(child.Call) {
			return false
		}
	}
	for _, receipt := range state.RecoveredTools {
		if !contains(receipt.Call) {
			return false
		}
	}
	return true
}

func validatePendingToolDefinition(state runState, call tools.Call, definition tools.Definition) error {
	if state.PendingToolCatalog == nil && state.MaxModelOutputTokens == 0 {
		return nil // Existing unproven zero-ceiling snapshots retain their semantics.
	}
	original, found := state.PendingToolCatalog[call.ID]
	current, err := definitionFingerprint(definition)
	if !found || err != nil || original != current || call.Name != definition.Name {
		return ErrToolDiscoveryDenied
	}
	return nil
}

func pendingToolCatalogErrorCode(state runState) string {
	if state.Discovery != nil {
		return "agent.discovery_invalid"
	}
	return "agent.tool_invalid"
}
