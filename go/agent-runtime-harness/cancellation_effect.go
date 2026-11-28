package harness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/handoff"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

// Native cancellation already has the original Kernel control/physical-grant
// protocol. A Host callback has a DIFFERENT effect domain and needs its own
// exclusive, durable claim under the same Turn lock, before callback entry.
const CancellationEffectParentItemID = "harness.cancel.effect"
const cancellationEffectProtocol = "effects_v1"
const standardRemoteCancellationOwner = "original_remote_agent_v1"

var ErrCancellationOutcomeUnknown = fmt.Errorf("harness cancellation effect requires original-owner reconciliation: %w", ErrConflict)

// CancellationEffect is the immutable identity supplied to the ORIGINAL Host
// owner for read-only recovery. ID, not Run revision or reason, identifies the
// one external cancellation operation. TTL and Kernel terminal status cannot
// certify its outcome. Hosts must retain this identity before external dispatch.
type CancellationEffect struct {
	ID       string     `json:"id"`
	IntentID string     `json:"intentID"`
	TurnID   string     `json:"turnID"`
	RunID    string     `json:"runID"`
	OwnerID  string     `json:"ownerID"`
	Before   kernel.Run `json:"before"`
}

type CancellationEffectOutcome string

const (
	CancellationEffectUnknown  CancellationEffectOutcome = "unknown"
	CancellationEffectNoEffect CancellationEffectOutcome = "no_effect"
	CancellationEffectSettled  CancellationEffectOutcome = "settled"
)

// RuntimeCancellationReconciler is optional. Legacy Host cancellers can still
// perform their FIRST callback, but an unknown/in-flight claim is never called
// again. Recovery requires a stable original owner and this read-only port.
// The effect is also available inside the original Cancel Context below.
// NoEffect certifies this original operation CANNOT EVER dispatch, including
// through a delayed caller; absence of a remote task is insufficient. Hosts
// must fence/deduplicate dispatch by effect.ID, not just runID or request time.
type RuntimeCancellationReconciler interface {
	CancellationOwnerID() string
	ReconcileCancellation(context.Context, CancellationEffect) (kernel.Snapshot, CancellationEffectOutcome, error)
}

type cancellationElectionKey struct{}
type cancellationEffectKey struct{}
type cancellationEffectWriteKey struct{}
type cancellationEffectWrite struct{ item Item }

func withCancellationElection(ctx context.Context, intent Item) context.Context {
	if intent.ID == "" {
		return ctx
	}
	return context.WithValue(ctx, cancellationElectionKey{}, cloneItem(intent))
}

func CancellationEffectFromContext(ctx context.Context) (CancellationEffect, bool) {
	effect, ok := ctx.Value(cancellationEffectKey{}).(CancellationEffect)
	return effect, ok
}

func cancellationSource(c RuntimeCanceller) string {
	if _, ok := c.(*kernel.Runtime); ok {
		return "native"
	}
	return "host"
}

func cancellationProtocol(intent Item) (string, bool) {
	f, ok := authorityStringFields(intent.Payload, "boundary", "runID", "protocol", "canceller")
	return f["canceller"], ok && f["protocol"] == cancellationEffectProtocol && (f["canceller"] == "native" || f["canceller"] == "host")
}

func cancellationEffect(item Item) (CancellationEffect, bool) {
	var e CancellationEffect
	valid := item.ParentItemID == CancellationEffectParentItemID && item.Kind == ItemDiagnostic && item.Status == ItemStarted &&
		item.InvocationID == "" && strings.HasPrefix(item.ID, "hcanceleffect_") && decodeTypedAuthorityJSON(item.Payload, &e) && e.ID == item.ID && e.IntentID != "" &&
		e.TurnID == item.TurnID && e.RunID == item.RunID && e.OwnerID != "" && e.Before.ID == e.RunID && e.Before.Revision != 0
	return e, valid
}

func cancellationEffectReceipt(grant Item, receipts []Item) (CancellationEffectOutcome, bool) {
	_, valid := cancellationEffect(grant)
	if !valid || grant.Seq == 0 || len(receipts) != 1 {
		return "", false
	}
	r := receipts[0]
	f, ok := authorityStringFields(r.Payload, "outcome")
	o := CancellationEffectOutcome(f["outcome"])
	return o, ok && (o == CancellationEffectNoEffect || o == CancellationEffectSettled) &&
		r.ID == stableID("hcanceleffectdone", grant.ID) && r.ParentItemID == grant.ID &&
		r.Kind == ItemDiagnostic && r.Status == ItemCompleted && r.InvocationID == "" &&
		r.TurnID == grant.TurnID && r.RunID == grant.RunID && r.Seq > grant.Seq
}

// CancellationEffectsSettled is consumed by Runner recovery AND Store final
// completion CAS. Every globally indexed receipt must be included by caller.
func CancellationEffectsSettled(intent Item, items []Item) bool {
	for _, item := range items {
		if item.ParentItemID != CancellationEffectParentItemID {
			continue
		}
		e, ok := cancellationEffect(item)
		if !ok {
			return false
		}
		if e.IntentID != intent.ID {
			continue
		}
		var receipts []Item
		for _, r := range items {
			if r.ParentItemID == item.ID {
				receipts = append(receipts, r)
			}
		}
		if _, ok = cancellationEffectReceipt(item, receipts); !ok {
			return false
		}
	}
	return true
}

// CancellationEffectProjectionReady runs at the terminal Store CAS, not only
// at a prior Runner read. A late Host claim cannot race Kernel terminal
// observation into a confirmed cancellation projection. Empty runID selects
// the entire Root closure; a child CAS checks only its own effect domain.
func CancellationEffectProjectionReady(items []Item, runID string) bool {
	for _, grant := range items {
		if grant.ParentItemID != CancellationEffectParentItemID || runID != "" && grant.RunID != runID {
			continue
		}
		var receipts []Item
		for _, r := range items {
			if r.ParentItemID == grant.ID {
				receipts = append(receipts, r)
			}
		}
		if _, ok := cancellationEffectReceipt(grant, receipts); !ok {
			return false
		}
	}
	return true
}

func CancellationConfirmationAppendReady(ctx context.Context, item Item, items []Item) bool {
	intent, bound := ctx.Value(cancellationElectionKey{}).(Item)
	if !bound || item.Kind != ItemSubtask {
		return true
	}
	var task Subtask
	if json.Unmarshal(item.Payload, &task) != nil {
		return false
	}
	return task.CancelStatus != "confirmed" || CancellationEffectsSettled(intent, items)
}

// CancellationEffectAppendRelevant prevents ordinary Artifacts from squatting
// in reserved IDs, including orphan receipts before a legitimate claim exists.
func CancellationEffectAppendRelevant(item Item) bool {
	return item.ParentItemID == CancellationEffectParentItemID || strings.HasPrefix(item.ParentItemID, "hcanceleffect_") ||
		strings.HasPrefix(item.ID, "hcanceleffect_") || strings.HasPrefix(item.ID, "hcanceleffectdone_")
}

// ValidCancellationEffectAppend is checked under the original Store Turn lock.
// Private per-call capability + authoritative intent/Attempt + complete global
// receipts are all required; public AppendItem cannot manufacture claims/ACKs.
func ValidCancellationEffectAppend(ctx context.Context, value Item, items []Item, root, target Invocation) bool {
	proof, authorized := ctx.Value(cancellationEffectWriteKey{}).(cancellationEffectWrite)
	if !authorized || !sameItemIdentity(proof.item, value) {
		return false
	}
	byID := make(map[string]Item, len(items))
	for _, item := range items {
		byID[item.ID] = item
	}
	if value.ParentItemID == CancellationEffectParentItemID {
		e, valid := cancellationEffect(value)
		intent := byID[e.IntentID]
		source, protocol := cancellationProtocol(intent)
		remote := source == "native" && e.OwnerID == standardRemoteCancellationOwner &&
			e.Before.Kind == handoff.RemoteAgentRunKind && target.ID != "" && target.ParentItemID != ""
		if !valid || !protocol || (source != "host" && !remote) || intent.TurnID != value.TurnID || intent.Seq == 0 ||
			!ValidCancellationFenceIntentAdmission(intent) || !CancellationEffectsSettled(intent, items) ||
			!ValidCancellationAttemptAdmission(ctx, value, root, target) || target.ID != "" && terminalInvocationStatus(target.Status) {
			return false
		}
		var receipts []Item
		for _, item := range items {
			if item.ParentItemID == intent.ID {
				receipts = append(receipts, item)
			}
		}
		return len(receipts) == 0 // Never dispatch after completion or damaged receipt.
	}
	grant, found := byID[value.ParentItemID]
	if !found {
		return false
	}
	for _, item := range items {
		if item.ParentItemID == grant.ID {
			return false
		}
	}
	_, valid := cancellationEffectReceipt(grant, []Item{value})
	return valid
}

func (runner *Runner) appendCancellationEffect(ctx context.Context, item Item) (Item, bool, error) {
	ctx = context.WithValue(ctx, cancellationEffectWriteKey{}, cancellationEffectWrite{item: cloneItem(item)})
	return runner.store.AppendItem(ctx, item)
}

func (runner *Runner) beginHostCancellationEffect(ctx context.Context, current kernel.Snapshot) (context.Context, Item, error) {
	intent, ok := ctx.Value(cancellationElectionKey{}).(Item)
	source, protocol := cancellationProtocol(intent)
	remote := source == "native" && current.Run.Kind == handoff.RemoteAgentRunKind &&
		runner.standardRemoteCoordinator() != nil
	if !ok || !protocol || (source != "host" && !remote) {
		return ctx, Item{}, ErrCancellationOutcomeUnknown
	}
	turn, err := loadVerifiedTurnIdentity(ctx, runner.store, intent.TurnID)
	if err != nil {
		return ctx, Item{}, err
	}
	ctx, err = runner.bindCancellationAttempt(ctx, turn, current.Run.ID)
	if err != nil {
		return ctx, Item{}, err
	}
	id, err := runner.runtime.NewID("cancel_effect")
	if err != nil {
		return ctx, Item{}, err
	}
	owner := "opaque"
	if remote {
		owner = standardRemoteCancellationOwner
	} else if r, ok := runner.cancellation.(RuntimeCancellationReconciler); ok {
		owner = strings.TrimSpace(r.CancellationOwnerID())
		if owner == "" || owner == "opaque" {
			return ctx, Item{}, ErrInvalidRequest
		}
	}
	e := CancellationEffect{ID: stableID("hcanceleffect", intent.ID, current.Run.ID, id), IntentID: intent.ID,
		TurnID: intent.TurnID, RunID: current.Run.ID, OwnerID: owner, Before: current.Run}
	raw, err := json.Marshal(e)
	if err != nil {
		return ctx, Item{}, err
	}
	now := runner.clock.Now().UTC()
	grant, fresh, err := runner.appendCancellationEffect(ctx, Item{ID: e.ID, TurnID: e.TurnID, RunID: e.RunID,
		Kind: ItemDiagnostic, Status: ItemStarted, ParentItemID: CancellationEffectParentItemID, Payload: raw, CreatedAt: now, UpdatedAt: now})
	if err != nil {
		return ctx, Item{}, err
	}
	if !fresh {
		return ctx, Item{}, ErrCancellationOutcomeUnknown
	}
	// A committed-but-delayed claim ACK may arrive after explicit owner
	// reconciliation. Never enter a callback whose claim has already closed.
	facts, err := runner.loadGlobalPhysicalEvidence(ctx, intent.TurnID)
	if err != nil {
		return ctx, Item{}, err
	}
	for _, fact := range facts {
		if fact.ParentItemID == grant.ID || fact.ParentItemID == intent.ID {
			return ctx, Item{}, ErrCancellationOutcomeUnknown
		}
	}
	return context.WithValue(ctx, cancellationEffectKey{}, e), grant, nil
}

func (runner *Runner) finishHostCancellationEffect(ctx context.Context, grant Item, outcome CancellationEffectOutcome) error {
	settle, stop := cancellationSettlementContext(ctx)
	defer stop()
	raw, err := json.Marshal(struct {
		Outcome CancellationEffectOutcome `json:"outcome"`
	}{outcome})
	if err != nil {
		return err
	}
	now := runner.clock.Now().UTC()
	_, _, err = runner.appendCancellationEffect(settle, Item{ID: stableID("hcanceleffectdone", grant.ID), TurnID: grant.TurnID,
		RunID: grant.RunID, ParentItemID: grant.ID, Kind: ItemDiagnostic, Status: ItemCompleted, Payload: raw, CreatedAt: now, UpdatedAt: now})
	return errors.Join(err, settle.Err())
}

// Reconciliation never calls Cancel. Kernel status is consulted only after the
// ORIGINAL Host owner certifies this exact effect settled, not vice versa.
func (runner *Runner) reconcileCancellationEffects(ctx context.Context, intent Item, items []Item) error {
	for _, grant := range items {
		if grant.ParentItemID != CancellationEffectParentItemID {
			continue
		}
		e, valid := cancellationEffect(grant)
		if !valid {
			return ErrCancellationOutcomeUnknown
		}
		if e.IntentID != intent.ID {
			continue
		}
		var receipts []Item
		for _, r := range items {
			if r.ParentItemID == grant.ID {
				receipts = append(receipts, r)
			}
		}
		if _, settled := cancellationEffectReceipt(grant, receipts); settled {
			continue
		}
		if e.OwnerID == standardRemoteCancellationOwner && e.Before.Kind == handoff.RemoteAgentRunKind {
			// Recover ONLY the original signed protocol receipt. A2A may
			// read its Task; ACP may observe its original returned Prompt.
			// Neither route may replay the admitted Cancel callback.
			snapshot, proofErr := runner.observeStandardRemoteCancellation(ctx, e)
			if proofErr != nil || len(receipts) != 0 {
				return ErrCancellationOutcomeUnknown
			}
			if _, proofErr = runner.verifiedCustomCancellationOutcome(ctx, kernel.Snapshot{Run: e.Before}, snapshot); proofErr != nil {
				return ErrCancellationOutcomeUnknown
			}
			if proofErr = runner.finishHostCancellationEffect(ctx, grant, CancellationEffectSettled); proofErr != nil {
				return proofErr
			}
			continue
		}
		r, ok := runner.cancellation.(RuntimeCancellationReconciler)
		if !ok || e.OwnerID == "opaque" || strings.TrimSpace(r.CancellationOwnerID()) != e.OwnerID || len(receipts) != 0 {
			return ErrCancellationOutcomeUnknown
		}
		snapshot, outcome, err := r.ReconcileCancellation(ctx, e)
		if err != nil || (outcome != CancellationEffectNoEffect && outcome != CancellationEffectSettled) {
			return errors.Join(ErrCancellationOutcomeUnknown, err)
		}
		if outcome == CancellationEffectSettled {
			if _, err = runner.verifiedCustomCancellationOutcome(ctx, kernel.Snapshot{Run: e.Before}, snapshot); err != nil {
				return errors.Join(ErrCancellationOutcomeUnknown, err)
			}
		}
		if err = runner.finishHostCancellationEffect(ctx, grant, outcome); err != nil {
			return err
		}
	}
	return nil
}
