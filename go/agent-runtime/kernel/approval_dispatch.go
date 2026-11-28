package kernel

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/jsoncontract"
)

// An original approval control CAS commits its decision and the original
// physical Grant correlation together. Later Tool/Model reducers may remove
// the resolved Checkpoint; this journal fact remains authoritative.
const approvalDispatchEvent = "kernel.approval.dispatch_committed"

type approvalDispatchFact struct {
	SourceRevision uint64                  `json:"sourceRevision"`
	Decision       ManagedApprovalDecision `json:"decision"`
}

func (r *Runtime) stampApprovalDispatch(current Snapshot, next *StoreMutation) error {
	g := r.physicalGrant
	if g == nil || !g.IsOwned() || g.AdmissionOperation != "approval" ||
		!strings.HasPrefix(g.ID, ownedDispatchGrantPrefix) {
		return nil
	}
	var response struct {
		Decision string `json:"decision"`
		Comment  string `json:"comment,omitempty"`
	}
	if next.Record.Checkpoint == nil || jsoncontract.ValidateUniqueJSONObject(next.Record.Checkpoint.Response) != nil || json.Unmarshal(next.Record.Checkpoint.Response, &response) != nil {
		return ErrInvalidInput
	}
	decision := ManagedApprovalDecision{CheckpointID: next.Record.Checkpoint.ID, Decision: response.Decision, Comment: response.Comment}
	if !decision.Valid() {
		return ErrInvalidInput
	}
	raw, err := json.Marshal(approvalDispatchFact{SourceRevision: current.Run.Revision, Decision: decision})
	if err != nil {
		return err
	}
	next.Events = append(next.Events, EventDraft{Type: approvalDispatchEvent, Message: g.ID, Data: raw})
	return nil
}

// CommittedApprovalDispatch reads the exact native approval fact, never a
// later terminal state or a decision supplied by the observer.
func (r *Runtime) CommittedApprovalDispatch(ctx context.Context, runID, dispatchID string, expected ManagedApprovalDecision) (bool, error) {
	if r == nil || ctx == nil || !expected.Valid() {
		return false, ErrInvalidInput
	}
	id, err := OwnedPhysicalDispatchGrantID(runID, "approval", dispatchID)
	if err != nil {
		return false, err
	}
	source, err := r.Load(ctx, runID)
	if err != nil {
		return false, err
	}
	found := false
	err = r.visitCommittedApprovals(ctx, source, func(grant string, decision ManagedApprovalDecision) error {
		if grant != id {
			return nil
		}
		if found || decision != expected {
			return ErrExecutionConflict
		}
		found = true
		return nil
	})
	return found, err
}

// NativeApprovalDecisions observes only approval facts committed by native
// commands without a Host dispatch Item. Other approval projections retain
// their original external/legacy contract.
func (r *Runtime) NativeApprovalDecisions(ctx context.Context, source Snapshot) ([]ManagedApprovalDecision, error) {
	var decisions []ManagedApprovalDecision
	seen := map[string]bool{}
	err := r.visitCommittedApprovals(ctx, source, func(grant string, decision ManagedApprovalDecision) error {
		if !strings.HasPrefix(grant, ownedNativeCommandGrantPrefix) {
			return nil
		}
		if seen[decision.CheckpointID] {
			return ErrExecutionConflict
		}
		seen[decision.CheckpointID] = true
		decisions = append(decisions, decision)
		return nil
	})
	return decisions, err
}

func (r *Runtime) visitCommittedApprovals(ctx context.Context, source Snapshot, visit func(string, ManagedApprovalDecision) error) error {
	if r == nil || ctx == nil || source.Run.ID == "" {
		return ErrInvalidInput
	}
	var after int64
	for after < source.EventHead {
		events, err := r.ListEvents(ctx, source.Run.ID, after, 512)
		if err != nil {
			return err
		}
		if len(events) == 0 {
			return ErrApplyOutcomeUnknown
		}
		for _, event := range events {
			if event.Seq != after+1 {
				return ErrApplyOutcomeUnknown
			}
			after = event.Seq
			if after > source.EventHead {
				break
			}
			if event.Type != approvalDispatchEvent {
				continue
			}
			var fact approvalDispatchFact
			if !strings.HasPrefix(event.Message, ownedDispatchGrantPrefix) || jsoncontract.ValidateUniqueJSONObject(event.Data) != nil || json.Unmarshal(event.Data, &fact) != nil || fact.SourceRevision == 0 || fact.SourceRevision >= source.Run.Revision || !fact.Decision.Valid() {
				return ErrExecutionConflict
			}
			if err = visit(event.Message, fact.Decision); err != nil {
				return err
			}
		}
	}
	return nil
}
