package agent

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	runtimecontext "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/context"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

// modelPreparation is a synchronous, Runtime-owned candidate construction
// scope, not a dispatch capability. The chosen invocation, if any, is used
// directly by the single durable intent commit.
type modelPreparation struct {
	run         kernel.Run
	client      model.Client
	now         time.Time
	original    model.Request
	catalog     []tools.Definition
	budget      *BoundedBudgetAdapter
	mu          sync.Mutex
	ledger      *budget.Ledger
	accountings []candidateAccounting
	selected    *ModelInvocation
	closed      bool
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
	assessment     runtimecontext.Assessment
	modelWindow    runtimecontext.ModelWindow
	omitLocalTools bool
	request        model.Request
	invocation     *ModelInvocation
	preparation    *modelPreparation
}

// assessModelCandidate is used at the pre-intent Context boundary by the
// concrete adapter, including its low-level Harness composition. In an Agent
// preparation scope it derives the actual invocation for this candidate
// before any counter sees it. Standalone use measures the literal request.
func assessModelCandidate(ctx context.Context, adapter *BoundedContextAdapter, request model.Request, window runtimecontext.ModelWindow, policy runtimecontext.Policy, frame runtimecontext.FrozenAccounting) (modelCandidate, error) {
	candidate := modelCandidate{request: model.CloneRequest(request), modelWindow: window}
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
		if preparation.budget != nil {
			ledger, err := preparation.samplingLedger(ctx)
			if err != nil {
				return modelCandidate{}, err
			}
			if ledger.ID != "" {
				return preparation.assessBudgetCandidate(ctx, adapter, candidate, ledger, window, policy, frame)
			}
		}
	}
	assessment, err := adapter.manager.AssessModelRequestWithAccounting(ctx, measured, window, policy, frame)
	if err != nil {
		return modelCandidate{}, err
	}
	candidate.assessment = assessment
	return candidate, nil
}

// All candidates of one Context search observe the same ledger snapshot.
// This is only a planning view; the actual reserve later uses the live CAS.
func (p *modelPreparation) samplingLedger(ctx context.Context) (budget.Ledger, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.ledger == nil {
		l, err := p.budget.bind(ctx, p.run.ID)
		if err != nil {
			return budget.Ledger{}, err
		}
		p.ledger = &l
	}
	return budget.CloneLedger(*p.ledger), nil
}

type candidateAccounting struct {
	window runtimecontext.ModelWindow
	frame  runtimecontext.FrozenAccounting
}

func (p *modelPreparation) assessSamplingRequest(ctx context.Context, adapter *BoundedContextAdapter, request model.Request,
	window runtimecontext.ModelWindow, policy runtimecontext.Policy, frame runtimecontext.FrozenAccounting) (runtimecontext.Assessment, candidateAccounting, error) {
	accounting := candidateAccounting{window: window, frame: frame}
	if !frame.MatchesRequest(request) {
		p.mu.Lock()
		found := false
		for _, cached := range p.accountings {
			if cached.frame.MatchesRequest(request) {
				accounting, found = cached, true
				break
			}
		}
		p.mu.Unlock()
		if !found {
			resolved, err := adapter.ports.ResolveWindow(ctx, model.CloneRequest(request))
			if err != nil {
				return runtimecontext.Assessment{}, candidateAccounting{}, err
			}
			frozen, err := adapter.manager.FreezeRequestAccounting(ctx, request, resolved)
			if err != nil {
				return runtimecontext.Assessment{}, candidateAccounting{}, err
			}
			accounting = candidateAccounting{window: resolved, frame: frozen}
			p.mu.Lock()
			p.accountings = append(p.accountings, accounting)
			p.mu.Unlock()
		}
	}
	assessment, err := adapter.manager.AssessModelRequestWithAccounting(ctx, request, accounting.window, policy, accounting.frame)
	return assessment, accounting, err
}

// Preview and final planning are the same SharedBudget algorithm. The
// preliminary measurement permits ordinary Context pressure/compaction even
// when a provider's admission planner refuses an oversized sample outright.
// Only the completed, exactly measured physical plan may be selected.
func (p *modelPreparation) assessBudgetCandidate(ctx context.Context, adapter *BoundedContextAdapter, candidate modelCandidate, ledger budget.Ledger,
	window runtimecontext.ModelWindow, policy runtimecontext.Policy, frame runtimecontext.FrozenAccounting) (modelCandidate, error) {
	original := candidate.invocation.Request
	preview, _, err := p.budget.ports.PreviewModel(budget.CloneLedger(ledger), model.CloneRequest(original))
	if err != nil {
		return modelCandidate{}, err
	}
	if !validDispatchModelRequest(original, preview) {
		return modelCandidate{}, ErrInvalidRequest
	}
	assessment, accounting, err := p.assessSamplingRequest(ctx, adapter, preview, window, policy, frame)
	if err != nil {
		return modelCandidate{}, err
	}
	candidate.assessment, candidate.modelWindow = assessment, accounting.window
	candidate.omitLocalTools = len(original.Tools) > 0 && len(preview.Tools) == 0
	physical, reservation, err := p.budget.ports.PlanModel(ctx, budget.CloneLedger(ledger), model.CloneRequest(original))
	if err != nil && !errors.Is(err, runtimecontext.ErrBudgetExceeded) {
		return modelCandidate{}, err
	}
	admissionErr := err
	if !validDispatchModelRequest(original, physical) {
		return modelCandidate{}, ErrInvalidRequest
	}
	if !reflect.DeepEqual(model.CloneRequest(preview), model.CloneRequest(physical)) {
		assessment, accounting, err = p.assessSamplingRequest(ctx, adapter, physical, window, policy, frame)
		if err != nil {
			return modelCandidate{}, err
		}
	}
	candidate.assessment, candidate.modelWindow = assessment, accounting.window
	candidate.omitLocalTools = len(original.Tools) > 0 && len(physical.Tools) == 0
	if admissionErr != nil {
		if !runtimecontext.WithinHardBudget(assessment, policy) {
			return candidate, nil
		}
		return modelCandidate{}, admissionErr
	}
	if runtimecontext.WithinHardBudget(assessment, policy) {
		plan, err := newSamplingPlan(ledger, original, physical, reservation, accounting.window, policy, accounting.frame, assessment)
		if err != nil {
			return modelCandidate{}, err
		}
		candidate.invocation.SamplingPlan = plan
	}
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
