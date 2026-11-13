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
func ManagedHostSource(session Session, turn Turn, invocation Invocation, config ConfigSnapshot, dispatch Item) (json.RawMessage, error) {
	return json.Marshal(struct {
		SessionID                                                    string
		HostThread                                                   HostRef
		Actor                                                        kernel.ActorRef
		TurnID                                                       string
		HostTurn                                                     HostRef
		Config                                                       ConfigSnapshot
		InvocationID, ParentItemID, CapabilityKey, DefinitionVersion string
		ExecutionClass                                               ExecutionClass
		Input                                                        json.RawMessage
		InputHash, ExecutionRefID                                    string
		Attempt                                                      int
		Dispatch                                                     Item
	}{session.ID, session.HostThread, session.Actor, turn.ID, turn.HostTurn, config,
		invocation.ID, invocation.ParentItemID, invocation.CapabilityKey, invocation.DefinitionVersion,
		invocation.ExecutionClass, invocation.Input, invocation.InputHash, invocation.ExecutionRefID, invocation.Attempt, dispatch})
}

// ValidateManagedOriginalRows is shared by concrete storage transactions. It
// admits only the original Root start, never an arbitrary child or resume.
func ValidateManagedOriginalRows(authority kernel.ManagedAuthority, b kernel.ManagedHostBinding, session Session, turn Turn, invocation Invocation, config ConfigSnapshot, dispatch Item, items []Item) error {
	if !b.Valid() || b.HostAuthority != authority || session.ID == "" || turn.SessionID != session.ID ||
		turn.ID != b.TurnID || config.ID != turn.ConfigSnapshotID || config.TurnID != turn.ID ||
		invocation.ID != b.InvocationID || invocation.TurnID != turn.ID || invocation.ParentItemID != "" ||
		invocation.ExecutionClass != ExecutionAgent || invocation.Attempt <= 0 || uint64(invocation.Attempt) != uint64(b.Attempt) ||
		invocation.ExecutionRefID != b.RunID || dispatch.ID != b.DispatchID || dispatch.InvocationID != invocation.ID ||
		dispatch.TurnID != turn.ID || dispatch.RunID != b.RunID {
		return ErrConflict
	}
	phase, ok := VerifiedExecutionDispatchGrant(dispatch)
	if !ok || phase != b.Phase || phase != "start" {
		return ErrConflict
	}
	source, err := ManagedHostSource(session, turn, invocation, config, dispatch)
	if err != nil || !bytes.Equal(source, b.Source) {
		return ErrConflict
	}
	// No ordinary completion, including a malformed sibling, is compatible
	// with an unresolved original dispatch.
	for _, item := range items {
		if item.ParentItemID == dispatch.ID {
			return ErrConflict
		}
	}
	return nil
}

// SelectManagedReservation runs under the original Turn lock. Mode and full
// cancellation payload are chosen only from real immutable Host records.
func SelectManagedReservation(p kernel.ManagedParkedProof, b kernel.ManagedHostBinding, turn Turn, invocation Invocation, items []Item) (kernel.ManagedHostReservation, error) {
	if !p.Matches(b) || terminalTurnStatus(turn.Status) || terminalInvocationStatus(invocation.Status) {
		return kernel.ManagedHostReservation{}, ErrConflict
	}
	transfer := p.Transfer()
	out := kernel.ManagedHostReservation{ID: "managed-host-" + kernel.ManagedDigest(transfer.SafePoint.RecoveryID), RecoveryID: transfer.SafePoint.RecoveryID, SuspensionID: transfer.SafePoint.ID, Binding: b, Mode: kernel.ManagedResume, Revision: 1}
	receipts := cancellationFenceReceipts(items)
	for _, item := range items {
		if item.ParentItemID != CancellationFenceParentItemID {
			continue
		}
		if VerifiedCancellationFenceReceipt(item, receipts[item.ID]) {
			continue
		}
		if !ValidCancellationFenceIntentAdmission(item) || item.Seq == 0 || item.RunID != b.RunID || item.TurnID != b.TurnID ||
			len(receipts[item.ID]) != 0 || out.Mode == kernel.ManagedCancel {
			return kernel.ManagedHostReservation{}, ErrConflict
		}
		out.Mode = kernel.ManagedCancel
		out.Cancellation = kernel.ManagedCancellation{IntentID: item.ID, Seq: item.Seq, Payload: append(json.RawMessage(nil), item.Payload...), PayloadDigest: kernel.ManagedDigest(item.Payload), Reason: ""}
	}
	if !out.Valid() {
		return kernel.ManagedHostReservation{}, ErrConflict
	}
	return out, nil
}

func cloneManagedReservation(r kernel.ManagedHostReservation) kernel.ManagedHostReservation {
	r.Binding.Source = append(json.RawMessage(nil), r.Binding.Source...)
	r.Cancellation.Payload = append(json.RawMessage(nil), r.Cancellation.Payload...)
	return r
}

// SameManagedReservation includes mode, payload, source, namespaces and revision.
func SameManagedReservation(a, b kernel.ManagedHostReservation) bool { return reflect.DeepEqual(a, b) }

// ManagedClosureItems derives the fixed append-only projection from an opaque
// Kernel read proof. No returned-callback fact is attributed to the old owner.
func ManagedClosureItems(p kernel.ManagedClosureProof, r kernel.ManagedHostReservation) ([]Item, error) {
	if !p.Matches(r) {
		return nil, ErrConflict
	}
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
	}{boundary, r.RecoveryID, r.Mode, kernel.ManagedDigest(r), kernel.ManagedDigest(snap)})
	if err != nil {
		return nil, err
	}
	now := snap.Run.UpdatedAt
	items := []Item{{ID: stableID("hdispatchdone", r.Binding.DispatchID), TurnID: r.Binding.TurnID, InvocationID: r.Binding.InvocationID, RunID: r.Binding.RunID, Kind: ItemDiagnostic, Status: ItemCompleted, ParentItemID: r.Binding.DispatchID, Payload: payload, CreatedAt: now, UpdatedAt: now}}
	if r.Mode == kernel.ManagedCancel {
		items = append(items, Item{ID: stableID("hcancelclosed", r.Cancellation.IntentID), TurnID: r.Binding.TurnID, RunID: r.Binding.RunID, Kind: ItemDiagnostic, Status: ItemCompleted, ParentItemID: r.Cancellation.IntentID, Payload: json.RawMessage(`{"boundary":"cancel_scope_settled"}`), CreatedAt: now, UpdatedAt: now})
	}
	return items, nil
}
func managedDispatchReceipt(item Item) bool {
	v, ok := authorityStringFields(item.Payload, "boundary", "recoveryID", "mode", "selectionHash", "terminalHash")
	if !ok || v["recoveryID"] == "" ||
		!((v["boundary"] == "managed_dispatch_transferred" && v["mode"] == string(kernel.ManagedResume)) ||
			(v["boundary"] == "managed_dispatch_cancelled_before_next_effect" && v["mode"] == string(kernel.ManagedCancel))) {
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

// ValidateManagedReservationRows rechecks the immutable selected cancellation,
// not merely a RESERVED row's existence. Closed RESUME history is unaffected
// by a later independent cancellation of the same Turn.
func ValidateManagedReservationRows(r kernel.ManagedHostReservation, items []Item, closed bool) error {
	if !r.Valid() {
		return ErrConflict
	}
	if r.Mode == kernel.ManagedResume {
		if !closed && ActiveCancellationFence(items) {
			return ErrConflict
		}
		return nil
	}
	found := false
	for _, item := range items {
		if item.ID != r.Cancellation.IntentID {
			continue
		}
		if found || !ValidCancellationFenceIntentAdmission(item) || item.RunID != r.Binding.RunID || item.TurnID != r.Binding.TurnID ||
			item.Seq != r.Cancellation.Seq || !bytes.Equal(item.Payload, r.Cancellation.Payload) ||
			kernel.ManagedDigest(item.Payload) != r.Cancellation.PayloadDigest || r.Cancellation.Reason != "" {
			return ErrConflict
		}
		found = true
	}
	if !found {
		return ErrConflict
	}
	return nil
}
