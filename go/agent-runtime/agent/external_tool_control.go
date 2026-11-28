package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/internal/featurecontrol"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

// ExternalToolControl identifies the original pending physical call and the
// already elected host cancellation. ParentRunID is the immediate frozen
// Harness delegation parent. Deferred members have independent native Runs.
type ExternalToolControl struct {
	Actor       kernel.ActorRef `json:"actor"`
	ParentRunID string          `json:"parentRunID,omitempty"`
	RunID       string          `json:"runID"`
	CallID      string          `json:"callID"`
	OperationID string          `json:"operationID"`
	InputDigest string          `json:"inputDigest"`
	ControlID   string          `json:"controlID"`
}

// ExternalToolNoEffectReceipt is the owner's durable proof that its
// original operation was withdrawn before starting.
type ExternalToolNoEffectReceipt struct {
	ExternalToolControl
	ReceiptID string `json:"receiptID"`
}

// ExternalToolNoEffectVerifier is installed at composition. It only reads
// the original owner's elected cancellation and its stored no-effect receipt.
// It must never cancel, launch, retry or infer a receipt from missing data.
type ExternalToolNoEffectVerifier interface {
	VerifyExternalToolNoEffect(context.Context, tools.ExecutionRequest, ExternalToolControl) (ExternalToolNoEffectReceipt, error)
}

type externalToolCancellation struct {
	Receipt        ExternalToolNoEffectReceipt `json:"receipt"`
	Call           tools.Call                  `json:"call"`
	GrantID        string                      `json:"grantID"`
	SourceRevision uint64                      `json:"sourceRevision"`
	SourceHash     string                      `json:"sourceHash"`
}

func controlIdentity(value string) bool {
	return value != "" && len(value) <= 128 && strings.TrimSpace(value) == value
}

type originalExternalControlWait struct {
	snapshot   kernel.Snapshot
	state      runState
	grant      kernel.ChildExecutionGrant
	request    tools.ExecutionRequest
	returnHash string
}

// VerifyExternalToolControlWait checks native callback return before the owner
// elects a no-effect receipt. The owner independently checks its own election.
func (r *Runner) VerifyExternalToolControlWait(ctx context.Context, revision uint64, control ExternalToolControl) error {
	_, err := r.externalControlWait(ctx, revision, control)
	return err
}

func (r *Runner) externalControlWait(ctx context.Context, revision uint64, c ExternalToolControl) (originalExternalControlWait, error) {
	var out originalExternalControlWait
	if r == nil || ctx == nil || revision == 0 || !controlIdentity(c.ControlID) || c.Actor.TenantID == "" || c.Actor.ActorID == "" {
		return out, ErrInvalidRequest
	}
	if r.externalNoEffectVerifier == nil {
		return out, ErrToolVerificationRequired
	}
	s, state, err := r.loadState(ctx, c.RunID)
	if err != nil {
		return out, err
	}
	out.snapshot, out.state = s, state
	if s.Run.Kind != RunKind || s.Run.Actor != c.Actor || s.Run.ParentRunID != "" {
		return out, ErrToolReceiptMismatch
	}
	if previous := state.ExternalToolCancellation; previous != nil {
		if previous.Receipt.ExternalToolControl != c {
			return out, ErrToolReceiptMismatch
		}
		out.request = tools.ExecutionRequest{RunID: c.RunID, Call: tools.CloneCall(previous.Call)}
		if previous.Call.ID != c.CallID || !previous.validSource(s) {
			return out, ErrToolReceiptMismatch
		}
		if revision != s.Run.Revision && revision != previous.SourceRevision {
			return out, kernel.ErrConflict
		}
		return out, r.verifyRecordedControl(ctx, s, previous)
	}
	if s.Run.Revision != revision {
		return out, kernel.ErrConflict
	}
	call, waiting, err := FrozenExternalToolCall(s)
	if err != nil || !waiting || len(state.DeferredChildren) != 0 {
		return out, errors.Join(ErrToolOutcomeUnknown, err)
	}
	wait := state.ToolWait
	if wait.SignalID != "" || call.ID != c.CallID || wait.OperationID != c.OperationID || wait.InputDigest != c.InputDigest {
		return out, ErrToolReceiptMismatch
	}
	g, err := r.runtime.LookupChildExecution(ctx, c.RunID)
	if err != nil {
		return out, errors.Join(ErrToolOutcomeUnknown, err)
	}
	out.grant = g
	out.request = tools.ExecutionRequest{RunID: c.RunID, Call: tools.CloneCall(call)}
	completion, err := r.runtime.LookupOwnedPhysicalCallbackReturn(ctx, g)
	if err != nil || !completion.HostCompleted || completion.GrantID != g.ID || completion.ReturnRevision != revision {
		return out, errors.Join(ErrRootOutcomeUnknown, err)
	}
	if completion.ControlCustody != kernel.OwnedCustodyScoped && completion.ControlCustody != kernel.OwnedBoundedApprovalReturn {
		return out, ErrRootOutcomeUnknown
	}
	outcome := kernel.OwnedPhysicalCallbackOutcome{HostCompleted: true, LeaseOwned: completion.LeaseOwned, ControlCustody: completion.ControlCustody, ToolReturns: completion.ToolReturns}
	if binding, found, bindingErr := originalCallReturn(s, completion, out.request); bindingErr != nil {
		return out, errors.Join(ErrRootOutcomeUnknown, bindingErr)
	} else if found && (binding.ReceiptDigest != "" ||
		(binding.LedgerID != "" && (state.ExternalToolHold == nil || state.ExternalToolHold.LedgerID != binding.LedgerID)) ||
		(binding.LedgerID == "" && state.ExternalToolHold != nil)) {
		return out, ErrToolReceiptMismatch
	}
	out.returnHash = kernel.ManagedDigest(kernel.OwnedPhysicalCallbackReturnPayload(s, outcome))
	if out.returnHash != completion.ReturnSourceHash {
		return out, ErrRootOutcomeUnknown
	}
	return out, nil
}

// ReconcileExternalToolNoEffectForControl records only the original no-effect
// receipt. The native Run stays fenced against Resume until the elected control
// cancels it. A retry after a lost response uses the same durable receipt.
func (r *Runner) ReconcileExternalToolNoEffectForControl(ctx context.Context, revision uint64, control ExternalToolControl) (kernel.Snapshot, error) {
	original, err := r.externalControlWait(ctx, revision, control)
	if err != nil {
		return original.snapshot, err
	}
	receipt, err := r.externalNoEffectVerifier.VerifyExternalToolNoEffect(ctx, tools.ExecutionRequest{RunID: control.RunID, Call: tools.CloneCall(original.request.Call)}, control)
	if err != nil {
		return original.snapshot, errors.Join(ErrToolOutcomeUnknown, err)
	}
	if receipt.ExternalToolControl != control || !controlIdentity(receipt.ReceiptID) {
		return original.snapshot, ErrToolReceiptMismatch
	}
	if previous := original.state.ExternalToolCancellation; previous != nil {
		if previous.Receipt != receipt {
			return original.snapshot, ErrToolReceiptMismatch
		}
		return original.snapshot, r.retireExternalToolControl(ctx, original.snapshot, previous.GrantID)
	}
	state := original.state
	if hold := state.ExternalToolHold; hold != nil {
		if hold.GrantID != original.grant.ID || hold.RequestHash != kernel.ManagedDigest(original.request) {
			return original.snapshot, ErrToolReceiptMismatch
		}
		raw, marshalErr := json.Marshal(receipt)
		if marshalErr != nil {
			return original.snapshot, marshalErr
		}
		if err = r.settleVerifiedToolReservation(ctx, original.request, func(id string) bool { return id == hold.LedgerID }, budget.Usage{}, raw); err != nil {
			return original.snapshot, errors.Join(ErrToolOutcomeUnknown, err)
		}
	}
	state.ToolWait, state.ExternalToolHold = nil, nil
	state.ToolDispatchID = ""
	state.clearToolAdmission(original.request.Call.ID)
	state.PendingCalls = remainingPendingCalls(state)
	state.ExternalToolCancellation = &externalToolCancellation{Receipt: receipt, Call: tools.CloneCall(original.request.Call), GrantID: original.grant.ID, SourceRevision: revision, SourceHash: original.returnHash}
	next, err := r.applyExternalControlState(ctx, original.snapshot, original.grant, state)
	if err != nil {
		return original.snapshot, err
	}
	return next, r.retireExternalToolControl(ctx, next, original.grant.ID)
}

func (r *Runner) applyExternalControlState(ctx context.Context, s kernel.Snapshot, g kernel.ChildExecutionGrant, state runState) (kernel.Snapshot, error) {
	encoded, err := encodeState(state)
	if err != nil {
		return s, err
	}
	m := kernel.Mutation{Status: s.Run.Status, State: encoded, Checkpoint: s.Checkpoint, ErrorCode: s.Run.ErrorCode, ErrorDetail: s.Run.ErrorDetail}

	source := featurecontrol.Seal(s.Run.ID, s.Run.Revision, "agent_owned_receipt_recovery_source", g.ID, s.State, s.Checkpoint)
	target := featurecontrol.Seal(s.Run.ID, s.Run.Revision, "agent_owned_receipt_recovery_target", g.ID, encoded, s.Checkpoint)
	return r.runtime.ApplyOwnedPhysicalRecovery(ctx, g, s.Run.Revision, m, source, target)
}

func (r *Runner) retireExternalToolControl(ctx context.Context, s kernel.Snapshot, grantID string) error {
	if !knownRootPhysicalSettlement(s) {
		return ErrRootOutcomeUnknown
	}
	g, err := r.runtime.LookupChildExecution(ctx, s.Run.ID)
	if errors.Is(err, kernel.ErrNotFound) {
		record, loadErr := r.runtime.LoadOwnedRetirement(ctx, grantID)
		if loadErr != nil {
			return loadErr
		}
		found, ackErr := observeExactOwnedRetirement(ctx, r.runtime, record.Evidence.Grant, s, &record.Evidence.Completion)
		if !found {
			return errors.Join(ErrRootOutcomeUnknown, ackErr)
		}
		return ackErr
	}
	if err != nil {
		return err
	}
	if g.ID != grantID {
		return ErrToolReceiptMismatch
	}
	return r.ReconcileRootPhysicalExecution(ctx, s.Run.ID)
}

// A state field alone cannot assert a previous callback return. Keep the
// original grant identity verifiable both before and after its retirement.
func (r *Runner) verifyRecordedControl(ctx context.Context, s kernel.Snapshot, p *externalToolCancellation) error {
	g, err := r.runtime.LookupChildExecution(ctx, s.Run.ID)
	var original kernel.OwnedPhysicalCallbackCompletion
	switch {
	case err == nil && g.ID == p.GrantID:
		original, err = r.runtime.LookupOwnedPhysicalCallbackReturn(ctx, g)
	case errors.Is(err, kernel.ErrNotFound):
		var record kernel.OwnedRetirementReceipt
		record, err = r.runtime.LoadOwnedRetirement(ctx, p.GrantID)
		if err == nil && kernel.ManagedDigest(record.Evidence.Snapshot) != kernel.ManagedDigest(s) {
			return ErrToolReceiptMismatch
		}
		original = record.Evidence.Completion
	default:
		return errors.Join(ErrRootOutcomeUnknown, err)
	}
	if err != nil || !original.HostCompleted || original.GrantID != p.GrantID || original.ReturnRevision != p.SourceRevision || original.ReturnSourceHash != p.SourceHash {
		return errors.Join(ErrRootOutcomeUnknown, err)
	}
	return nil
}

func (proof *externalToolCancellation) validSource(s kernel.Snapshot) bool {
	return proof.SourceRevision != 0 && proof.SourceRevision < s.Run.Revision &&
		proof.GrantID != "" && len(proof.SourceHash) == 64 && s.Run.Status == kernel.RunStatusRunning
}
