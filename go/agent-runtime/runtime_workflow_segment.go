package agentruntime

import (
	"encoding/json"
	"time"

	model "github.com/orz-i/Gaoge/sdk/go/agent-runtime/domain"
)

const (
	defaultWorkflowSegmentNodeActivations = 256
	defaultWorkflowSegmentDurationMS      = 250
	defaultWorkflowSegmentEffects         = 1
	defaultWorkflowSegmentTransitionBytes = 1 << 20
)

type workflowSegmentPolicy struct {
	maxNodeActivations int
	maxDuration        time.Duration
	maxEffects         int
	maxTransitionBytes int
}

func (r *workflowRunner) ensureWorkflowSegmentState() {
	if r == nil {
		return
	}
	if r.segment.policy.maxEffects <= 0 {
		r.segment.policy = r.service.workflowSegmentPolicy()
	}
	if r.segment.startedAt.IsZero() {
		r.segment.startedAt = r.service.now()
		r.segment.startActivations = r.budget.NodeActivations
	}
}

type workflowSegmentState struct {
	startedAt        time.Time
	startActivations int
	effectClaims     int
	yieldReason      string
	transitionBytes  int
	policy           workflowSegmentPolicy
}

func (s *Engine) workflowSegmentPolicy() workflowSegmentPolicy {
	var cfg WorkflowConfig
	if s != nil && s.cfg != nil {
		cfg = s.cfg.Snapshot().Workflow
	}
	return workflowSegmentPolicy{
		maxNodeActivations: positiveWorkflowCeiling(cfg.MaxSegmentNodeActivations, defaultWorkflowSegmentNodeActivations),
		maxDuration:        time.Duration(positiveWorkflowCeiling(cfg.MaxSegmentDurationMS, defaultWorkflowSegmentDurationMS)) * time.Millisecond,
		maxEffects:         positiveWorkflowCeiling(cfg.MaxSegmentEffects, defaultWorkflowSegmentEffects),
		maxTransitionBytes: positiveWorkflowCeiling(cfg.MaxSegmentTransitionBytes, defaultWorkflowSegmentTransitionBytes),
	}
}

func (r *workflowRunner) workflowDeadlineExceededAt(now time.Time) bool {
	if r == nil || r.budget.Limits.MaxDurationSeconds <= 0 || r.run.StartedAt.IsZero() {
		return false
	}
	return !now.Before(r.run.StartedAt.Add(time.Duration(r.budget.Limits.MaxDurationSeconds) * time.Second))
}

func (r *workflowRunner) shouldYieldWorkflowSegment() bool {
	r.ensureWorkflowSegmentState()
	if r == nil || r.segment.yieldReason != "" {
		return r != nil && r.segment.yieldReason != ""
	}
	if !r.progress {
		return false
	}
	if r.budget.NodeActivations-r.segment.startActivations >= r.segment.policy.maxNodeActivations {
		r.segment.yieldReason = "node_activation_limit"
		return true
	}
	if r.service.now().Sub(r.segment.startedAt) >= r.segment.policy.maxDuration {
		r.segment.yieldReason = "wall_clock_limit"
		return true
	}
	transitionBytes, err := r.workflowTransitionBytes()
	if err == nil {
		r.segment.transitionBytes = transitionBytes
		if transitionBytes >= r.segment.policy.maxTransitionBytes {
			r.segment.yieldReason = "transition_bytes_limit"
			return true
		}
	}
	return false
}

func (r *workflowRunner) claimWorkflowEffectForDispatch(effect workflowEffectState) bool {
	r.ensureWorkflowSegmentState()
	if r.segment.effectClaims >= r.segment.policy.maxEffects {
		r.segment.yieldReason = "external_effect_limit"
		r.progress = true
		return false
	}
	effect.Status = workflowEffectStatusDispatching
	effect.DispatchAttempt++
	r.state.Effects[effect.EffectID] = effect
	r.dispatchEffectID = effect.EffectID
	r.segment.effectClaims++
	r.progress = true
	return true
}

func (r *workflowRunner) workflowTransitionBytes() (int, error) {
	value := struct {
		State        workflowRuntimeState
		Budget       model.WorkflowBudget
		Steps        []model.Step
		Interactions []model.Interaction
		Events       []model.Event
		CacheEntries []model.WorkflowCacheEntry
	}{
		State: r.state, Budget: r.budget, Steps: r.transitionSteps(),
		Interactions: r.interactionRows, Events: r.events, CacheEntries: r.cacheEntries,
	}
	raw, err := json.Marshal(value)
	return len(raw), err
}

func (r *workflowRunner) appendWorkflowSegmentYieldEvent() {
	if r.segment.yieldReason == "" {
		return
	}
	r.events = append(r.events, newRunEvent(r.run, "workflow.segment.yielded", r.run.CurrentStepID, r.segment.yieldReason, map[string]interface{}{
		"reason":          r.segment.yieldReason,
		"nodeActivations": r.budget.NodeActivations - r.segment.startActivations,
		"elapsedMS":       r.service.now().Sub(r.segment.startedAt).Milliseconds(),
		"effectClaims":    r.segment.effectClaims,
		"transitionBytes": r.segment.transitionBytes,
	}, nil))
}
