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

// A candidate disposition joins Context and SharedBudget feasibility.
// Pressure may be relieved by another transcript candidate; a permanent
// minimum-quota denial cannot. Waiting is permanently feasible but not yet
// executable; the durable intent retains the plan until native admission.
type candidateDisposition uint8

const (
	candidateAdmitted candidateDisposition = iota + 1
	candidateWaiting
	candidatePressure
	candidateExhausted
)

type candidateQuota struct {
	LedgerID  string
	Revision  uint64
	Requested budget.Usage
	Dimension budget.Dimension
	err       error
}

func previewCandidateQuota(ledger budget.Ledger, reservation budget.Reservation) (candidateQuota, error) {
	err := ledger.PreviewReservation(reservation, true)
	if err != nil && !errors.Is(err, budget.ErrExhausted) && !errors.Is(err, budget.ErrWaiting) {
		return candidateQuota{}, err
	}
	dimension, _ := budget.DeniedDimension(err)
	return candidateQuota{LedgerID: ledger.ID, Revision: ledger.Revision, Requested: reservation.Requested, Dimension: dimension, err: err}, nil
}

// modelCandidate couples the decision to the exact request measured by
// Runtime, including its final InvocationID. Assessing a candidate neither
// reserves quota nor writes Context or authorizes dispatch.
type modelCandidate struct {
	disposition    candidateDisposition
	quota          candidateQuota
	assessment     runtimecontext.Assessment
	modelWindow    runtimecontext.ModelWindow
	omitLocalTools bool
	request        model.Request
	invocation     *ModelInvocation
	preparation    *modelPreparation
}

func (c modelCandidate) selectable() bool {
	return c.disposition == candidateAdmitted || c.disposition == candidateWaiting
}

func (c modelCandidate) soft() bool {
	return c.selectable() && c.assessment.HardTokenEstimate <= c.assessment.SoftInputTokens
}

func (c *modelCandidate) decide(policy runtimecontext.Policy) {
	switch {
	case errors.Is(c.quota.err, budget.ErrExhausted):
		if c.quota.Dimension == budget.DimensionInputTokens || c.quota.Dimension == budget.DimensionTotalTokens {
			c.disposition = candidatePressure
		} else {
			c.disposition = candidateExhausted
		}
	case !runtimecontext.WithinHardBudget(c.assessment, policy):
		c.disposition = candidatePressure
	case errors.Is(c.quota.err, budget.ErrWaiting):
		c.disposition = candidateWaiting
	default:
		c.disposition = candidateAdmitted
	}
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
	}
	return assessInvocationCandidate(ctx, adapter, candidate, measured, window, policy, frame)
}

// Both business and summary use the same physical assessment and reservation
// logic after their purpose-specific logical invocation has been constructed.
// A candidate remains non-authoritative until the original intent and grant
// are committed by the Runner.
func assessInvocationCandidate(ctx context.Context, adapter *BoundedContextAdapter, candidate modelCandidate, measured model.Request,
	window runtimecontext.ModelWindow, policy runtimecontext.Policy, frame runtimecontext.FrozenAccounting) (modelCandidate, error) {
	if preparation := candidate.preparation; preparation != nil && preparation.budget != nil {
		ledger, err := preparation.samplingLedger(ctx)
		if err != nil {
			return modelCandidate{}, err
		}
		if ledger.ID != "" {
			return preparation.assessBudgetCandidate(ctx, adapter, candidate, ledger, window, policy, frame)
		}
	}
	assessment, err := adapter.manager.AssessModelRequestWithAccounting(ctx, measured, window, policy, frame)
	if err != nil {
		return modelCandidate{}, err
	}
	candidate.assessment = assessment
	candidate.decide(policy)
	// A Context-only admission has the same exact physical shape contract.
	// No later middleware may narrow or otherwise invalidate its measured header.
	if candidate.selectable() && candidate.invocation != nil {
		plan, err := newPhysicalSamplingPlan(candidate.invocation.Request, measured)
		if err != nil {
			return modelCandidate{}, err
		}
		plan.bindContext(window, policy, frame, assessment)
		if !plan.valid(candidate.invocation.Request) {
			return modelCandidate{}, ErrInvalidRequest
		}
		candidate.invocation.SamplingPlan = plan
	}
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

// Preview and final planning are the same SharedBudget algorithm. A pure
// reservation check distinguishes permanent minimum exhaustion, candidate
// pressure, and transient contention before any Context or intent write.
func (p *modelPreparation) assessBudgetCandidate(ctx context.Context, adapter *BoundedContextAdapter, candidate modelCandidate, ledger budget.Ledger,
	window runtimecontext.ModelWindow, policy runtimecontext.Policy, frame runtimecontext.FrozenAccounting) (modelCandidate, error) {
	original := candidate.invocation.Request
	hash, err := model.PhysicalRequestHash(original)
	if err != nil {
		return modelCandidate{}, err
	}
	minimum := budget.Reservation{ID: "model:" + original.InvocationID, RunID: original.RunID,
		RequestHash: hash, Requested: budget.Usage{LLMCalls: 1}}
	if _, exists := ledger.Reservations[minimum.ID]; exists {
		return modelCandidate{}, budget.ErrLedgerConflict
	}
	tokenLimited := ledger.HasRunTokenLimits(original.RunID)
	if tokenLimited {
		minimum.Requested.InputTokens, minimum.Requested.OutputTokens, minimum.Requested.TotalTokens = 1, 1, 2
	}
	quota, err := previewCandidateQuota(ledger, minimum)
	if err != nil {
		return modelCandidate{}, err
	}
	if errors.Is(quota.err, budget.ErrExhausted) {
		candidate.quota, candidate.disposition = quota, candidateExhausted
		return candidate, nil
	}

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
		// A Host failure by itself is not a Runtime admission decision.
		// Only a separately measured hard Context rejection can certify it.
		if !runtimecontext.WithinHardBudget(assessment, policy) {
			candidate.disposition = candidatePressure
			return candidate, nil
		}
		return modelCandidate{}, admissionErr
	}
	if !validFreshSamplingReservation(original, reservation) ||
		tokenLimited != (reservation.Requested.InputTokens > 0) {
		return modelCandidate{}, ErrInvalidRequest
	}
	quota, err = previewCandidateQuota(ledger, reservation)
	if err != nil {
		return modelCandidate{}, err
	}
	candidate.quota = quota
	candidate.decide(policy)
	if candidate.selectable() {
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
	if !candidate.selectable() {
		return model.Request{}, ErrInvalidRequest
	}
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
// uses the same identity constructor. A selected SharedBudget freezes its
// physical sampling restrictions even without a Context assessment.
func (p *modelPreparation) finish(ctx context.Context, request model.Request) (ModelInvocation, error) {
	p.mu.Lock()
	p.closed = true
	chosen := p.selected
	p.mu.Unlock()
	if chosen == nil {
		invocation, err := p.candidate(request)
		if err != nil || p.budget == nil {
			return invocation, err
		}
		ledger, err := p.samplingLedger(ctx)
		if err != nil || ledger.ID == "" {
			return invocation, err
		}
		original := invocation.Request
		physical, reservation, err := p.budget.ports.PlanModel(ctx, budget.CloneLedger(ledger), model.CloneRequest(original))
		if err != nil {
			return ModelInvocation{}, err
		}
		if ledger.HasRunTokenLimits(original.RunID) != (reservation.Requested.InputTokens > 0) {
			return ModelInvocation{}, ErrInvalidRequest
		}
		invocation.SamplingPlan, err = newSamplingPlan(ledger, original, physical, reservation,
			runtimecontext.ModelWindow{}, runtimecontext.Policy{}, runtimecontext.FrozenAccounting{}, runtimecontext.Assessment{})
		return invocation, err
	}
	selected := *chosen
	projected := model.CloneRequest(selected.Request)
	projected.InvocationID = p.original.InvocationID
	if !reflect.DeepEqual(projected, model.CloneRequest(request)) || !matchesFrozenProviderBinding(p.client, selected) {
		return ModelInvocation{}, ErrInvalidRequest
	}
	return selected, nil
}
