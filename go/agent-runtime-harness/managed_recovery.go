package harness

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"

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

// nativeHostSource is the one immutable command projection. The
// transaction still validates all original rows; it grants no callback.
func nativeHostSource(session Session, turn Turn, invocation Invocation, config ConfigSnapshot, b kernel.ManagedHostBinding) (json.RawMessage, error) {
	if _, err := canonicalSealedConfig(config); err != nil {
		return nil, err
	}
	type command struct {
		Correlation, Phase string
		Approval           *kernel.ManagedApprovalDecision
	}
	return json.Marshal(struct {
		SessionID                  string
		HostThread                 HostRef
		Actor                      kernel.ActorRef
		TurnID                     string
		HostTurn                   HostRef
		Config                     ConfigSnapshot
		InvocationID, ParentItemID string
		Input                      json.RawMessage
		InputHash, ExecutionRefID  string
		Attempt                    int
		Dispatch                   command
	}{session.ID, session.HostThread, session.Actor, turn.ID, turn.HostTurn, config,
		invocation.ID, invocation.ParentItemID,
		invocation.Input, invocation.InputHash, invocation.ExecutionRefID, invocation.Attempt,
		command{b.DispatchID, b.Phase, b.Approval}})
}

// ValidateManagedOriginalRows is shared by concrete storage transactions. It
// admits the exact original native Root dispatch, including a frozen Retry.
func ValidateManagedOriginalRows(authority kernel.ManagedAuthority, b kernel.ManagedHostBinding, session Session, turn Turn, invocation Invocation, config ConfigSnapshot, dispatch Item, items []Item) error {
	if !b.Valid() || b.HostAuthority != authority || session.ID == "" || turn.SessionID != session.ID ||
		turn.ID != b.TurnID || config.ID != turn.ConfigSnapshotID || config.TurnID != turn.ID ||
		invocation.ID != b.InvocationID || invocation.TurnID != turn.ID || invocation.ParentItemID != "" ||
		invocation.Attempt <= 0 || uint64(invocation.Attempt) != uint64(b.Attempt) ||
		invocation.ExecutionRefID != b.RunID {
		return ErrConflict
	}
	if dispatch.ID != "" || terminalTurnStatus(turn.Status) ||
		terminalInvocationStatus(invocation.Status) || ActiveCancellationFence(items) || PendingExecutionDispatch(items, invocation.ID) {
		return ErrConflict
	}
	if invocation.Attempt > 1 {
		if _, err := retrySelectionFromItems(invocation, items); err != nil {
			return ErrConflict
		}
	}
	source, err := nativeHostSource(session, turn, invocation, config, b)
	if err != nil || !bytes.Equal(source, b.Source) {
		return ErrConflict
	}
	if b.Approval != nil {
		return verifyNativeApprovalProjection(turn.ID, invocation.ID, b.RunID, *b.Approval, items)
	}
	return nil
}

// Retired Managed Host receipts are not new admissible product facts.
func IsManagedDispatchClosure(item Item) bool {
	var v struct{ Boundary string }
	_ = json.Unmarshal(item.Payload, &v)
	return v.Boundary == "managed_dispatch_transferred" || v.Boundary == "managed_dispatch_cancelled_before_next_effect"
}
