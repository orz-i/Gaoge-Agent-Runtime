package harnesspostgres_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"
	"time"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
)

// The reverse, positive half of the terminal protocol: after both original
// parent and child physical Starts have first-party definite no-effect
// receipts, each Invocation may safely project Failed; the Turn may become
// Failed only AFTER its child is terminal, then the parent's retry rotates
// exactly one Attempt. Exercise the same contract on Memory and SQLite.
func TestOriginalNoEffectParentAndChildAllowLegitimateRetryAcrossStores(t *testing.T) {
	for name, create := range rootInvocationStores() {
		t.Run(name, func(t *testing.T) {
			store := create(t)
			now := time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)
			turn := harness.Turn{
				ID: "turn-no-effect-" + name, SessionID: "session-no-effect-" + name,
				HostTurn:         harness.HostRef{Kind: "conversation_turn", ID: "host-no-effect-" + name},
				ConfigSnapshotID: "frozen", Status: harness.TurnRunning,
				Revision: 1, CreatedAt: now, UpdatedAt: now,
			}
			if _, fresh, err := store.CreateTurn(t.Context(), turn); err != nil || !fresh {
				t.Fatalf("create Turn: fresh=%v err=%v", fresh, err)
			}
			input := json.RawMessage(`{"goal":"retry only after exact no effect"}`)
			hash := sha256.Sum256(input)
			root := harness.Invocation{
				ID: "root-no-effect-" + name, TurnID: turn.ID,
				CapabilityKey:     harness.CapabilityAgent,
				DefinitionVersion: harness.RuntimeCapabilityVersion,
				ExecutionClass:    harness.ExecutionAgent,
				ExecutionRefID:    "original-root-run-" + name,
				Input:             input, InputHash: hex.EncodeToString(hash[:]),
				Status: harness.InvocationRunning, Attempt: 1, Revision: 1,
				CreatedAt: now, UpdatedAt: now, OutputRefs: []harness.HostRef{},
			}
			if _, fresh, err := store.CreateInvocation(t.Context(), root); err != nil || !fresh {
				t.Fatalf("create root: fresh=%v err=%v", fresh, err)
			}
			child := root
			child.ID = "child-no-effect-" + name
			child.ParentItemID = "original-parent-item"
			child.ExecutionClass = harness.ExecutionTeam
			child.CapabilityKey = harness.CapabilityTeam
			child.ExecutionRefID = "original-child-run-" + name
			child.Status = harness.InvocationAccepted
			if _, fresh, err := store.CreateInvocation(t.Context(), child); err != nil || !fresh {
				t.Fatalf("create child: fresh=%v err=%v", fresh, err)
			}
			recordTestOriginalNoEffect(t, store, root, now)
			recordTestOriginalNoEffect(t, store, child, now)
			failedRoot := root
			failedRoot.Status = harness.InvocationFailed
			updatedRoot, err := store.UpdateInvocation(t.Context(), failedRoot, root.Revision)
			if err != nil {
				t.Fatalf("root original no-effect did not allow failed status: %v", err)
			}
			terminalTurn := turn
			terminalTurn.Status = harness.TurnFailed
			if _, err := store.UpdateTurn(t.Context(), terminalTurn, turn.Revision); err == nil {
				t.Fatal("nonterminal child was ignored during parent terminalization")
			}
			failedChild := child
			failedChild.Status = harness.InvocationFailed
			if _, err := store.UpdateInvocation(t.Context(), failedChild, child.Revision); err != nil {
				t.Fatalf("child original no-effect did not allow failed status: %v", err)
			}
			if _, err := store.UpdateTurn(t.Context(), terminalTurn, turn.Revision); err != nil {
				t.Fatalf("all original no-effect receipts did not allow terminal Turn: %v", err)
			}
			nextRun := "next-run-" + name
			next, err := store.RetryInvocation(t.Context(), root.ID, updatedRoot.Revision, nextRun, now.Add(time.Second))
			if err != nil || next.ExecutionRefID != nextRun || next.Attempt != 2 ||
				next.Status != harness.InvocationAccepted {
				t.Fatalf("verified parent/child no-effect proof did not allow legitimate Attempt rotation: %+v err=%v", next, err)
			}
		})
	}
}
