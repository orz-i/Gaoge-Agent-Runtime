package harness

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

// A trusted Store may encounter pre-hard-cut historical rows. In particular,
// an old Failed status could have been persisted because a *Control* returned
// no-effect, without any evidence that the original Kernel Run finished.
// Retrying must fail closed even if the status was already stored.
func TestMemoryRetryRejectsHistoricalControlOnlyFailedWithoutOriginalStart(t *testing.T) {
	store, turn, original := auditTerminalMemoryRoot(t, "old-control-only", TurnRunning)
	now := time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)
	original.Input = json.RawMessage(`{"goal":"original still may be running"}`)
	original.InputHash = hashInvocationBytes(original.Input)
	store.invocations[original.ID] = original
	control := Item{
		ID: "historical-control", TurnID: turn.ID,
		InvocationID: original.ID, RunID: original.ExecutionRefID,
		Kind: ItemDiagnostic, Status: ItemStarted,
		ParentItemID: ExecutionDispatchParentItemID,
		Payload:      json.RawMessage(`{"phase":"resolve-approval","physicalRunID":"` + original.ExecutionRefID + `"}`),
		CreatedAt:    now, UpdatedAt: now,
	}
	if _, fresh, err := store.AppendItem(t.Context(), control); err != nil || !fresh {
		t.Fatalf("control grant: fresh=%t err=%v", fresh, err)
	}
	receipt := Item{
		ID: stableID("hdispatchdone", control.ID), TurnID: turn.ID,
		InvocationID: original.ID, RunID: original.ExecutionRefID,
		Kind: ItemDiagnostic, Status: ItemCompleted,
		ParentItemID: control.ID,
		Payload:      json.RawMessage(`{"boundary":"native_agent_control_no_effect"}`),
		CreatedAt:    now, UpdatedAt: now,
	}
	if _, fresh, err := store.AppendItem(t.Context(), receipt); err != nil || !fresh {
		t.Fatalf("original Control result: fresh=%t err=%v", fresh, err)
	}
	if RetryOriginalOperationEvidenceValid(t.Context(), store.items[turn.ID], original) {
		t.Fatal("historical Control result impersonated original Start evidence")
	}
	// Fault injection models an already persisted pre-hard-cut incorrect
	// terminal projection, not a permitted modern Store.UpdateInvocation.
	corrupted := original
	corrupted.Status = InvocationFailed
	corrupted.Revision++
	store.invocations[original.ID] = corrupted
	turn.Status = TurnFailed
	store.turns[turn.ID] = turn
	if _, err := store.RetryInvocation(t.Context(), original.ID, corrupted.Revision,
		"new-physical-run", now.Add(time.Second)); !errors.Is(err, ErrConflict) {
		t.Fatalf("historical misattributed terminal status granted Attempt 2: %v", err)
	}
	persisted := store.invocations[original.ID]
	if persisted.Attempt != original.Attempt || persisted.ExecutionRefID != original.ExecutionRefID {
		t.Fatalf("retry corrupted original identity: %+v", persisted)
	}
}

func TestMemoryRetryRejectsHistoricalEffectfulStartPlusHarmlessControl(t *testing.T) {
	store, turn, owner := auditTerminalMemoryRoot(t, "effectful-and-control", TurnRunning)
	now := time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)
	owner.Input = json.RawMessage(`{"goal":"original Kernel may remain running"}`)
	owner.InputHash = hashInvocationBytes(owner.Input)
	store.invocations[owner.ID] = owner
	start := Item{
		ID: "original-start", TurnID: turn.ID,
		InvocationID: owner.ID, RunID: owner.ExecutionRefID,
		Kind: ItemDiagnostic, Status: ItemStarted,
		ParentItemID: ExecutionDispatchParentItemID,
		Payload:      json.RawMessage(`{"phase":"start","physicalRunID":"` + owner.ExecutionRefID + `"}`),
	}
	started := Item{ID: stableID("hdispatchdone", start.ID), TurnID: turn.ID,
		InvocationID: owner.ID, RunID: owner.ExecutionRefID,
		Kind: ItemDiagnostic, Status: ItemCompleted, ParentItemID: start.ID,
		Payload: json.RawMessage(`{"boundary":"physical_dispatch_returned"}`)}
	control := Item{ID: "original-control", TurnID: turn.ID,
		InvocationID: owner.ID, RunID: owner.ExecutionRefID,
		Kind: ItemDiagnostic, Status: ItemStarted,
		ParentItemID: ExecutionDispatchParentItemID,
		Payload:      json.RawMessage(`{"phase":"resolve-approval","physicalRunID":"` + owner.ExecutionRefID + `"}`)}
	noEffect := Item{ID: stableID("hdispatchdone", control.ID), TurnID: turn.ID,
		InvocationID: owner.ID, RunID: owner.ExecutionRefID,
		Kind: ItemDiagnostic, Status: ItemCompleted, ParentItemID: control.ID,
		Payload: json.RawMessage(`{"boundary":"native_agent_control_no_effect"}`)}
	for _, fact := range []Item{start, started, control, noEffect} {
		if _, fresh, err := store.AppendItem(t.Context(), fact); err != nil || !fresh {
			t.Fatalf("original physical history %s fresh=%t err=%v", fact.ID, fresh, err)
		}
	}
	// The old terminal predicate could misattribute Control no-effect to
	// original Kernel failure. Even that already committed historical
	// mistake may not provide a second execution identity.
	old := owner
	old.Status = InvocationFailed
	old.Revision++
	store.invocations[owner.ID] = old
	turn.Status = TurnFailed
	store.turns[turn.ID] = turn
	if RetryOriginalOperationEvidenceValid(t.Context(), store.items[turn.ID], old) {
		t.Fatal("effectful Start and harmless Control passed without original Kernel terminal witness")
	}
	if _, err := store.RetryInvocation(t.Context(), old.ID, old.Revision, "unsafe-second-run",
		now.Add(time.Second)); !errors.Is(err, ErrConflict) {
		t.Fatalf("original Kernel may still run; historical Failed minted Attempt 2: %v", err)
	}
	if store.invocations[owner.ID].Attempt != owner.Attempt {
		t.Fatal("rejected Retry mutated the original Attempt")
	}
}

func TestMemoryRetryEffectfulStartRequiresExactKernelTerminalWitness(t *testing.T) {
	store, turn, owner := auditTerminalMemoryRoot(t, "kernel-terminal-witness", TurnRunning)
	now := time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)
	owner.Input = json.RawMessage(`{"goal":"actual terminal Kernel"}`)
	owner.InputHash = hashInvocationBytes(owner.Input)
	store.invocations[owner.ID] = owner
	grant := Item{
		ID: "effectful-original-start", TurnID: turn.ID,
		InvocationID: owner.ID, RunID: owner.ExecutionRefID,
		Kind: ItemDiagnostic, Status: ItemStarted,
		ParentItemID: ExecutionDispatchParentItemID,
		Payload:      json.RawMessage(`{"phase":"start","physicalRunID":"` + owner.ExecutionRefID + `"}`),
	}
	done := Item{ID: stableID("hdispatchdone", grant.ID), TurnID: turn.ID,
		InvocationID: owner.ID, RunID: owner.ExecutionRefID,
		Kind: ItemDiagnostic, Status: ItemCompleted, ParentItemID: grant.ID,
		Payload: json.RawMessage(`{"boundary":"physical_dispatch_returned"}`)}
	for _, fact := range []Item{grant, done} {
		if _, fresh, err := store.AppendItem(t.Context(), fact); err != nil || !fresh {
			t.Fatalf("original Started/Returned fact: fresh=%t err=%v", fresh, err)
		}
	}
	// Simulate the already-authorized private Kernel terminal projection,
	// not an arbitrary Control/no-effect terminal status.
	failed := owner
	failed.Status = InvocationFailed
	failed.Revision++
	store.invocations[owner.ID] = failed
	turn.Status = TurnFailed
	store.turns[turn.ID] = turn
	if _, err := store.RetryInvocation(t.Context(), owner.ID, failed.Revision,
		"without-original-witness", now.Add(time.Second)); !errors.Is(err, ErrConflict) {
		t.Fatalf("effectful Start was retried without terminal Kernel proof: %v", err)
	}
	if _, err := withOriginalKernelRetryWitness(t.Context(), failed, kernel.Snapshot{
		Run: kernel.Run{ID: owner.ExecutionRefID, Status: kernel.RunStatusRunning},
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("nonterminal Kernel minted retry authority: %v", err)
	}
	if _, err := withOriginalKernelRetryWitness(t.Context(), failed, kernel.Snapshot{
		Run: kernel.Run{ID: "foreign-kernel-run", Status: kernel.RunStatusFailed},
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("foreign Kernel minted retry authority: %v", err)
	}
	ctx, err := withOriginalKernelRetryWitness(t.Context(), failed, kernel.Snapshot{
		Run: kernel.Run{ID: owner.ExecutionRefID, Status: kernel.RunStatusFailed},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !RetryOriginalOperationEvidenceValid(ctx, store.items[turn.ID], failed) {
		t.Fatal("matching original terminal Kernel witness was rejected")
	}
	retried, err := store.RetryInvocation(ctx, owner.ID, failed.Revision,
		"original-kernel-verified-next-run", now.Add(time.Second))
	if err != nil || retried.Status != InvocationAccepted || retried.Attempt != 2 ||
		retried.ExecutionRefID != "original-kernel-verified-next-run" {
		t.Fatalf("legitimate terminal Kernel Retry was rejected: %+v err=%v", retried, err)
	}
}
