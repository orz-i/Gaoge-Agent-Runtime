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

// A first PostgreSQL connection commits the exact no-effect records. A
// second independent connection must read them inside the original Turn
// transaction lock and reject a terminal Turn until its child also has an
// original terminal outcome. Once both are terminal, Retry may rotate only
// the explicitly selected root Attempt.
func TestRealPostgresOriginalNoEffectParentAndChildTerminalCASAcrossConnections(t *testing.T) {
	first, second, _ := realAuthorityStorePair(t)
	now := time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)
	turn := harness.Turn{
		ID: "pg-noeffect-terminal-turn", SessionID: "pg-noeffect-session",
		HostTurn:         harness.HostRef{Kind: "conversation_turn", ID: "pg-noeffect-host"},
		ConfigSnapshotID: "pg-noeffect-config", Status: harness.TurnRunning,
		Revision: 1, CreatedAt: now, UpdatedAt: now,
	}
	if _, fresh, err := first.CreateTurn(t.Context(), turn); err != nil || !fresh {
		t.Fatalf("Turn creation: fresh=%v err=%v", fresh, err)
	}
	input := json.RawMessage(`{"goal":"verified pre-dispatch rejection"}`)
	sum := sha256.Sum256(input)
	root := harness.Invocation{
		ID: "pg-noeffect-root", TurnID: turn.ID,
		CapabilityKey:     harness.CapabilityAgent,
		DefinitionVersion: harness.RuntimeCapabilityVersion,
		ExecutionClass:    harness.ExecutionAgent,
		ExecutionRefID:    "pg-noeffect-original-root-run",
		Input:             input, InputHash: hex.EncodeToString(sum[:]),
		Status: harness.InvocationRunning, Attempt: 1, Revision: 1,
		OutputRefs: []harness.HostRef{}, CreatedAt: now, UpdatedAt: now,
	}
	if _, fresh, err := first.CreateInvocation(t.Context(), root); err != nil || !fresh {
		t.Fatalf("original parent: fresh=%v err=%v", fresh, err)
	}
	child := root
	child.ID = "pg-noeffect-child"
	child.ExecutionRefID = "pg-noeffect-original-child-run"
	child.ParentItemID = "original-child-parent"
	child.ExecutionClass = harness.ExecutionTeam
	child.CapabilityKey = harness.CapabilityTeam
	child.Status = harness.InvocationAccepted
	if _, fresh, err := first.CreateInvocation(t.Context(), child); err != nil || !fresh {
		t.Fatalf("original child: fresh=%v err=%v", fresh, err)
	}

	failedRoot := root
	failedRoot.Status = harness.InvocationFailed
	if _, err := second.UpdateInvocation(t.Context(), failedRoot, root.Revision); !errors.Is(err, harness.ErrConflict) {
		t.Fatalf("second connection accepted Failed without original physical proof: %v", err)
	}
	recordTestOriginalNoEffect(t, first, root, now)
	recordTestOriginalNoEffect(t, first, child, now)
	updatedRoot, err := second.UpdateInvocation(t.Context(), failedRoot, root.Revision)
	if err != nil {
		t.Fatalf("second connection rejected original root no-effect receipt: %v", err)
	}
	failedTurn := turn
	failedTurn.Status = harness.TurnFailed
	if _, err := second.UpdateTurn(t.Context(), failedTurn, turn.Revision); !errors.Is(err, harness.ErrConflict) {
		t.Fatalf("second connection hid nonterminal child in terminal Turn: %v", err)
	}
	failedChild := child
	failedChild.Status = harness.InvocationFailed
	if _, err := second.UpdateInvocation(t.Context(), failedChild, child.Revision); err != nil {
		t.Fatalf("second connection rejected original child no-effect receipt: %v", err)
	}
	if _, err := second.UpdateTurn(t.Context(), failedTurn, turn.Revision); err != nil {
		t.Fatalf("verified original parent and child no-effect receipts did not release terminal Turn: %v", err)
	}
	next, err := second.RetryInvocation(
		t.Context(), root.ID, updatedRoot.Revision, "pg-noeffect-next-attempt", now.Add(time.Second),
	)
	if err != nil || next.Attempt != 2 || next.Status != harness.InvocationAccepted ||
		next.ExecutionRefID != "pg-noeffect-next-attempt" {
		t.Fatalf("verified cross-connection terminal and safe Retry failed: %+v err=%v", next, err)
	}
}

// Independent physical PostgreSQL connections must not infer the original
// Kernel Run failed merely because a later Control did nothing. The same
// invariant also protects any pre-hard-cut malformed Failed rows on restart.
func TestRealPostgresControlNoEffectCannotGrantSecondPhysicalAttempt(t *testing.T) {
	first, second, db := realAuthorityStorePair(t)
	now := time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)
	turn := harness.Turn{
		ID: "pg-control-terminal-turn", SessionID: "pg-control-session",
		HostTurn:         harness.HostRef{Kind: "conversation_turn", ID: "pg-control-host"},
		ConfigSnapshotID: "frozen-config", Status: harness.TurnRunning,
		Revision: 1, CreatedAt: now, UpdatedAt: now,
	}
	if _, fresh, err := first.CreateTurn(t.Context(), turn); err != nil || !fresh {
		t.Fatalf("Turn: fresh=%t err=%v", fresh, err)
	}
	input := json.RawMessage(`{"goal":"original Kernel may still run"}`)
	digest := sha256.Sum256(input)
	owner := harness.Invocation{
		ID: "pg-control-root", TurnID: turn.ID,
		CapabilityKey:     harness.CapabilityAgent,
		DefinitionVersion: harness.RuntimeCapabilityVersion,
		ExecutionClass:    harness.ExecutionAgent,
		ExecutionRefID:    "pg-original-running-kernel",
		Input:             input, InputHash: hex.EncodeToString(digest[:]),
		Status: harness.InvocationRunning, Attempt: 1, Revision: 1,
		OutputRefs: []harness.HostRef{}, CreatedAt: now, UpdatedAt: now,
	}
	if _, fresh, err := first.CreateInvocation(t.Context(), owner); err != nil || !fresh {
		t.Fatalf("Invocation: fresh=%t err=%v", fresh, err)
	}
	control := harness.Item{
		ID: "pg-original-control", TurnID: turn.ID,
		InvocationID: owner.ID, RunID: owner.ExecutionRefID,
		Kind: harness.ItemDiagnostic, Status: harness.ItemStarted,
		ParentItemID: harness.ExecutionDispatchParentItemID,
		Payload:      json.RawMessage(`{"phase":"resolve-approval","physicalRunID":"` + owner.ExecutionRefID + `"}`),
		CreatedAt:    now, UpdatedAt: now,
	}
	if _, fresh, err := first.AppendItem(t.Context(), control); err != nil || !fresh {
		t.Fatalf("Control grant: fresh=%t err=%v", fresh, err)
	}
	receipt := harness.Item{
		ID: sqlProofFactID("hdispatchdone", control.ID), TurnID: turn.ID,
		InvocationID: owner.ID, RunID: owner.ExecutionRefID,
		Kind: harness.ItemDiagnostic, Status: harness.ItemCompleted,
		ParentItemID: control.ID,
		Payload:      json.RawMessage(`{"boundary":"native_agent_control_no_effect"}`),
		CreatedAt:    now, UpdatedAt: now,
	}
	if _, fresh, err := first.AppendItem(t.Context(), receipt); err != nil || !fresh {
		t.Fatalf("Control no-effect: fresh=%t err=%v", fresh, err)
	}
	failed := owner
	failed.Status = harness.InvocationFailed
	if _, err := second.UpdateInvocation(t.Context(), failed, owner.Revision); !errors.Is(err, harness.ErrConflict) {
		t.Fatalf("connection B forged InvocationFailed from Control no-effect: %v", err)
	}
	terminalTurn := turn
	terminalTurn.Status = harness.TurnFailed
	if _, err := second.UpdateTurn(t.Context(), terminalTurn, turn.Revision); !errors.Is(err, harness.ErrConflict) {
		t.Fatalf("connection B forged TurnFailed from Control no-effect: %v", err)
	}
	// Mimic legacy rows written before the protocol hard cut. The original
	// Retry transaction must independently re-evaluate physical lineage.
	if err := db.Exec("UPDATE agent_harness_invocations SET status = ?, revision = revision + 1 WHERE id = ?",
		string(harness.InvocationFailed), owner.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("UPDATE agent_harness_turns SET status = ? WHERE id = ?",
		string(harness.TurnFailed), turn.ID).Error; err != nil {
		t.Fatal(err)
	}
	old, err := second.GetInvocation(t.Context(), owner.ID)
	if err != nil || old.Status != harness.InvocationFailed {
		t.Fatalf("historical fault injection: %+v err=%v", old, err)
	}
	if _, err := second.RetryInvocation(t.Context(), owner.ID, old.Revision,
		"pg-untrusted-second-run", now.Add(time.Second)); !errors.Is(err, harness.ErrConflict) {
		t.Fatalf("historical Control-only no-effect minted another physical Run: %v", err)
	}
	unchanged, err := first.GetInvocation(t.Context(), owner.ID)
	if err != nil || unchanged.Attempt != owner.Attempt ||
		unchanged.ExecutionRefID != owner.ExecutionRefID {
		t.Fatalf("failed Retry changed original physical identity: %+v err=%v", unchanged, err)
	}
}

// A real PostgreSQL historical record may have been terminalized by the old
// overbroad control no-effect rule. Even when its original Start returned
// and a later Control was harmless, the original Kernel might STILL run.
// The second connection cannot rotate that original Run ID without a
// separately verified, exact terminal Kernel witness.
func TestRealPostgresEffectfulOriginalStartAndControlNoEffectCannotGrantRetry(t *testing.T) {
	first, second, db := realAuthorityStorePair(t)
	now := time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)
	turn := harness.Turn{
		ID: "pg-original-effectful-start-turn", SessionID: "pg-original-effectful-session",
		HostTurn:         harness.HostRef{Kind: "conversation_turn", ID: "pg-original-effectful-host"},
		ConfigSnapshotID: "frozen", Status: harness.TurnRunning,
		Revision: 1, CreatedAt: now, UpdatedAt: now,
	}
	if _, fresh, err := first.CreateTurn(t.Context(), turn); err != nil || !fresh {
		t.Fatalf("Turn: fresh=%t err=%v", fresh, err)
	}
	input := json.RawMessage(`{"goal":"an original, possibly running Agent"}`)
	digest := sha256.Sum256(input)
	owner := harness.Invocation{
		ID: "pg-effectful-original", TurnID: turn.ID,
		CapabilityKey:     harness.CapabilityAgent,
		DefinitionVersion: harness.RuntimeCapabilityVersion,
		ExecutionClass:    harness.ExecutionAgent,
		Input:             input, InputHash: hex.EncodeToString(digest[:]),
		ExecutionRefID: "original-effectful-running-run",
		Status:         harness.InvocationRunning,
		Revision:       1, Attempt: 1, OutputRefs: []harness.HostRef{},
		CreatedAt: now, UpdatedAt: now,
	}
	if _, fresh, err := first.CreateInvocation(t.Context(), owner); err != nil || !fresh {
		t.Fatalf("Invocation: fresh=%t err=%v", fresh, err)
	}
	for _, phase := range []struct {
		id, operation, boundary string
	}{
		{"effectful-original-start", "start", "physical_dispatch_returned"},
		{"later-harmless-control", "resolve-approval", "native_agent_control_no_effect"},
	} {
		grant := harness.Item{
			ID: phase.id, TurnID: turn.ID,
			InvocationID: owner.ID, RunID: owner.ExecutionRefID,
			Kind: harness.ItemDiagnostic, Status: harness.ItemStarted,
			ParentItemID: harness.ExecutionDispatchParentItemID,
			Payload:      json.RawMessage(`{"phase":"` + phase.operation + `","physicalRunID":"` + owner.ExecutionRefID + `"}`),
			CreatedAt:    now, UpdatedAt: now,
		}
		if _, fresh, err := first.AppendItem(t.Context(), grant); err != nil || !fresh {
			t.Fatalf("grant %s: fresh=%t err=%v", phase.operation, fresh, err)
		}
		receipt := harness.Item{
			ID: sqlProofFactID("hdispatchdone", grant.ID), TurnID: turn.ID,
			InvocationID: owner.ID, RunID: owner.ExecutionRefID,
			Kind: harness.ItemDiagnostic, Status: harness.ItemCompleted,
			ParentItemID: grant.ID,
			Payload:      json.RawMessage(`{"boundary":"` + phase.boundary + `"}`),
			CreatedAt:    now, UpdatedAt: now,
		}
		if _, fresh, err := first.AppendItem(t.Context(), receipt); err != nil || !fresh {
			t.Fatalf("receipt %s: fresh=%t err=%v", phase.boundary, fresh, err)
		}
	}
	failed := owner
	failed.Status = harness.InvocationFailed
	if _, err := second.UpdateInvocation(t.Context(), failed, owner.Revision); !errors.Is(err, harness.ErrConflict) {
		t.Fatalf("active original Start falsely failed from later Control result: %v", err)
	}
	// Fault-inject the former Store's historical wrong terminal projection
	// via a privileged direct DB write. The production Store never uses
	// this path; its Retry must remain fail-closed on restart.
	if err := db.Exec("UPDATE agent_harness_invocations SET status = ?, revision = revision + 1 WHERE id = ?",
		string(harness.InvocationFailed), owner.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("UPDATE agent_harness_turns SET status = ? WHERE id = ?",
		string(harness.TurnFailed), turn.ID).Error; err != nil {
		t.Fatal(err)
	}
	historical, err := second.GetInvocation(t.Context(), owner.ID)
	if err != nil || historical.Status != harness.InvocationFailed {
		t.Fatalf("historical failure injection not visible to second connection: %+v err=%v", historical, err)
	}
	if _, err := second.RetryInvocation(t.Context(), owner.ID, historical.Revision,
		"unverified-second-run", now.Add(time.Second)); !errors.Is(err, harness.ErrConflict) {
		t.Fatalf("P1: real PostgreSQL second connection rotated effectful original Start after harmless Control: %v", err)
	}
	unchanged, err := first.GetInvocation(t.Context(), owner.ID)
	if err != nil || unchanged.Attempt != owner.Attempt ||
		unchanged.ExecutionRefID != owner.ExecutionRefID {
		t.Fatalf("rejected Retry changed original Run identity: %+v err=%v", unchanged, err)
	}
}
