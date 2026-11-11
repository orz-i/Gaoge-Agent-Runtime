package harnesspostgres_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"testing"
	"time"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
)

// Effect results are scoped to their ORIGINAL operation, not just RunID:
// a failed Control/Resume may have no effect on a still-active original Run.
func TestTerminalNoEffectMustBeOriginalStartNotControlOrResume(t *testing.T) {
	for name, create := range rootInvocationStores() {
		for _, tc := range []struct {
			phase    string
			boundary string
		}{
			{"resolve-approval", "native_agent_control_no_effect"},
			{"grant-call-allowance", "native_agent_control_no_effect"},
			{"resume", "physical_dispatch_rejected"},
		} {
			t.Run(name+"/"+tc.phase, func(t *testing.T) {
				store := create(t)
				now := time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)
				turn := harness.Turn{
					ID: "turn-" + name + "-" + tc.phase, SessionID: "session-" + name,
					HostTurn:         harness.HostRef{Kind: "conversation_turn", ID: "host-" + tc.phase},
					ConfigSnapshotID: "config", Status: harness.TurnRunning,
					Revision: 1, CreatedAt: now, UpdatedAt: now,
				}
				if _, fresh, err := store.CreateTurn(t.Context(), turn); err != nil || !fresh {
					t.Fatalf("turn: fresh=%t err=%v", fresh, err)
				}
				input := json.RawMessage(`{"task":"original Kernel still running"}`)
				hash := sha256.Sum256(input)
				owner := harness.Invocation{
					ID: "root-" + name + "-" + tc.phase, TurnID: turn.ID,
					CapabilityKey:     harness.CapabilityAgent,
					DefinitionVersion: harness.RuntimeCapabilityVersion,
					ExecutionClass:    harness.ExecutionAgent,
					ExecutionRefID:    "original-active-run-" + name,
					Input:             input, InputHash: hex.EncodeToString(hash[:]),
					Status: harness.InvocationRunning, Attempt: 1, Revision: 1,
					CreatedAt: now, UpdatedAt: now, OutputRefs: []harness.HostRef{},
				}
				if _, fresh, err := store.CreateInvocation(t.Context(), owner); err != nil || !fresh {
					t.Fatalf("owner: fresh=%t err=%v", fresh, err)
				}
				grant := harness.Item{
					ID:     sqlProofFactID("hcontrol", turn.ID, tc.phase),
					TurnID: turn.ID, InvocationID: owner.ID, RunID: owner.ExecutionRefID,
					Kind: harness.ItemDiagnostic, Status: harness.ItemStarted,
					ParentItemID: harness.ExecutionDispatchParentItemID,
					Payload:      json.RawMessage(`{"phase":"` + tc.phase + `","physicalRunID":"` + owner.ExecutionRefID + `"}`),
					CreatedAt:    now, UpdatedAt: now,
				}
				if _, fresh, err := store.AppendItem(t.Context(), grant); err != nil || !fresh {
					t.Fatalf("control grant: fresh=%t err=%v", fresh, err)
				}
				done := harness.Item{
					ID: sqlProofFactID("hdispatchdone", grant.ID), TurnID: turn.ID,
					InvocationID: owner.ID, RunID: owner.ExecutionRefID,
					Kind: harness.ItemDiagnostic, Status: harness.ItemCompleted,
					ParentItemID: grant.ID,
					Payload:      json.RawMessage(`{"boundary":"` + tc.boundary + `"}`),
					CreatedAt:    now, UpdatedAt: now,
				}
				if _, fresh, err := store.AppendItem(t.Context(), done); err != nil || !fresh {
					t.Fatalf("valid control no-effect: fresh=%t err=%v", fresh, err)
				}
				items, err := store.ListItems(t.Context(), turn.ID, 0, 25)
				if err != nil {
					t.Fatal(err)
				}
				if harness.VerifiedOriginalStartNoEffect(items, owner) {
					t.Fatal("control or resume no-effect was promoted into original Start no-effect")
				}
				failed := owner
				failed.Status = harness.InvocationFailed
				if _, err := store.UpdateInvocation(t.Context(), failed, owner.Revision); !errors.Is(err, harness.ErrConflict) {
					t.Fatalf("control-only no-effect forged InvocationFailed: %v", err)
				}
				target := turn
				target.Status = harness.TurnFailed
				if _, err := store.UpdateTurn(t.Context(), target, turn.Revision); !errors.Is(err, harness.ErrConflict) {
					t.Fatalf("control-only no-effect forged TurnFailed: %v", err)
				}
				if _, err := store.RetryInvocation(t.Context(), owner.ID, owner.Revision, "another-physical-run", now.Add(time.Second)); !errors.Is(err, harness.ErrConflict) {
					t.Fatalf("still-running original Run allowed a second Attempt: %v", err)
				}
			})
		}
	}
}

func TestTerminalNoEffectStartBoundariesRemainPositive(t *testing.T) {
	for name, create := range rootInvocationStores() {
		for _, tc := range []struct{ phase, boundary string }{
			{"start", "physical_dispatch_rejected"},
			{"start", "feature_start_no_effect"},
			{"start-retry", "native_agent_start_no_effect"},
		} {
			t.Run(name+"/"+tc.phase+"/"+tc.boundary, func(t *testing.T) {
				store := create(t)
				now := time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)
				turn := harness.Turn{ID: "start-" + name + "-" + tc.phase + "-" + tc.boundary,
					SessionID: "session", HostTurn: harness.HostRef{Kind: "conversation_turn", ID: "host"},
					ConfigSnapshotID: "config", Status: harness.TurnRunning, Revision: 1,
					CreatedAt: now, UpdatedAt: now}
				if _, fresh, err := store.CreateTurn(t.Context(), turn); err != nil || !fresh {
					t.Fatalf("turn: fresh=%t err=%v", fresh, err)
				}
				owner := harness.Invocation{ID: "root-" + turn.ID, TurnID: turn.ID,
					CapabilityKey: harness.CapabilityAgent, ExecutionClass: harness.ExecutionAgent,
					ExecutionRefID: "original-start-" + name, Status: harness.InvocationRunning,
					Attempt: 1, Revision: 1, CreatedAt: now, UpdatedAt: now}
				if tc.phase == "start-retry" {
					// The first-party retry Start has a new Attempt and Run.
					// It may not borrow the original Attempt 1 failure.
					owner.Attempt = 2
				}
				if _, fresh, err := store.CreateInvocation(t.Context(), owner); err != nil || !fresh {
					t.Fatalf("owner: fresh=%t err=%v", fresh, err)
				}
				grant := harness.Item{ID: sqlProofFactID("horiginalstart", turn.ID),
					TurnID: turn.ID, InvocationID: owner.ID, RunID: owner.ExecutionRefID,
					Kind: harness.ItemDiagnostic, Status: harness.ItemStarted,
					ParentItemID: harness.ExecutionDispatchParentItemID,
					Payload:      json.RawMessage(`{"phase":"` + tc.phase + `","physicalRunID":"` + owner.ExecutionRefID + `"}`),
					CreatedAt:    now, UpdatedAt: now}
				if _, fresh, err := store.AppendItem(t.Context(), grant); err != nil || !fresh {
					t.Fatalf("grant: fresh=%t err=%v", fresh, err)
				}
				done := harness.Item{ID: sqlProofFactID("hdispatchdone", grant.ID),
					TurnID: turn.ID, InvocationID: owner.ID, RunID: owner.ExecutionRefID,
					Kind: harness.ItemDiagnostic, Status: harness.ItemCompleted,
					ParentItemID: grant.ID,
					Payload:      json.RawMessage(`{"boundary":"` + tc.boundary + `"}`),
					CreatedAt:    now, UpdatedAt: now}
				if _, fresh, err := store.AppendItem(t.Context(), done); err != nil || !fresh {
					t.Fatalf("return: fresh=%t err=%v", fresh, err)
				}
				items, err := store.ListItems(t.Context(), turn.ID, 0, 25)
				if err != nil {
					t.Fatal(err)
				}
				if !harness.VerifiedOriginalStartNoEffect(items, owner) {
					t.Fatal("original no-effect Start receipt was not recognized")
				}
				failed := owner
				failed.Status = harness.InvocationFailed
				if _, err := store.UpdateInvocation(t.Context(), failed, owner.Revision); err != nil {
					t.Fatalf("original Start no-effect did not authorize Failed: %v", err)
				}
			})
		}
	}
}
