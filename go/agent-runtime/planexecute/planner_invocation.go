package planexecute

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

type PlannerInvocationStatus string

const (
	PlannerInvocationPending   PlannerInvocationStatus = "pending"
	PlannerInvocationCompleted PlannerInvocationStatus = "completed"
	PlannerInvocationConsumed  PlannerInvocationStatus = "consumed"

	plannerInvocationLeaseDuration = 2 * time.Minute
)

// PlannerInvocation is the PlanExecute-owned durable intent and receipt for one
// logical planning call. Physical Planner execution may repeat after a crash,
// but the same stable invocation identity is reused and only one receipt may be
// consumed into a Plan.
type PlannerInvocation struct {
	ID                  string                  `json:"id"`
	RunID               string                  `json:"runID"`
	SourceRevision      uint64                  `json:"sourceRevision"`
	RequestHash         string                  `json:"requestHash"`
	Status              PlannerInvocationStatus `json:"status"`
	Request             PlannerRequest          `json:"request,omitempty"`
	ResponseID          string                  `json:"responseID,omitempty"`
	Draft               PlanDraft               `json:"draft,omitempty"`
	ExecutionAttempt    uint32                  `json:"executionAttempt,omitempty"`
	ExecutionLeaseUntil *time.Time              `json:"executionLeaseUntil,omitempty"`
	CreatedAt           time.Time               `json:"createdAt"`
	CompletedAt         *time.Time              `json:"completedAt,omitempty"`
	ConsumedAt          *time.Time              `json:"consumedAt,omitempty"`
}

func newPlannerInvocation(request PlannerRequest, sourceRevision uint64, now time.Time) (PlannerInvocation, error) {
	request = clonePlannerRequest(request)
	request.InvocationID = ""
	hash, err := plannerRequestHash(request)
	if err != nil {
		return PlannerInvocation{}, err
	}
	material := request.RunID + "\x00" + strconv.FormatUint(sourceRevision, 10) + "\x00" + hash
	digest := sha256.Sum256([]byte(material))
	id := "plannerinv_" + hex.EncodeToString(digest[:16])
	request.InvocationID = id
	return PlannerInvocation{
		ID: id, RunID: request.RunID, SourceRevision: sourceRevision, RequestHash: hash,
		Status: PlannerInvocationPending, Request: request, CreatedAt: now.UTC(),
	}, nil
}

func plannerRequestHash(request PlannerRequest) (string, error) {
	request = clonePlannerRequest(request)
	request.InvocationID = ""
	encoded, err := json.Marshal(request)
	if err != nil {
		return "", errors.Join(ErrInvalidRequest, err)
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func clonePlannerRequest(request PlannerRequest) PlannerRequest {
	request.AllowedToolKeys = cloneOptionalStrings(request.AllowedToolKeys)
	return request
}

func clonePlanDraft(draft PlanDraft) PlanDraft {
	draft.Steps = append([]StepDraft(nil), draft.Steps...)
	for index := range draft.Steps {
		draft.Steps[index].ToolKeys = append([]string(nil), draft.Steps[index].ToolKeys...)
	}
	return draft
}

func clonePlannerInvocation(value *PlannerInvocation) *PlannerInvocation {
	if value == nil {
		return nil
	}
	cloned := *value
	cloned.Request = clonePlannerRequest(value.Request)
	cloned.Draft = clonePlanDraft(value.Draft)
	if value.ExecutionLeaseUntil != nil {
		leaseUntil := value.ExecutionLeaseUntil.UTC()
		cloned.ExecutionLeaseUntil = &leaseUntil
	}
	if value.CompletedAt != nil {
		completedAt := value.CompletedAt.UTC()
		cloned.CompletedAt = &completedAt
	}
	if value.ConsumedAt != nil {
		consumedAt := value.ConsumedAt.UTC()
		cloned.ConsumedAt = &consumedAt
	}
	return &cloned
}

func validPlannerInvocation(value *PlannerInvocation) bool {
	if value == nil || strings.TrimSpace(value.ID) == "" || strings.TrimSpace(value.RunID) == "" ||
		value.SourceRevision == 0 || len(value.RequestHash) != sha256.Size*2 || value.CreatedAt.IsZero() {
		return false
	}
	switch value.Status {
	case PlannerInvocationPending:
		if strings.TrimSpace(value.Request.InvocationID) != value.ID || value.CompletedAt != nil || value.ConsumedAt != nil {
			return false
		}
		if value.ExecutionLeaseUntil != nil && value.ExecutionAttempt == 0 {
			return false
		}
		hash, err := plannerRequestHash(value.Request)
		return err == nil && hash == value.RequestHash
	case PlannerInvocationCompleted:
		if strings.TrimSpace(value.Request.InvocationID) != value.ID || value.CompletedAt == nil ||
			value.ConsumedAt != nil || value.ExecutionLeaseUntil != nil {
			return false
		}
		hash, err := plannerRequestHash(value.Request)
		return err == nil && hash == value.RequestHash
	case PlannerInvocationConsumed:
		return value.CompletedAt != nil && value.ConsumedAt != nil && value.ExecutionLeaseUntil == nil
	default:
		return false
	}
}

func (runner *Runner) advancePlannerInvocation(
	ctx context.Context,
	snapshot kernel.Snapshot,
	state executionState,
) (kernel.Snapshot, error) {
	invocation := clonePlannerInvocation(state.PlannerInvocation)
	if invocation == nil {
		return snapshot, ErrInvalidRequest
	}
	if invocation.Status == PlannerInvocationPending {
		now := runner.runtime.Now().UTC()
		if invocation.ExecutionLeaseUntil != nil && now.Before(invocation.ExecutionLeaseUntil.UTC()) {
			return snapshot, ErrPlannerInvocationBusy
		}
		leaseUntil := now.Add(plannerInvocationLeaseDuration)
		invocation.ExecutionAttempt++
		invocation.ExecutionLeaseUntil = &leaseUntil
		state.PlannerInvocation = clonePlannerInvocation(invocation)
		encoded, err := encodeState(state)
		if err != nil {
			return snapshot, err
		}
		claimed, err := runner.runtime.Apply(ctx, snapshot.Run.ID, snapshot.Run.Revision, kernel.Mutation{
			Status: kernel.RunStatusRunning, State: encoded, Checkpoint: snapshot.Checkpoint,
			Events: []kernel.EventDraft{{
				Type: "planexecute.planner.claimed", Message: invocation.ID, Wakeup: true, WakeupAt: &leaseUntil,
			}},
		})
		if err != nil {
			return snapshot, err
		}
		snapshot = claimed
		response, generateErr := runner.planner.GeneratePlan(ctx, clonePlannerRequest(invocation.Request))
		if generateErr != nil {
			var retryable interface{ Retryable() bool }
			if errors.As(generateErr, &retryable) && retryable.Retryable() {
				return runner.releasePlannerInvocation(ctx, snapshot, state, generateErr)
			}
			return runner.fail(ctx, snapshot, state, "planexecute.planner_failed", errors.Join(ErrPlannerFailure, generateErr))
		}
		completedAt := runner.runtime.Now().UTC()
		invocation.Status = PlannerInvocationCompleted
		invocation.ResponseID = strings.TrimSpace(response.ResponseID)
		invocation.Draft = clonePlanDraft(response.Draft)
		invocation.ExecutionLeaseUntil = nil
		invocation.CompletedAt = &completedAt
		state.PlannerInvocation = clonePlannerInvocation(invocation)
		encoded, err = encodeState(state)
		if err != nil {
			return snapshot, err
		}
		completed, err := runner.runtime.Apply(ctx, snapshot.Run.ID, snapshot.Run.Revision, kernel.Mutation{
			Status: kernel.RunStatusRunning, State: encoded, Checkpoint: snapshot.Checkpoint,
			Events: []kernel.EventDraft{{Type: "planexecute.planner.completed", Message: invocation.ID, Wakeup: true}},
		})
		if err != nil {
			return snapshot, err
		}
		snapshot = completed
	}

	state, err := decodeState(snapshot.State)
	if err != nil {
		return snapshot, err
	}
	invocation = clonePlannerInvocation(state.PlannerInvocation)
	if invocation == nil || invocation.Status != PlannerInvocationCompleted {
		return snapshot, ErrInvalidRequest
	}
	consumedAt := runner.runtime.Now().UTC()
	invocation.Status = PlannerInvocationConsumed
	invocation.ConsumedAt = &consumedAt
	state.PlannerInvocation = clonePlannerInvocation(invocation)
	materialized, err := runner.materializePlan(
		invocation.Draft, state.Model, state.ApprovalPolicy, invocation.Request.MaxSteps, state.AllowedToolKeys,
	)
	if err != nil {
		return runner.fail(ctx, snapshot, state, "planexecute.plan_invalid", err)
	}
	materialized.PlannerInvocation = clonePlannerInvocation(invocation)
	return runner.persistPlan(ctx, snapshot, materialized)
}

func (runner *Runner) releasePlannerInvocation(ctx context.Context, snapshot kernel.Snapshot, state executionState, cause error) (kernel.Snapshot, error) {
	state.PlannerInvocation.ExecutionLeaseUntil = nil
	encoded, err := encodeState(state)
	if err != nil {
		return snapshot, err
	}
	updated, err := runner.runtime.Apply(ctx, snapshot.Run.ID, snapshot.Run.Revision, kernel.Mutation{
		Status: kernel.RunStatusRunning, State: encoded, Checkpoint: snapshot.Checkpoint,
		Events: []kernel.EventDraft{{Type: "planexecute.planner.waiting", Message: cause.Error(), Wakeup: true, WakeupAt: planWakeupAt(runner.runtime.Now())}},
	})
	return updated, errors.Join(ErrPlannerPending, err)
}
