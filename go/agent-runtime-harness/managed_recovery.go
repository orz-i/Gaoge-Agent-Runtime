package harness

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"reflect"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

// NewManagedHostAuthority initializes storage identity, never execution proof.
// Concrete Stores call this only during initialization.
func NewManagedHostAuthority() (kernel.ManagedAuthority, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return kernel.ManagedAuthority{}, err
	}
	return kernel.ManagedAuthority{Namespace: hex.EncodeToString(b[:]), Generation: 1, Version: 1}, nil
}

// ManagedHostSource is the canonical immutable source projection. It is not a
// capability: the storage transaction must read and validate all original rows.
func ManagedHostSource(session Session, turn Turn, invocation Invocation, config ConfigSnapshot, dispatch any) (json.RawMessage, error) {
	return json.Marshal(struct {
		SessionID                  string
		HostThread                 HostRef
		Actor                      kernel.ActorRef
		TurnID                     string
		HostTurn                   HostRef
		Config                     ConfigSnapshot
		InvocationID, ParentItemID string

		Input                     json.RawMessage
		InputHash, ExecutionRefID string
		Attempt                   int
		Dispatch                  any
	}{session.ID, session.HostThread, session.Actor, turn.ID, turn.HostTurn, config,
		invocation.ID, invocation.ParentItemID,
		invocation.Input, invocation.InputHash, invocation.ExecutionRefID, invocation.Attempt, dispatch})
}

func nativeHostSource(session Session, turn Turn, inv Invocation, config ConfigSnapshot, b kernel.ManagedHostBinding) (json.RawMessage, error) {
	if _, err := canonicalSealedConfig(config); err != nil {
		return nil, err
	}
	return ManagedHostSource(session, turn, inv, config, struct {
		Correlation, Phase string
		Approval           *kernel.ManagedApprovalDecision
	}{b.DispatchID, b.Phase, b.Approval})
}

// ValidateManagedOriginalRows is shared by concrete storage transactions. It
// admits the exact original native Root dispatch, never a descendant or retry.
func ValidateManagedOriginalRows(authority kernel.ManagedAuthority, b kernel.ManagedHostBinding, session Session, turn Turn, invocation Invocation, config ConfigSnapshot, dispatch Item, items []Item) error {
	if !b.Valid() || b.HostAuthority != authority || session.ID == "" || turn.SessionID != session.ID ||
		turn.ID != b.TurnID || config.ID != turn.ConfigSnapshotID || config.TurnID != turn.ID ||
		invocation.ID != b.InvocationID || invocation.TurnID != turn.ID || invocation.ParentItemID != "" ||
		invocation.Attempt <= 0 || uint64(invocation.Attempt) != uint64(b.Attempt) ||
		invocation.ExecutionRefID != b.RunID {
		return ErrConflict
	}
	if b.DirectNativeCommand() {
		source, err := nativeHostSource(session, turn, invocation, config, b)
		if err != nil || !bytes.Equal(source, b.Source) {
			return ErrConflict
		}
		if dispatch.ID != "" || terminalTurnStatus(turn.Status) || terminalInvocationStatus(invocation.Status) || ActiveCancellationFence(items) || PendingExecutionDispatch(items, invocation.ID) {
			return ErrConflict
		}
		if b.Approval != nil {
			if err = verifyNativeApprovalProjection(turn.ID, invocation.ID, b.RunID, *b.Approval, items); err != nil {
				return err
			}
		}
		return nil
	}
	if dispatch.ID != b.DispatchID || dispatch.InvocationID != invocation.ID || dispatch.TurnID != turn.ID || dispatch.RunID != b.RunID {
		return ErrConflict
	}
	phase, ok := VerifiedExecutionDispatchGrant(dispatch)
	if !ok || phase != b.Phase {
		return ErrConflict
	}
	if b.Phase == "resolve-approval" {
		a, ok := originalDispatchApproval(dispatch)
		if !ok || !reflect.DeepEqual(a, b.Approval) {
			return ErrConflict
		}
		if items != nil {
			parent, _ := OriginalApprovalRequestID(dispatch)
			var request Item
			var children []Item
			for _, item := range items {
				if item.ID == parent {
					if request.ID != "" {
						return ErrConflict
					}
					request = item
				}
				if item.ParentItemID == parent {
					children = append(children, item)
				}
			}
			if _, _, err := originalApprovalProjection(dispatch, request, children, dispatch.CreatedAt); err != nil {
				return err
			}
		}
	}
	source, err := ManagedHostSource(session, turn, invocation, config, dispatch)
	if err != nil || !bytes.Equal(source, b.Source) {
		return ErrConflict
	}
	// No ordinary completion, including a malformed sibling, is compatible
	// with an unresolved original dispatch.
	receipts := executionDispatchReceiptIndex(items)
	for _, item := range items {
		if item.ParentItemID == dispatch.ID {
			return ErrConflict
		}
		if item.ParentItemID != ExecutionDispatchParentItemID || item.InvocationID != invocation.ID || item.ID == dispatch.ID {
			continue
		}
		// An open original is the sole unresolved dispatch. Historical
		// grants must have an exact receipt before the current one.
		if _, settled := VerifiedExecutionDispatchReceipt(item, receipts[item.ID]); !settled || item.Seq >= dispatch.Seq || receipts[item.ID][0].Seq >= dispatch.Seq {
			return ErrConflict
		}
	}
	return nil
}

func VerifyManagedRecoveryAdmission(b kernel.ManagedHostBinding, turn Turn, invocation Invocation, items []Item) error {
	if (!b.DirectNativeCommand() && !nativeManagedDispatchID(b.DispatchID)) || terminalTurnStatus(turn.Status) || terminalInvocationStatus(invocation.Status) || ActiveCancellationFence(items) {
		return ErrConflict
	}
	return nil
}

// ManagedClosureItems derives the fixed append-only projection from an opaque
// Kernel read proof. No returned-callback fact is attributed to the old owner.
func ManagedClosureItems(p kernel.ManagedClosureProof, r kernel.ManagedRecoveryAuthorization) ([]Item, error) {
	if !p.Matches(r) {
		return nil, ErrConflict
	}
	point := p.Transfer().SafePoint
	b := point.Host
	snap := p.Snapshot()
	boundary := "managed_dispatch_transferred"
	if r.Mode == kernel.ManagedCancel {
		boundary = "managed_dispatch_cancelled_before_next_effect"
	}
	payload, err := json.Marshal(struct {
		Boundary      string                     `json:"boundary"`
		RecoveryID    string                     `json:"recoveryID"`
		Mode          kernel.ManagedRecoveryMode `json:"mode"`
		SelectionHash string                     `json:"selectionHash"`
		TerminalHash  string                     `json:"terminalHash"`
	}{boundary, point.RecoveryID, r.Mode, kernel.ManagedDigest(r), kernel.ManagedDigest(snap)})
	if err != nil {
		return nil, err
	}
	now := snap.Run.UpdatedAt
	items := []Item{{ID: stableID("hdispatchdone", b.DispatchID), TurnID: b.TurnID, InvocationID: b.InvocationID, RunID: b.RunID, Kind: ItemDiagnostic, Status: ItemCompleted, ParentItemID: b.DispatchID, Payload: payload, CreatedAt: now, UpdatedAt: now}}

	if r.Mode == kernel.ManagedCancel && !r.Cancellation.Direct() {
		items = append(items, Item{ID: stableID("hcancelclosed", r.Cancellation.IntentID), TurnID: b.TurnID, RunID: b.RunID, Kind: ItemDiagnostic, Status: ItemCompleted, ParentItemID: r.Cancellation.IntentID, Payload: json.RawMessage(`{"boundary":"cancel_scope_settled"}`), CreatedAt: now, UpdatedAt: now})
	}
	return items, nil
}
func managedDispatchReceipt(item Item) bool {
	v, ok := authorityStringFields(item.Payload, "boundary", "recoveryID", "mode", "selectionHash", "terminalHash")
	if !ok || v["recoveryID"] == "" ||
		((v["boundary"] != "managed_dispatch_transferred" || v["mode"] != string(kernel.ManagedResume)) &&
			(v["boundary"] != "managed_dispatch_cancelled_before_next_effect" || v["mode"] != string(kernel.ManagedCancel))) {
		return false
	}
	for _, name := range []string{"selectionHash", "terminalHash"} {
		hash, err := hex.DecodeString(v[name])
		if err != nil || len(hash) != 32 || hex.EncodeToString(hash) != v[name] {
			return false
		}
	}
	return true
}

// IsManagedDispatchClosure reserves this namespace to CloseManagedRecovery.
func IsManagedDispatchClosure(item Item) bool {
	var v struct{ Boundary string }
	_ = json.Unmarshal(item.Payload, &v)
	return v.Boundary == "managed_dispatch_transferred" || v.Boundary == "managed_dispatch_cancelled_before_next_effect"
}

// ManagedClosureReplay compares the native projection with the original
// append-only receipts. No independent Host closure state is needed.
func ManagedClosureReplay(p kernel.ManagedClosureProof, items []Item) (bool, error) {
	t := p.Transfer()
	r, b := t.Selection, t.SafePoint.Host
	additions, err := ManagedClosureItems(p, r)
	if err != nil {
		return false, err
	}
	receipts := executionDispatchReceiptIndex(items)
	children := receipts[b.DispatchID]
	if len(children) == 0 {
		return false, nil
	}
	if len(children) != 1 || !sameItemIdentity(children[0], additions[0]) {
		return false, ErrConflict
	}
	var grant Item
	for _, item := range items {
		if item.ID == b.DispatchID {
			grant = item
		}
	}
	if _, ok := VerifiedExecutionDispatchReceipt(grant, children); !ok {
		return false, ErrConflict
	}
	if parent, ok := OriginalApprovalRequestID(grant); ok {
		var request Item
		var decisions []Item
		for _, item := range items {
			if item.ID == parent {
				request = item
			}
			if item.ParentItemID == parent {
				decisions = append(decisions, item)
			}
		}
		_, insert, err := PlanOriginalApprovalSettlement(grant, children[0], request, decisions)
		if err != nil || insert {
			return false, ErrConflict
		}
	}
	if len(additions) == 2 {
		if err := ManagedClosureAuthorization(p, items); err != nil {
			return false, err
		}
		var intent Item
		for _, item := range items {
			if item.ID == r.Cancellation.IntentID {
				intent = item
			}
		}
		done := cancellationFenceReceipts(items)[intent.ID]
		if len(done) != 1 || !sameItemIdentity(done[0], additions[1]) || !VerifiedCancellationFenceReceipt(intent, done) || !CancellationEffectsSettled(intent, items) {
			return false, ErrConflict
		}
	}
	return true, nil
}

// ManagedClosureAuthorization checks a legacy cancellation fact if one
// exists; a direct native cancellation carries its immutable user reason
// in the native commit and needs no Host cancellation fence.
func ManagedClosureAuthorization(p kernel.ManagedClosureProof, items []Item) error {
	t := p.Transfer()
	r, b := t.Selection, t.SafePoint.Host
	if !p.Matches(r) {
		return ErrConflict
	}
	if r.Mode != kernel.ManagedCancel || r.Cancellation.Direct() {
		return nil
	}
	for _, item := range items {
		if item.ID != r.Cancellation.IntentID {
			continue
		}
		if !ValidCancellationFenceIntentAdmission(item) || item.RunID != b.RunID || item.TurnID != b.TurnID || item.Seq != r.Cancellation.Seq || !bytes.Equal(item.Payload, r.Cancellation.Payload) || kernel.ManagedDigest(item.Payload) != r.Cancellation.PayloadDigest {
			return ErrConflict
		}
		if source, ok := cancellationProtocol(item); !ok || source != "native" || !CancellationEffectsSettled(item, items) {
			return ErrCancellationOutcomeUnknown
		}
		return nil
	}
	return ErrConflict
}
