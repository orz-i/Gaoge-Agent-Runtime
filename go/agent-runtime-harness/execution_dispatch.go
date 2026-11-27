package harness

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync/atomic"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
)

// ErrExecutionOutcomeUnknown means a Feature Start returned no authoritative
// Kernel Run snapshot without a proven no-effect outcome. The original Run ID
// and grant must be retained for reconciliation; Retry cannot rotate them.
var ErrExecutionOutcomeUnknown = errors.New("harness physical feature start outcome is unknown")

type dispatchProvenNoEffect struct{ cause error }

func (e dispatchProvenNoEffect) Error() string { return e.cause.Error() }
func (e dispatchProvenNoEffect) Unwrap() error { return e.cause }

func isDispatchProvenNoEffect(err error) bool {
	_, ok := err.(dispatchProvenNoEffect) //nolint:errorlint // Direct Harness-minted proof, not a wrapped error.
	return ok
}

// ExecutionDispatchParentItemID marks a physical Feature Start/Resume grant.
// New grants and cancellation intents serialize at the same existing Turn
// Store boundary. An unfinished grant prevents a competing cancellation from
// claiming it stopped the Feature; a cancelled Turn cannot admit a late grant.
const ExecutionDispatchParentItemID = "harness.execution.dispatch"

// executionDispatchReceiptIndex preserves every claimed settlement, including
// malformed and conflicting ones. Dropping a second receipt or an incorrect
// owner would turn inconsistent physical execution evidence into authority.
func executionDispatchReceiptIndex(items []Item) map[string][]Item {
	// Index only actual physical grant identities. Every child of one
	// grant counts, not just Diagnostic children: filtering out an
	// unexpected ItemKind would let one valid receipt hide a second
	// contradictory persisted fact.
	grants := make(map[string]struct{})
	for _, item := range items {
		if item.ParentItemID == ExecutionDispatchParentItemID {
			// A corrupted historical grant is still an original claim,
			// never proof that a physical operation did not occur.
			grants[item.ID] = struct{}{}
		}
	}
	byGrant := make(map[string][]Item)
	for _, item := range items {
		if _, owned := grants[item.ParentItemID]; owned {
			byGrant[item.ParentItemID] = append(byGrant[item.ParentItemID], item)
		}
	}
	return byGrant
}

type originalApprovalDispatchKey struct{}

// The existing immutable physical grant carries the exact approval request.
// Legacy two-field grants remain readable for ordinary return retirement.
func executionDispatchFields(raw json.RawMessage) (map[string]string, bool) {
	if fields, ok := authorityStringFields(raw, "phase", "physicalRunID"); ok {
		return fields, true
	}
	fields, ok := authorityStringFields(raw, "phase", "physicalRunID", "checkpointID", "decision", "comment")
	if !ok || (fields["phase"] != "resolve-approval" && fields["phase"] != "resolve-subtask-approval") {
		return nil, false
	}
	a := kernel.ManagedApprovalDecision{CheckpointID: fields["checkpointID"], Decision: fields["decision"], Comment: fields["comment"]}
	return fields, a.Valid()
}
func originalDispatchApproval(grant Item) (*kernel.ManagedApprovalDecision, bool) {
	fields, ok := executionDispatchFields(grant.Payload)
	if !ok || (fields["phase"] != "resolve-approval" && fields["phase"] != "resolve-subtask-approval") || fields["checkpointID"] == "" {
		return nil, false
	}
	return &kernel.ManagedApprovalDecision{CheckpointID: fields["checkpointID"], Decision: fields["decision"], Comment: fields["comment"]}, true
}

// ValidExecutionDispatchGrantAdmission checks the exact original physical
// phase before a Store has assigned its per-Turn Seq. Memory and SQL use this
// same proof before committing a grant. A fabricated phase is not authority.
func ValidExecutionDispatchGrantAdmission(grant Item) (string, bool) {
	if grant.ID == "" || grant.Kind != ItemDiagnostic ||
		grant.Status != ItemStarted || grant.ParentItemID != ExecutionDispatchParentItemID ||
		grant.TurnID == "" || grant.InvocationID == "" || grant.RunID == "" {
		return "", false
	}
	original, ok := executionDispatchFields(grant.Payload)
	if !ok || original["physicalRunID"] == "" {
		return "", false
	}
	phase := original["phase"]
	switch phase {
	case "start", "start-retry", "resume", "resolve-approval",
		"grant-call-allowance", "handoff-start":
		if original["physicalRunID"] == grant.RunID {
			return phase, true
		}
	case "resolve-subtask-approval", "resolve-wait":
		if original["checkpointID"] != "" && original["physicalRunID"] != grant.RunID {
			return "", false
		}
		// These explicitly target a proven descendant Kernel Run, not
		// necessarily the owning Harness Invocation's root Run. The
		// final physical call separately checks the child association.
		return phase, true
	}
	return "", false
}

// HostExecutionDispatchOwnerAllowed is evaluated under the original Turn
// lock. Native Named Agent commands cannot elect Host execution authority.
func HostExecutionDispatchOwnerAllowed(owner Invocation) bool {
	if !validInvocationStatus(owner.Status) || terminalInvocationStatus(owner.Status) {
		return false
	}
	if owner.ParentItemID != "" && (isHandoffDelegationInvocation(owner) || strings.HasPrefix(owner.ParentItemID, "hid_")) {
		d, err := decodeFrozenHandoffDelegation(owner.Input)
		return err == nil && !d.NativeLocal
	}
	return true
}

// ValidateHostExecutionDispatchOwner checks both the elected owner and any
// separately targeted subtask. The original Turn lock covers both reads.
func ValidateHostExecutionDispatchOwner(grant Item, owner Invocation, status TurnStatus, lookup func(string) (Invocation, error)) error {
	if !validTurnStatus(status) || terminalTurnStatus(status) || owner.ID != grant.InvocationID || owner.TurnID != grant.TurnID || owner.ExecutionRefID != grant.RunID || !HostExecutionDispatchOwnerAllowed(owner) {
		return ErrConflict
	}
	fields, valid := executionDispatchFields(grant.Payload)
	if !valid {
		return ErrConflict
	}
	if fields["phase"] == "resolve-subtask-approval" && fields["physicalRunID"] != grant.RunID {
		target, err := lookup(fields["physicalRunID"])
		if err != nil && !errors.Is(err, ErrNotFound) {
			return err
		}
		if err == nil && !HostExecutionDispatchOwnerAllowed(target) {
			return ErrConflict
		}
	}
	return nil
}

// VerifiedExecutionDispatchGrant also requires the original Store-allocated
// Item sequence. An uncommitted request cannot be read as a settled grant.
func VerifiedExecutionDispatchGrant(grant Item) (string, bool) {
	if grant.Seq == 0 {
		return "", false
	}
	return ValidExecutionDispatchGrantAdmission(grant)
}

// VerifiedExecutionDispatchReceipt is the single durable grant-settlement
// contract used by the Memory and SQL Stores, retry, cancellation and native
// recovery. A physical grant has exactly ONE recognized completion fact,
// committed later in the same Turn with the exact Invocation and Run owner.
// A foreign/unknown/contradictory completion is NOT a settlement.
func VerifiedExecutionDispatchReceipt(grant Item, receipts []Item) (string, bool) {
	phase, validGrant := VerifiedExecutionDispatchGrant(grant)
	if !validGrant {
		return "", false
	}
	var receipt Item
	count := 0
	for _, item := range receipts {
		if item.ParentItemID != grant.ID {
			continue
		}
		count++
		receipt = item
	}
	if count != 1 || receipt.ID != stableID("hdispatchdone", grant.ID) ||
		receipt.Kind != ItemDiagnostic ||
		receipt.Status != ItemCompleted ||
		receipt.TurnID != grant.TurnID || receipt.InvocationID != grant.InvocationID ||
		receipt.RunID != grant.RunID || receipt.Seq <= grant.Seq {
		return "", false
	}
	payload, ok := authorityStringFields(receipt.Payload, "boundary")
	if !ok {
		return "", false
	}
	switch payload["boundary"] {
	case "physical_dispatch_rejected":
		return payload["boundary"], true
	case "physical_dispatch_returned":
		// Handoff may dispatch to a separate child Runtime. Only the
		// Handoff-specific observed return can settle that original grant;
		// a local Kernel snapshot is not remote-execution evidence.
		if phase != "handoff-start" {
			return payload["boundary"], true
		}
	case "native_agent_start_no_effect":
		if phase == "start" || phase == "start-retry" || phase == "handoff-start" {
			return payload["boundary"], true
		}
	case "native_kernel_epoch_recovered":
		if phase == "start" || phase == "start-retry" {
			return payload["boundary"], true
		}
	case "native_agent_callback_not_entered":
		// Unlike pre-Create rejection, the native Run exists. Only an
		// original Agent call with acknowledged no-entry may close this.
		if phase == "start" || phase == "start-retry" || phase == "resume" ||
			phase == "resolve-approval" || phase == "resolve-subtask-approval" {
			return payload["boundary"], true
		}
	case approvalNotAppliedBoundary:
		if _, ok := originalDispatchApproval(grant); ok {
			return payload["boundary"], true
		}
	case "native_agent_control_no_effect":
		if phase == "resolve-approval" || phase == "resolve-subtask-approval" ||
			phase == "grant-call-allowance" {
			return payload["boundary"], true
		}
	case "native_allowance_committed":
		if phase == "grant-call-allowance" {
			return payload["boundary"], true
		}
	case "handoff_start_returned":
		if phase == "handoff-start" {
			return payload["boundary"], true
		}
	}
	return "", false
}

// ExecutionDispatchBlocksAdmission shares the exact receipt predicate
// between Store admission and the no-new-effect election. A Handoff Start
// may retry a definitive rejection, never an actually returned Start.
func ExecutionDispatchBlocksAdmission(grant Item, receipts []Item, requestedPhase string) bool {
	boundary, settled := VerifiedExecutionDispatchReceipt(grant, receipts)
	phase, _ := VerifiedExecutionDispatchGrant(grant)
	return !settled || requestedPhase == "handoff-start" && (phase != requestedPhase || boundary != "physical_dispatch_rejected" && boundary != "native_agent_start_no_effect")
}
func PendingExecutionDispatch(items []Item, invocationID string) bool {
	return executionDispatchAdmissionBlocked(items, invocationID, "")
}
func executionDispatchAdmissionBlocked(items []Item, invocationID, phase string) bool {
	receipts := executionDispatchReceiptIndex(items)
	for _, item := range items {
		if item.ParentItemID != ExecutionDispatchParentItemID || (invocationID != "" && item.InvocationID != invocationID) {
			continue
		}
		if ExecutionDispatchBlocksAdmission(item, receipts[item.ID], phase) {
			return true
		}
	}
	return false
}

func (runner *Runner) pendingExecutionDispatch(
	ctx context.Context, turnID string, targets map[string]struct{}, targetRuns map[string]struct{},
) (bool, error) {
	items, err := runner.loadGlobalPhysicalEvidence(ctx, turnID)
	if err != nil {
		return false, err
	}
	receipts := executionDispatchReceiptIndex(items)
	for _, item := range items {
		if item.ParentItemID != ExecutionDispatchParentItemID {
			continue
		}
		if _, done := VerifiedExecutionDispatchReceipt(item, receipts[item.ID]); done {
			continue
		}
		var phase struct {
			Phase         string `json:"phase"`
			PhysicalRunID string `json:"physicalRunID"`
		}
		if json.Unmarshal(item.Payload, &phase) != nil {
			return false, ErrConflict
		}
		if targets != nil {
			_, matchedOwner := targets[item.InvocationID]
			_, matchedRun := targetRuns[phase.PhysicalRunID]
			if !matchedOwner && (phase.PhysicalRunID == "" || !matchedRun) {
				continue
			}
		}
		// A foreign Invocation's malformed receipt must not veto a
		// disjoint targeted cancellation. After the original scope is
		// established, a present-but-contradictory receipt disables the
		// otherwise valid native Kernel Start cancellation exception.
		if len(receipts[item.ID]) != 0 {
			return true, nil
		}
		// The same exact original-dispatch repair used by Resume is also
		// available to protective control. Never require a new physical
		// call to close an already acknowledged native retirement.
		if native, ok := runner.agent.(*agent.Runner); ok && native.ObservesKernelRuntime(runner.runtime) {
			owner, ownerErr := runner.store.GetInvocation(ctx, item.InvocationID)
			if ownerErr != nil {
				return false, ownerErr
			}
			if owner.ID != item.InvocationID || owner.TurnID != turnID || owner.ExecutionRefID != item.RunID {
				return false, ErrConflict
			}
			repaired, repairErr := runner.recoverOriginalPhysicalDispatch(ctx, owner)
			if repairErr != nil {
				return false, repairErr
			}
			if repaired {
				continue
			}
		}
		if (phase.Phase == "start" || phase.Phase == "start-retry") &&
			runner.nativeAgentStartOwnedByKernel(ctx, item, phase.PhysicalRunID) {
			// Only the exact first-party Agent with an authoritative
			// Kernel Run can be cancelled before its Start receipt. Native
			// Agent execution checks Kernel CAS (and Epoch when configured)
			// before continuing; composite Features can still create children
			// after their parent Kernel Run has been created.
			continue
		}
		return true, nil
	}
	return false, nil
}

// nativeAgentStartOwnedByKernel is the sole exception to an outstanding
// physical Start grant blocking cancellation. Run existence is not sufficient:
// only a concrete first-party Agent, exact Root Invocation ownership and
// native Agent Run can use Kernel cancellation CAS (and an acquired native
// execution Epoch where present). A custom Agent, composite Feature or
// delegated child never inherits this exception.
func (runner *Runner) nativeAgentStartOwnedByKernel(ctx context.Context, grant Item, physicalRunID string) bool {
	if runner == nil || runner.runtime == nil || runner.store == nil || physicalRunID == "" ||
		physicalRunID != grant.RunID || grant.InvocationID == "" {
		return false
	}
	native, ok := runner.agent.(*agent.Runner)
	if !ok || !native.NativeStartCancellationFencedByKernel(runner.runtime) {
		return false
	}
	// A custom or separately bound RuntimeCanceller cannot claim that
	// cancelling this Kernel Run fences the native Agent's execution.
	canceller, nativeCancel := runner.cancellation.(*kernel.Runtime)
	if !nativeCancel || canceller != runner.runtime {
		return false
	}
	owner, err := runner.store.GetInvocation(ctx, grant.InvocationID)
	if err != nil || owner.ID != grant.InvocationID || owner.TurnID != grant.TurnID ||
		owner.ParentItemID != "" || owner.ExecutionRefID != grant.RunID {
		return false
	}
	started, err := runner.runtime.Load(ctx, grant.RunID)
	if err != nil || started.Run.ID != grant.RunID ||
		started.Run.Kind != agent.RunKind || started.Run.Status != kernel.RunStatusRunning {
		return false
	}
	// Native Agent Runs can be unleased (Kernel CAS) or require a bound
	// Epoch (Kernel Store fencing). Either case has a first-party native
	// continuation guard. Any other Store error gives no cancel authority.
	proof := runner.runtime.RequireExecution(ctx, grant.RunID)
	return proof == nil || errors.Is(proof, kernel.ErrExecutionRequired)
}

// withPhysicalInvocationDispatch is the single Harness physical Start/Resume
// boundary. The grant is durable BEFORE any Feature call and competes with
// cancellation-intent admission in the same Store transaction. It is closed
// only when a returned Kernel snapshot proves which Run was reached. An
// ambiguous no-snapshot outcome stays fenced for explicit reconciliation.
func (runner *Runner) withPhysicalInvocationDispatch(
	ctx context.Context, invocation Invocation, phase string,
	physical func(context.Context) (kernel.Snapshot, error),
) (kernel.Snapshot, error) {
	return runner.withPhysicalInvocationDispatchFor(ctx, invocation, phase, invocation.ExecutionRefID, physical)
}

// SubAgent control may mutate a proven descendant Run rather
// than the owning Harness Invocation's root Run. The durable grant still
// belongs to the owning Invocation for cancellation arbitration.
func (runner *Runner) withPhysicalInvocationDispatchFor(
	ctx context.Context, invocation Invocation, phase, resultRunID string,
	physical func(context.Context) (kernel.Snapshot, error),
) (kernel.Snapshot, error) {
	if physical == nil {
		return kernel.Snapshot{}, ErrInvalidRequest
	}
	return runner.withPhysicalInvocationDispatchForContext(ctx, invocation, phase, resultRunID,
		nil, physical)
}

// An AgentTask approval carries a private per-call proof only AFTER the
// Store has admitted its original-Invocation Grant. The checkpoint identity
// is scoped to the exact revision; a process-local Context cannot substitute
// for a new physical dispatch after a later approval checkpoint is created.
func (runner *Runner) withPhysicalSubtaskApprovalDispatch(
	ctx context.Context, invocation Invocation, childRunID string,
	childRevision uint64, checkpointID string,
	physical func(context.Context) (kernel.Snapshot, error),
) (kernel.Snapshot, error) {
	if childRevision == 0 || strings.TrimSpace(checkpointID) == "" {
		return kernel.Snapshot{}, ErrInvalidRequest
	}
	return runner.withPhysicalInvocationDispatchForContext(
		ctx, invocation, "resolve-subtask-approval", childRunID,
		&subtaskApprovalDispatchScope{
			ChildRunID: childRunID, CheckpointID: checkpointID, Revision: childRevision,
		}, physical)
}

// Native callback names are shared by initial dispatch and original receipt recovery.
func nativeDispatchOperation(phase string) plugin.RunOperation {
	switch phase {
	case "start", "start-retry", "handoff-start":
		return plugin.RunStart
	case "resume":
		return plugin.RunResume
	case "resolve-approval", "resolve-subtask-approval":
		return plugin.RunResolveApproval
	default:
		return ""
	}
}

func (runner *Runner) withPhysicalInvocationDispatchForContext(
	ctx context.Context, invocation Invocation, phase, resultRunID string,
	scope *subtaskApprovalDispatchScope,
	physical func(context.Context) (kernel.Snapshot, error),
) (kernel.Snapshot, error) {
	if physical == nil || (scope != nil && phase != "resolve-subtask-approval") {
		return kernel.Snapshot{}, ErrInvalidRequest
	}
	grant, err := runner.beginPhysicalInvocationDispatch(ctx, invocation, phase, resultRunID)
	if err != nil {
		return kernel.Snapshot{}, err
	}
	if scope != nil {
		// The first ownership read happened before a potentially stale Turn
		// projection. Recheck AFTER durable grant admission and BEFORE any
		// Agent middleware or external effect, so a parent already terminal
		// cannot use an otherwise valid child checkpoint.
		child, loadErr := runner.runtime.Load(ctx, scope.ChildRunID)
		if loadErr == nil && (child.Run.ID != scope.ChildRunID ||
			child.Run.Revision != scope.Revision ||
			child.Checkpoint == nil || child.Checkpoint.ID != scope.CheckpointID) {
			loadErr = ErrConflict
		}
		if loadErr == nil {
			loadErr = runner.verifyLocalApprovalRun(ctx, child)
		}
		if loadErr != nil {
			// No physical callback has started in this grant, so this
			// rejection is definitive no-effect. The receipt must be sealed,
			// not kept as an unknown external operation on a stale parent.
			settled := runner.finishPhysicalDispatchGrant(ctx, grant, "physical_dispatch_rejected")
			if settled != nil {
				return kernel.Snapshot{}, errors.Join(ErrExecutionOutcomeUnknown, loadErr, settled)
			}
			return kernel.Snapshot{}, errors.Join(ErrConflict, loadErr)
		}
	}
	physicalCtx := ctx
	if scope != nil {
		bound := *scope
		bound.GrantID = grant.ID
		bound.active = new(atomic.Bool)
		bound.active.Store(true)
		// Middleware may retain Context for later asynchronous work. Expire
		// this grant capability when the exact physical callback returns or
		// panics; a retained Context cannot authorize a later approval without
		// a fresh durable Harness admission.
		defer bound.active.Store(false)
		physicalCtx = context.WithValue(physicalCtx, subtaskApprovalDispatchContextKey{}, bound)
	}
	native, nativeAgent := runner.agent.(*agent.Runner)
	operation := nativeDispatchOperation(phase)
	observe := nativeAgent && operation != "" && phase != "handoff-start"
	var snapshot kernel.Snapshot
	var physicalErr error
	var outcome agent.PhysicalCallOutcome
	if observe {
		snapshot, outcome, physicalErr = native.ObservePhysicalDispatch(physicalCtx, resultRunID, operation, grant.ID, physical)
	} else {
		snapshot, physicalErr = physical(physicalCtx)
	}
	if snapshot.Run.ID == "" {
		if observe && phase == "resume" && outcome.RejectedStaleResume(resultRunID) {
			receiptErr := runner.finishPhysicalDispatchGrant(ctx, grant, "physical_dispatch_rejected")
			if receiptErr != nil {
				return snapshot, errors.Join(ErrExecutionOutcomeUnknown, physicalErr, receiptErr)
			}
			return snapshot, physicalErr
		}
		// An arbitrary Feature may have dispatched remote side effects
		// before returning a generic error, including a Kernel CAS conflict.
		// Only the concrete first-party Agent control can certify that its
		// own preflight failed BEFORE invoking middleware or ApplyControl.
		// Never accept that marker from a custom wrapper or from a general
		// physical Start/Resume path.
		_, nativeAgent := runner.agent.(*agent.Runner)
		control := phase == "resolve-approval" || phase == "resolve-subtask-approval" ||
			phase == "grant-call-allowance"
		if nativeAgent && control && agent.IsNativeControlNoEffect(physicalErr) {
			return snapshot, errors.Join(physicalErr,
				runner.finishPhysicalDispatchGrant(ctx, grant, "native_agent_control_no_effect"))
		}
		// A direct first-party Agent pre-middleware/pre-Create failure is
		// also definitive. Its private result cannot be minted by a
		// custom Feature or returned from an effectful Run middleware.
		// Every other no-snapshot Agent error remains outcome-unknown.
		if nativeAgent && (phase == "start" || phase == "start-retry") && agent.IsNativeStartNoEffect(physicalErr) {
			if receiptErr := runner.finishPhysicalDispatchGrant(ctx, grant, "native_agent_start_no_effect"); receiptErr == nil {
				return snapshot, dispatchProvenNoEffect{cause: physicalErr}
			} else {
				return snapshot, errors.Join(ErrExecutionOutcomeUnknown, physicalErr, receiptErr)
			}
		}
		// Unknown effects retain the grant and its exact execution identity
		// for explicit reconciliation, including errors without a snapshot.
		return snapshot, errors.Join(ErrExecutionOutcomeUnknown, physicalErr)
	}
	if snapshot.Run.ID != resultRunID {
		return snapshot, errors.Join(physicalErr, ErrConflict)
	}
	if observe && !outcome.Matches(resultRunID, operation) {
		// A nonempty Run is recoverable identity, not this callback's receipt.
		// Only the current first-party invocation can certify settled or a
		// durable known yield still protected by its original Kernel scope.
		return snapshot, errors.Join(ErrExecutionOutcomeUnknown, physicalErr)
	}
	boundary := "physical_dispatch_returned"
	if observe && outcome.Kind() == agent.PhysicalCallNotEntered {
		boundary = "native_agent_callback_not_entered"
	}
	receiptErr := runner.finishPhysicalDispatchGrant(ctx, grant, boundary)
	if boundary == "native_agent_callback_not_entered" {
		if receiptErr != nil {
			return snapshot, errors.Join(ErrExecutionOutcomeUnknown, physicalErr, receiptErr)
		}
		// Reuse the existing private no-effect result. Unlike pre-Create
		// rejection this has a real snapshot and a distinct durable boundary.
		if physicalErr != nil {
			return snapshot, dispatchProvenNoEffect{cause: physicalErr}
		}
	}
	return snapshot, errors.Join(physicalErr, receiptErr)
}

// beginPhysicalInvocationDispatch is shared by Kernel Feature operations and
// Role Handoff starts. Admission is serialized with cancellation and retry by
// the Store; the frozen root/ancestor Attempt check happens after the grant,
// before calling any physical executor. A failed pre-effect check settles the
// grant as definitive no-effect, without ever invoking the executor.
func (runner *Runner) beginPhysicalInvocationDispatch(
	ctx context.Context, invocation Invocation, phase, physicalRunID string,
) (Item, error) {
	if runner == nil || runner.store == nil || runner.runtime == nil || runner.clock == nil ||
		strings.TrimSpace(invocation.ID) == "" || invocation.TurnID == "" ||
		invocation.ExecutionRefID == "" || strings.TrimSpace(physicalRunID) == "" {
		return Item{}, ErrInvalidRequest
	}
	switch phase {
	case "resume", "resolve-approval", "resolve-subtask-approval", "grant-call-allowance", "handoff-start":
		if err := runner.requireInvocationRootModelOutputCeiling(ctx, invocation); err != nil {
			return Item{}, err
		}
	}
	identifier, err := runner.runtime.NewID("dispatch")
	if err != nil {
		return Item{}, err
	}
	payload, err := json.Marshal(struct {
		Phase         string `json:"phase"`
		PhysicalRunID string `json:"physicalRunID,omitempty"`
	}{Phase: phase, PhysicalRunID: physicalRunID})
	if approval, ok := ctx.Value(originalApprovalDispatchKey{}).(kernel.ManagedApprovalDecision); ok && (phase == "resolve-approval" || phase == "resolve-subtask-approval") {
		if !approval.Valid() {
			return Item{}, ErrInvalidRequest
		}
		payload, err = json.Marshal(struct {
			Phase         string `json:"phase"`
			PhysicalRunID string `json:"physicalRunID"`
			CheckpointID  string `json:"checkpointID"`
			Decision      string `json:"decision"`
			Comment       string `json:"comment"`
		}{phase, physicalRunID, approval.CheckpointID, approval.Decision, approval.Comment})
	}
	if err != nil {
		return Item{}, err
	}
	now := runner.clock.Now().UTC()
	grant := Item{
		ID:     stableID("hdispatch", invocation.ID, invocation.ExecutionRefID, identifier),
		TurnID: invocation.TurnID, InvocationID: invocation.ID, RunID: invocation.ExecutionRefID,
		Kind: ItemDiagnostic, Status: ItemStarted,
		ParentItemID: ExecutionDispatchParentItemID, Payload: payload,
		CreatedAt: now, UpdatedAt: now,
	}
	_, fresh, appendErr := runner.store.AppendItem(ctx, grant)
	if errors.Is(appendErr, ErrConflict) {
		repaired, repairErr := runner.recoverOriginalPhysicalDispatch(ctx, invocation)
		if repairErr != nil {
			return Item{}, errors.Join(ErrConflict, repairErr)
		}
		if repaired {
			_, fresh, appendErr = runner.store.AppendItem(ctx, grant)
		}
	}
	if appendErr != nil || !fresh {
		return Item{}, errors.Join(ErrConflict, appendErr)
	}
	if parentErr := runner.requireLiveInvocationParents(ctx, invocation); parentErr != nil {
		return Item{}, errors.Join(parentErr,
			runner.finishPhysicalDispatchGrant(ctx, grant, "physical_dispatch_rejected"))
	}
	return grant, nil
}

// The release is appended under the same immutable grant ID, including after
// caller cancellation. A duplicate ACK has the same deterministic identity.
func (runner *Runner) requireLiveInvocationParents(ctx context.Context, invocation Invocation) error {
	if invocation.ParentItemID == "" {
		return nil
	}
	proof, err := frozenChildRoot(invocation)
	if err != nil {
		return err
	}
	root, err := runner.loadFrozenChildRoot(ctx, invocation)
	if err != nil {
		return err
	}
	// Derive the same ordered transitive Attempt selection as admission.
	// Do not adopt a current Item owner or permit a missing ancestor.
	parents, err := runner.freezeChildParentAttempts(ctx, invocation.TurnID, invocation.ParentItemID, root, invocation.ID)
	if err != nil || !slices.Equal(parents, proof.Parents) {
		return errors.Join(ErrConflict, err)
	}
	if runner.runtime == nil {
		return ErrConflict
	}
	state, err := runner.runtime.Load(ctx, root.ExecutionRefID)
	if err != nil || state.Run.ID != root.ExecutionRefID || terminalRuntimeStatus(state.Run.Status) {
		return errors.Join(ErrConflict, err)
	}
	return validateRootRuntimeOwnership(root, state)
}

func (runner *Runner) finishPhysicalDispatchGrant(ctx context.Context, grant Item, boundary string) error {
	// The caller may have disconnected while the physical Feature returned.
	settlementCtx, stopSettlement := cancellationSettlementContext(ctx)
	defer stopSettlement()
	settlementCtx, boundary, err := runner.qualifyApprovalSettlement(settlementCtx, grant, boundary)
	if err != nil {
		return err
	}
	now := runner.clock.Now().UTC()
	payload, err := json.Marshal(struct {
		Boundary string `json:"boundary"`
	}{Boundary: boundary})
	if err != nil {
		return err
	}
	_, fresh, err := runner.store.AppendItem(settlementCtx, Item{
		ID: stableID("hdispatchdone", grant.ID), TurnID: grant.TurnID, InvocationID: grant.InvocationID, RunID: grant.RunID,
		Kind: ItemDiagnostic, Status: ItemCompleted, ParentItemID: grant.ID, Payload: payload, CreatedAt: now, UpdatedAt: now,
	})
	if err == nil && fresh && boundary == "physical_dispatch_returned" && runner.turnFeed != nil {
		if a, ok := originalDispatchApproval(grant); ok {
			// Durable projection was part of the Store transaction. The
			// feed is only a live notification, not a second write.
			if item, e := runner.store.GetItem(settlementCtx, stableID("hia", grant.TurnID, a.CheckpointID, "decision")); e == nil {
				publishItemFact(settlementCtx, runner.turnFeed, item)
			}
		}
	}
	return errors.Join(err, settlementCtx.Err())
}
