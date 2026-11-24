package agent

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"time"

	runtimecontext "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/context"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

// modelPreparation is a synchronous, Runtime-owned candidate construction
// scope, not a dispatch capability. The chosen invocation, if any, is used
// directly by the single durable intent commit.
type modelPreparation struct {
	run      kernel.Run
	client   model.Client
	now      time.Time
	original model.Request
	catalog  []tools.Definition
	mu       sync.Mutex
	selected *ModelInvocation
	closed   bool
}
type modelPreparationKey struct{}

func (p *modelPreparation) candidate(request model.Request) (ModelInvocation, error) {
	if !validPreparedModelRequest(p.original, request) {
		return ModelInvocation{}, ErrInvalidRequest
	}
	fingerprints, err := catalogFingerprintsForRequest(p.catalog, request.Tools)
	if err != nil {
		return ModelInvocation{}, err
	}
	return planModelInvocation(p.run, p.client, request, fingerprints, "", nil, p.now)
}

func (p *modelPreparation) close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.closed = true
}

func planModelInvocation(run kernel.Run, client model.Client, request model.Request, fingerprints []string, purpose string, binding *ContextSummaryBinding, now time.Time) (ModelInvocation, error) {
	probe := model.CloneRequest(request)
	probe.InvocationID = ""
	provider, fingerprint := providerName(client, probe), modelBindingFingerprint(client, probe)
	if provider == "" && declaresProviderIdentity(client) ||
		(provider != "" || fingerprint != "") && !validBindingFingerprint(fingerprint) {
		return ModelInvocation{}, ErrInvalidRequest
	}
	invocation, err := newInvocation(run, request, fingerprints, provider, fingerprint, purpose, binding, now)
	if err != nil {
		return ModelInvocation{}, err
	}
	if !matchesFrozenProviderBinding(client, invocation) {
		return ModelInvocation{}, errors.Join(ErrInvalidRequest, ErrProviderBindingMismatch)
	}
	return invocation, nil
}

// modelCandidate couples the budget result to the exact request measured by
// Runtime, including its final InvocationID. Its fields granting selection are
// private; assessing a candidate neither writes state nor authorizes dispatch.
type modelCandidate struct {
	assessment  runtimecontext.Assessment
	request     model.Request
	invocation  *ModelInvocation
	preparation *modelPreparation
}

// assessModelCandidate is used at the pre-intent Context boundary by the
// concrete adapter, including its low-level Harness composition. In an Agent
// preparation scope it derives the actual invocation for this candidate
// before any counter sees it. Standalone use measures the literal request.
func assessModelCandidate(ctx context.Context, manager *runtimecontext.Manager, request model.Request, window runtimecontext.ModelWindow, policy runtimecontext.Policy, frame runtimecontext.FrozenAccounting) (modelCandidate, error) {
	candidate := modelCandidate{request: model.CloneRequest(request)}
	measured := candidate.request
	if preparation, ok := ctx.Value(modelPreparationKey{}).(*modelPreparation); ok {
		preparation.mu.Lock()
		closed := preparation.closed
		preparation.mu.Unlock()
		if closed {
			return modelCandidate{}, ErrInvalidRequest
		}
		invocation, err := preparation.candidate(candidate.request)
		if err != nil {
			return modelCandidate{}, err
		}
		candidate.preparation, candidate.invocation = preparation, &invocation
		measured = invocation.Request
	}
	assessment, err := manager.AssessModelRequestWithAccounting(ctx, measured, window, policy, frame)
	if err != nil {
		return modelCandidate{}, err
	}
	candidate.assessment = assessment
	return candidate, nil
}

// selectForIntent records the exact admitted candidate before any Context write. It
// returns the unchanged pre-intent projection for existing Host callback
// receipts; the actual invocation remains owned by Runtime.
func (candidate modelCandidate) selectForIntent(ctx context.Context) (model.Request, error) {
	if candidate.invocation != nil {
		p, ok := ctx.Value(modelPreparationKey{}).(*modelPreparation)
		if !ok || p != candidate.preparation || !matchesFrozenProviderBinding(p.client, *candidate.invocation) {
			return model.Request{}, ErrInvalidRequest
		}
		p.mu.Lock()
		defer p.mu.Unlock()
		if p.closed || p.selected != nil {
			return model.Request{}, ErrInvalidRequest
		}
		selected := cloneModelInvocation(*candidate.invocation)
		p.selected = &selected
	}
	return model.CloneRequest(candidate.request), nil
}

// finish consumes the selected candidate rather than constructing a second
// intent. An ordinary Host-only preparer without a Context assessment still
// uses the same identity constructor, without claiming budget evidence.
func (p *modelPreparation) finish(request model.Request) (ModelInvocation, error) {
	p.mu.Lock()
	p.closed = true
	chosen := p.selected
	p.mu.Unlock()
	if chosen == nil {
		return p.candidate(request)
	}
	selected := *chosen
	projected := model.CloneRequest(selected.Request)
	projected.InvocationID = p.original.InvocationID
	if !reflect.DeepEqual(projected, model.CloneRequest(request)) || !matchesFrozenProviderBinding(p.client, selected) {
		return ModelInvocation{}, ErrInvalidRequest
	}
	return selected, nil
}
