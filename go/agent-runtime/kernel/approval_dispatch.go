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
// later terminal state or a decision supplied by the observer. The original
// callback/transfer retirement is a separate obligation.
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
	var after int64
	found := false
	for after < source.EventHead {
		events, err := r.ListEvents(ctx, runID, after, 512)
		if err != nil {
			return false, err
		}
		if len(events) == 0 {
			return false, ErrApplyOutcomeUnknown
		}
		for _, event := range events {
			if event.Seq != after+1 {
				return false, ErrApplyOutcomeUnknown
			}
			after = event.Seq
			if after > source.EventHead {
				break
			}
			if event.Type != approvalDispatchEvent || event.Message != id {
				continue
			}
			var fact approvalDispatchFact
			if found || jsoncontract.ValidateUniqueJSONObject(event.Data) != nil || json.Unmarshal(event.Data, &fact) != nil || fact.SourceRevision == 0 || fact.SourceRevision >= source.Run.Revision || fact.Decision != expected {
				return false, ErrExecutionConflict
			}
			found = true
		}
	}
	return found, nil
}
