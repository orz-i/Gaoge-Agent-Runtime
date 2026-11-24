package harness

import (
	"context"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	runtimecontext "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/context"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

// ModelWindowResolver supplies the routed model's real window for an exact
// sampling request. It must not invent a default; an unknown limit fails closed.
// This is a read-only measurement dependency: it must not generate model output
// or perform business effects. Recovery may repeat an unreturned resolution.
type ModelWindowResolver func(context.Context, model.Request) (runtimecontext.ModelWindow, error)

// NewManagedContextPreparer builds a pre-intent Context projection with
// optional, durable pressure-based rollover. Config.ContextPolicy is read from
// the immutable Harness Turn snapshot; the host resolves model capacity.
//
// Compaction is permitted only for the owning Turn with an authorized
// harness.read_context_artifact Tool. Removed content is sealed in the existing
// Harness Store BEFORE committing the next Checkpoint. An unsuccessful CAS
// may leave harmless orphaned artifacts, never a missing referenced archive.
// The returned request is frozen by Agent in its ModelInvocation before
// dispatch, so a process restart does not recalculate a pending window.
func NewManagedContextPreparer(store Store, manager *runtimecontext.Manager, resolve ModelWindowResolver) (agent.ModelRequestPreparer, error) {
	bounded, err := NewBoundedContextAdapter(store, manager, resolve, nil)
	if err != nil {
		return nil, err
	}
	return bounded.PrepareRequest, nil
}

func hasContextArtifactReader(request model.Request) bool {
	for _, definition := range request.Tools {
		if definition.Key == ContextArtifactToolKey {
			return true
		}
	}
	return false
}
