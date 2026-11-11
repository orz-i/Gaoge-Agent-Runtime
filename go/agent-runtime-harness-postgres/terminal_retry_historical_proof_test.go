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

func TestSQLRetryRejectsHistoricalControlOnlyFailedWithoutOriginalStart(t *testing.T) {
	store, db := newStoreWithDB(t)
	now := time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)
	turn := harness.Turn{
		ID: "legacy-control-turn", SessionID: "session-control",
		HostTurn:         harness.HostRef{Kind: "conversation_turn", ID: "host-control"},
		ConfigSnapshotID: "frozen", Status: harness.TurnRunning,
		Revision: 1, CreatedAt: now, UpdatedAt: now,
	}
	if _, fresh, err := store.CreateTurn(t.Context(), turn); err != nil || !fresh {
		t.Fatalf("Turn: fresh=%t err=%v", fresh, err)
	}
	input := json.RawMessage(`{"goal":"possibly active original Kernel Run"}`)
	digest := sha256.Sum256(input)
	owner := harness.Invocation{
		ID: "legacy-control-root", TurnID: turn.ID,
		CapabilityKey: harness.CapabilityAgent, ExecutionClass: harness.ExecutionAgent,
		DefinitionVersion: harness.RuntimeCapabilityVersion,
		Input:             input, InputHash: hex.EncodeToString(digest[:]),
		ExecutionRefID: "possibly-running-kernel", Status: harness.InvocationRunning,
		Revision: 1, Attempt: 1, OutputRefs: []harness.HostRef{},
		CreatedAt: now, UpdatedAt: now,
	}
	if _, fresh, err := store.CreateInvocation(t.Context(), owner); err != nil || !fresh {
		t.Fatalf("owner: fresh=%t err=%v", fresh, err)
	}
	grant := harness.Item{
		ID: "historical-control", TurnID: turn.ID,
		InvocationID: owner.ID, RunID: owner.ExecutionRefID,
		Kind: harness.ItemDiagnostic, Status: harness.ItemStarted,
		ParentItemID: harness.ExecutionDispatchParentItemID,
		Payload:      json.RawMessage(`{"phase":"resolve-approval","physicalRunID":"` + owner.ExecutionRefID + `"}`),
		CreatedAt:    now, UpdatedAt: now,
	}
	if _, fresh, err := store.AppendItem(t.Context(), grant); err != nil || !fresh {
		t.Fatalf("grant: fresh=%t err=%v", fresh, err)
	}
	receipt := harness.Item{
		ID: sqlProofFactID("hdispatchdone", grant.ID), TurnID: turn.ID,
		InvocationID: owner.ID, RunID: owner.ExecutionRefID,
		Kind: harness.ItemDiagnostic, Status: harness.ItemCompleted,
		ParentItemID: grant.ID,
		Payload:      json.RawMessage(`{"boundary":"native_agent_control_no_effect"}`),
		CreatedAt:    now, UpdatedAt: now,
	}
	if _, fresh, err := store.AppendItem(t.Context(), receipt); err != nil || !fresh {
		t.Fatalf("Control completion: fresh=%t err=%v", fresh, err)
	}
	// A privileged fault injector mimics a historical row committed by
	// a pre-hard-cut Store with an overbroad no-effect terminal predicate.
	if err := db.Exec("UPDATE agent_harness_invocations SET status = ?, revision = revision + 1 WHERE id = ?",
		string(harness.InvocationFailed), owner.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("UPDATE agent_harness_turns SET status = ? WHERE id = ?",
		string(harness.TurnFailed), turn.ID).Error; err != nil {
		t.Fatal(err)
	}
	historical, err := store.GetInvocation(t.Context(), owner.ID)
	if err != nil || historical.Status != harness.InvocationFailed {
		t.Fatalf("fault injection failed: %+v err=%v", historical, err)
	}
	if _, err := store.RetryInvocation(t.Context(), owner.ID, historical.Revision,
		"second-physical-run", now.Add(time.Second)); !errors.Is(err, harness.ErrConflict) {
		t.Fatalf("historical Control-only Failed row granted a second Attempt: %v", err)
	}
	unchanged, err := store.GetInvocation(t.Context(), owner.ID)
	if err != nil || unchanged.Attempt != owner.Attempt ||
		unchanged.ExecutionRefID != owner.ExecutionRefID {
		t.Fatalf("rejected attempt mutated original: %+v err=%v", unchanged, err)
	}
}

func TestSQLRetryRejectsHistoricalEffectfulStartPlusHarmlessControl(t *testing.T) {
	store, db := newStoreWithDB(t)
	now := time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)
	turn := harness.Turn{
		ID: "legacy-effectful-control-turn", SessionID: "legacy-effectful-session",
		HostTurn:         harness.HostRef{Kind: "conversation_turn", ID: "legacy-effectful-host"},
		ConfigSnapshotID: "frozen", Status: harness.TurnRunning,
		Revision: 1, CreatedAt: now, UpdatedAt: now,
	}
	if _, fresh, err := store.CreateTurn(t.Context(), turn); err != nil || !fresh {
		t.Fatalf("Turn: fresh=%t err=%v", fresh, err)
	}
	input := json.RawMessage(`{"goal":"original Start returned effectful"}`)
	digest := sha256.Sum256(input)
	owner := harness.Invocation{
		ID: "legacy-effectful-control-root", TurnID: turn.ID,
		CapabilityKey: harness.CapabilityAgent, ExecutionClass: harness.ExecutionAgent,
		DefinitionVersion: harness.RuntimeCapabilityVersion,
		Input:             input, InputHash: hex.EncodeToString(digest[:]),
		ExecutionRefID: "legacy-potentially-active-run",
		Status:         harness.InvocationRunning,
		Revision:       1, Attempt: 1, OutputRefs: []harness.HostRef{},
		CreatedAt: now, UpdatedAt: now,
	}
	if _, fresh, err := store.CreateInvocation(t.Context(), owner); err != nil || !fresh {
		t.Fatalf("owner: fresh=%t err=%v", fresh, err)
	}
	start := harness.Item{
		ID: "legacy-effectful-start", TurnID: turn.ID,
		InvocationID: owner.ID, RunID: owner.ExecutionRefID,
		Kind: harness.ItemDiagnostic, Status: harness.ItemStarted,
		ParentItemID: harness.ExecutionDispatchParentItemID,
		Payload:      json.RawMessage(`{"phase":"start","physicalRunID":"` + owner.ExecutionRefID + `"}`),
		CreatedAt:    now, UpdatedAt: now,
	}
	started := harness.Item{
		ID: sqlProofFactID("hdispatchdone", start.ID), TurnID: turn.ID,
		InvocationID: owner.ID, RunID: owner.ExecutionRefID,
		Kind: harness.ItemDiagnostic, Status: harness.ItemCompleted,
		ParentItemID: start.ID,
		Payload:      json.RawMessage(`{"boundary":"physical_dispatch_returned"}`),
		CreatedAt:    now, UpdatedAt: now,
	}
	control := harness.Item{
		ID: "legacy-harmless-control", TurnID: turn.ID,
		InvocationID: owner.ID, RunID: owner.ExecutionRefID,
		Kind: harness.ItemDiagnostic, Status: harness.ItemStarted,
		ParentItemID: harness.ExecutionDispatchParentItemID,
		Payload:      json.RawMessage(`{"phase":"resolve-approval","physicalRunID":"` + owner.ExecutionRefID + `"}`),
		CreatedAt:    now, UpdatedAt: now,
	}
	noEffect := harness.Item{
		ID: sqlProofFactID("hdispatchdone", control.ID), TurnID: turn.ID,
		InvocationID: owner.ID, RunID: owner.ExecutionRefID,
		Kind: harness.ItemDiagnostic, Status: harness.ItemCompleted,
		ParentItemID: control.ID,
		Payload:      json.RawMessage(`{"boundary":"native_agent_control_no_effect"}`),
		CreatedAt:    now, UpdatedAt: now,
	}
	for _, fact := range []harness.Item{start, started, control, noEffect} {
		if _, fresh, err := store.AppendItem(t.Context(), fact); err != nil || !fresh {
			t.Fatalf("original fact %s: fresh=%t err=%v", fact.ID, fresh, err)
		}
	}
	// Historical fault injection bypasses the corrected terminal CAS.
	if err := db.Exec("UPDATE agent_harness_invocations SET status = ?, revision = revision + 1 WHERE id = ?",
		string(harness.InvocationFailed), owner.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("UPDATE agent_harness_turns SET status = ? WHERE id = ?",
		string(harness.TurnFailed), turn.ID).Error; err != nil {
		t.Fatal(err)
	}
	old, err := store.GetInvocation(t.Context(), owner.ID)
	if err != nil || old.Status != harness.InvocationFailed {
		t.Fatalf("fault injection: %+v err=%v", old, err)
	}
	if _, err := store.RetryInvocation(t.Context(), owner.ID, old.Revision,
		"unsafe-next-physical-run", now.Add(time.Second)); !errors.Is(err, harness.ErrConflict) {
		t.Fatalf("effectful original Start plus harmless Control minted a second Attempt: %v", err)
	}
	unchanged, err := store.GetInvocation(t.Context(), owner.ID)
	if err != nil || unchanged.Attempt != 1 ||
		unchanged.ExecutionRefID != owner.ExecutionRefID {
		t.Fatalf("rejected Retry modified the original: %+v err=%v", unchanged, err)
	}
}
