package agent

import (
	"bytes"
	"context"
	"encoding/json"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

// validatePendingModelToolDeclarations checks a durable, previously composed
// Model Request against the *current* catalog before each physical dispatch.
//
// A ModelInvocation may outlive policy changes while pending or retryable.
// Its RequestHash guarantees durable request identity but does not attest that
// the old tool grants remain available. This guard neither rebuilds the
// invocation nor changes its model, ToolKeys, or retry identity.
func (runner *Runner) validatePendingModelToolDeclarations(
	ctx context.Context, snapshot kernel.Snapshot, state runState, request model.Request,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if request.RunID != snapshot.Run.ID || request.Model != state.Model {
		return ErrToolDiscoveryInvalid
	}
	if state.Discovery != nil && state.Discovery.RunID != snapshot.Run.ID {
		return ErrToolDiscoveryInvalid
	}
	local, hosted, err := runner.resolveSelectedTools(ctx, state.ToolKeys, state.Model, snapshot.Run.DeadlineAt)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		return ErrToolDiscoveryDenied
	}
	if err = verifyHostedGrantVersions(state.HostedToolGrants, hosted); err != nil {
		return ErrToolDiscoveryDenied
	}
	if err = validateDiscoveryCatalog(state.Discovery, local); err != nil {
		return ErrToolDiscoveryDenied
	}

	localByKey := make(map[string]tools.Definition, len(local))
	for _, definition := range local {
		localByKey[definition.Key] = definition
	}
	seen := make(map[string]struct{}, len(request.Tools))
	for _, original := range request.Tools {
		if _, duplicate := seen[original.Key]; duplicate {
			return ErrToolDiscoveryDenied
		}
		seen[original.Key] = struct{}{}
		if original.Key == ToolDiscoveryKey {
			if state.Discovery == nil || !sameFrozenToolDefinition(discoveryToolDefinition, original) {
				return ErrToolDiscoveryDenied
			}
			continue
		}
		current, found := localByKey[original.Key]
		if !found || !sameFrozenToolDefinition(current, original) {
			return ErrToolDiscoveryDenied
		}
		if err = validateLoadedDiscoveryExecution(state.Discovery, original.Key, current); err != nil {
			return ErrToolDiscoveryDenied
		}
	}

	hostedByKey := make(map[string]model.HostedTool, len(hosted))
	for _, item := range hosted {
		hostedByKey[item.Key] = item
	}
	seen = make(map[string]struct{}, len(request.HostedTools))
	for _, original := range request.HostedTools {
		if _, duplicate := seen[original.Key]; duplicate {
			return ErrToolDiscoveryDenied
		}
		seen[original.Key] = struct{}{}
		current, found := hostedByKey[original.Key]
		if !found || !sameFrozenHostedTool(current, original) {
			return ErrToolDiscoveryDenied
		}
	}
	return nil
}

func sameFrozenToolDefinition(left, right tools.Definition) bool {
	leftHash, leftErr := definitionFingerprint(left)
	rightHash, rightErr := definitionFingerprint(right)
	return leftErr == nil && rightErr == nil && leftHash == rightHash
}

// JSON serialization canonicalizes insignificant RawMessage whitespace.
// Comparing the entire Hosted descriptor (not only its version) also prevents
// a catalog implementation from changing a provider payload under the same
// definition version between physical request attempts.
func sameFrozenHostedTool(left, right model.HostedTool) bool {
	leftBytes, leftErr := json.Marshal(left)
	rightBytes, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftBytes, rightBytes)
}
