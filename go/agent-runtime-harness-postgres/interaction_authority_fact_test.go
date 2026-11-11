package harnesspostgres_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	harnesspostgres "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness-postgres"
)

// Test-only equivalent of the original Harness stable-ID fixture contract.
// Production SQL consumes the shared typed validator instead of deriving IDs.
func sqlProofFactID(prefix string, values ...string) string {
	hash := sha256.New()
	for _, value := range values {
		_, _ = hash.Write([]byte(strings.TrimSpace(value)))
		_, _ = hash.Write([]byte{0})
	}
	return prefix + "_" + hex.EncodeToString(hash.Sum(nil))[:24]
}

func newSQLInteractionAuthorityFixture(
	t *testing.T, suffix string,
) (*harnesspostgres.Store, harness.Interaction, harness.Invocation, [3]harness.Item) {
	t.Helper()
	store := newStore(t)
	interaction, owner, chain := seedSQLInteractionAuthorityFixture(t, store, suffix)
	return store, interaction, owner, chain
}

func seedSQLInteractionAuthorityFixture(
	t *testing.T, store *harnesspostgres.Store, suffix string,
) (harness.Interaction, harness.Invocation, [3]harness.Item) {
	t.Helper()
	turn, owner, i := createWaitingInteractionFixture(t, store, suffix)
	i.Status = harness.InteractionResolved
	i.Response = json.RawMessage("{}")
	resolved, err := store.ResolveInteraction(t.Context(), i, i.Revision)
	if err != nil {
		t.Fatalf("resolve original Interaction: %v", err)
	}
	i = resolved.Interaction
	owner.Status = harness.InvocationRunning
	owner.UpdatedAt = owner.UpdatedAt.Add(time.Second)
	owner, err = store.UpdateInvocation(t.Context(), owner, owner.Revision)
	if err != nil {
		t.Fatalf("claim original Invocation: %v", err)
	}
	turn.Status = harness.TurnRunning
	turn.UpdatedAt = turn.UpdatedAt.Add(time.Second)
	if _, err = store.UpdateTurn(t.Context(), turn, turn.Revision); err != nil {
		t.Fatalf("advance Turn: %v", err)
	}
	now := time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC)
	// The original first-party Resolved Interaction Item contains the full
	// business response and frozen presentation, not just its status.
	sourcePayload := map[string]any{
		"interactionID": i.ID, "artifactRefs": append([]harness.HostRef(nil), i.ArtifactRefs...),
		"key": i.Key, "kind": i.Kind, "schema": i.Schema,
		"status": i.Status, "response": i.Response,
	}
	if i.ApplicationRef != nil {
		sourcePayload["applicationRef"] = i.ApplicationRef
	}
	if len(i.Presentation) != 0 {
		sourcePayload["presentation"] = i.Presentation
	}
	sourceJSON, err := json.Marshal(sourcePayload)
	if err != nil {
		t.Fatal(err)
	}
	source := harness.Item{
		ID:     sqlProofFactID("hinteractionitem", i.ID, string(harness.InteractionResolved)),
		TurnID: i.TurnID, InvocationID: owner.ID, Kind: harness.ItemInteraction,
		Status:    harness.ItemCompleted,
		Payload:   sourceJSON,
		CreatedAt: now, UpdatedAt: now,
	}
	if _, fresh, err := store.AppendItem(t.Context(), source); err != nil || !fresh {
		t.Fatalf("persist original Resolved Interaction Item: fresh=%t err=%v", fresh, err)
	}
	receipt := harness.Item{
		ID:     sqlProofFactID("hbizverified", i.ID),
		TurnID: i.TurnID, InvocationID: owner.ID, RunID: owner.ExecutionRefID,
		Kind: harness.ItemDiagnostic, Status: harness.ItemCompleted, ParentItemID: source.ID,
		Payload:   json.RawMessage(`{"interactionID":"` + i.ID + `","boundary":"host_response_verified"}`),
		CreatedAt: now, UpdatedAt: now,
	}
	claim := harness.Item{
		ID:     sqlProofFactID("hcontclaim", i.ID),
		TurnID: i.TurnID, InvocationID: owner.ID, RunID: owner.ExecutionRefID,
		Kind: harness.ItemDiagnostic, Status: harness.ItemStarted, ParentItemID: receipt.ID,
		Payload:   json.RawMessage(`{"interactionID":"` + i.ID + `","boundary":"runtime_continuation_claimed"}`),
		CreatedAt: now, UpdatedAt: now,
	}
	completed := harness.Item{
		ID:     sqlProofFactID("hcontdone", i.ID),
		TurnID: i.TurnID, InvocationID: owner.ID, RunID: owner.ExecutionRefID,
		Kind: harness.ItemDiagnostic, Status: harness.ItemCompleted, ParentItemID: claim.ID,
		Payload:   json.RawMessage(`{"interactionID":"` + i.ID + `","boundary":"runtime_continuation_completed"}`),
		CreatedAt: now, UpdatedAt: now,
	}
	return i, owner, [3]harness.Item{receipt, claim, completed}
}

func TestSQLInteractionAuthorityRejectsForgedFactsBeforeCommit(t *testing.T) {
	for _, tc := range []struct {
		name   string
		stage  int
		mutate func(*harness.Item)
	}{
		{"response-wrong-run", 0, func(v *harness.Item) { v.RunID = "foreign" }},
		{"response-wrong-parent", 0, func(v *harness.Item) { v.ParentItemID = "unrelated" }},
		{"response-wrong-boundary", 0, func(v *harness.Item) {
			v.Payload = json.RawMessage(`{"interactionID":"wrong","boundary":"host_response_verified"}`)
		}},
		{"response-wrong-ID", 0, func(v *harness.Item) { v.ID = "hbizverified_forged" }},
		{"claim-wrong-run", 1, func(v *harness.Item) { v.RunID = "foreign" }},
		{"claim-wrong-parent", 1, func(v *harness.Item) { v.ParentItemID = "unrelated" }},
		{"claim-wrong-boundary", 1, func(v *harness.Item) {
			v.Payload = json.RawMessage(`{"interactionID":"wrong","boundary":"runtime_continuation_claimed"}`)
		}},
		{"claim-wrong-ID", 1, func(v *harness.Item) { v.ID = "hcontclaim_forged" }},
		{"done-wrong-run", 2, func(v *harness.Item) { v.RunID = "foreign" }},
		{"done-wrong-parent", 2, func(v *harness.Item) { v.ParentItemID = "unrelated" }},
		{"done-wrong-boundary", 2, func(v *harness.Item) {
			v.Payload = json.RawMessage(`{"interactionID":"wrong","boundary":"runtime_continuation_completed"}`)
		}},
		{"done-wrong-ID", 2, func(v *harness.Item) { v.ID = "hcontdone_forged" }},
		{"done-contradictory-alternate-ID", 2, func(v *harness.Item) { v.ID = "alternate-completion" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, _, _, chain := newSQLInteractionAuthorityFixture(t, tc.name)
			for index := 0; index < tc.stage; index++ {
				if _, fresh, err := store.AppendItem(t.Context(), chain[index]); err != nil || !fresh {
					t.Fatalf("valid stage %d rejected: fresh=%t err=%v", index, fresh, err)
				}
			}
			// Snapshot the canonical positive proof BEFORE constructing a
			// deliberately malformed negative. The two inputs must own
			// independent payload bytes: failed mutation/admission must not
			// change what the test later asks the Store to accept.
			good := chain[tc.stage]
			good.Payload = append(json.RawMessage(nil), good.Payload...)
			bad := good
			bad.Payload = append(json.RawMessage(nil), bad.Payload...)
			tc.mutate(&bad)
			if _, _, err := store.AppendItem(t.Context(), bad); !errors.Is(err, harness.ErrConflict) {
				t.Fatalf("forged %s admitted: %v", tc.name, err)
			}
			// Reject-before-commit must not reserve the canonical receipt ID.
			if _, fresh, err := store.AppendItem(t.Context(), good); err != nil || !fresh {
				t.Fatalf("canonical receipt blocked after rejecting forgery: fresh=%t err=%v", fresh, err)
			}
		})
	}
}

func TestSQLInteractionAuthorityValidChainAndReplay(t *testing.T) {
	store, i, owner, chain := newSQLInteractionAuthorityFixture(t, "verified_all")
	for _, fact := range chain {
		saved, fresh, err := store.AppendItem(t.Context(), fact)
		if err != nil || !fresh {
			t.Fatalf("valid SQL authority receipt rejected: fresh=%t err=%v", fresh, err)
		}
		if saved.Seq == 0 {
			t.Fatal("SQL fact missing original sequence")
		}
		if _, replay, err := store.AppendItem(t.Context(), fact); err != nil || replay {
			t.Fatalf("idempotent SQL receipt replay: fresh=%t err=%v", replay, err)
		}
	}
	items, err := store.ListItems(t.Context(), i.TurnID, 0, 20)
	if err != nil {
		t.Fatal(err)
	}
	verified := false
	for _, item := range items {
		if item.ID == chain[2].ID {
			// A real Store must return the same durable proof used by
			// the Harness read-side validator on restart.
			verified = harness.VerifiedInteractionAuthorityAppend(item, i, owner, items[:len(items)-1])
		}
	}
	if !verified {
		t.Fatal("saved SQL completion was not a valid authoritative continuation fact")
	}
	other := chain[2]
	other.ID = "contradictory-second-completion"
	if _, _, err := store.AppendItem(t.Context(), other); !errors.Is(err, harness.ErrConflict) {
		t.Fatalf("second different completion committed: %v", err)
	}
}

func TestSQLInteractionAuthorityLongTurnOriginalChain(t *testing.T) {
	store, interaction, _, chain := newSQLInteractionAuthorityFixture(t, "long_history")
	now := time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC)
	// More than two normal ListItems pages separate the original callback
	// from the continuation. Exact source identity must remain available
	// without materializing every unrelated historical Item in one query.
	for n := 0; n < 1300; n++ {
		if _, fresh, err := store.AppendItem(t.Context(), harness.Item{
			ID:     fmt.Sprintf("unrelated-history-%04d", n),
			TurnID: interaction.TurnID, Kind: harness.ItemArtifact,
			Status: harness.ItemCompleted, ParentItemID: "unrelated-host-history",
			Payload:   json.RawMessage(`{"unrelated":true}`),
			CreatedAt: now, UpdatedAt: now,
		}); err != nil || !fresh {
			t.Fatalf("seed history %d: fresh=%v err=%v", n, fresh, err)
		}
	}
	for index, fact := range chain {
		if _, fresh, err := store.AppendItem(t.Context(), fact); err != nil || !fresh {
			t.Fatalf("canonical chain %d lost after long history: fresh=%v err=%v", index, fresh, err)
		}
	}
	if _, repeated, err := store.AppendItem(t.Context(), chain[2]); err != nil || repeated {
		t.Fatalf("long-history continuation receipt did not replay idempotently: repeat=%v err=%v", repeated, err)
	}
}

// A noncanonical child of the resolved Host source is a conflicting effect
// receipt; Memory and SQL must reject it before any transaction can commit.
func TestSQLIndependentFinalHostSourceRejectsForeignKindSiblingBeforeCommit(t *testing.T) {
	store, interaction, owner, chain := newSQLInteractionAuthorityFixture(t, "foreign-source-child")
	sourceID := sqlProofFactID("hinteractionitem", interaction.ID, string(harness.InteractionResolved))
	artifact := harness.Item{ID: "ordinary-host-artifact", TurnID: interaction.TurnID, InvocationID: owner.ID,
		Kind: harness.ItemArtifact, Status: harness.ItemCompleted, ParentItemID: sourceID,
		Payload: json.RawMessage(`{"attachment":"business-data"}`)}
	if _, fresh, err := store.AppendItem(t.Context(), artifact); err != nil || !fresh {
		t.Fatalf("ordinary Host Artifact was blocked: fresh=%t err=%v", fresh, err)
	}
	bogus := harness.Item{ID: "unexpected-host-effect-sibling", TurnID: interaction.TurnID, InvocationID: owner.ID,
		Kind: harness.ItemDiagnostic, Status: harness.ItemCompleted, ParentItemID: sourceID,
		Payload: json.RawMessage(`{"interactionID":"` + interaction.ID + `","boundary":"other-host-effect"}`)}
	if _, fresh, err := store.AppendItem(t.Context(), bogus); !errors.Is(err, harness.ErrConflict) || fresh {
		t.Fatalf("noncanonical child of the reserved resolved Host source was committed: fresh=%t err=%v", fresh, err)
	}
	if _, fresh, err := store.AppendItem(t.Context(), chain[0]); err != nil || !fresh {
		t.Fatalf("canonical original Host receipt was blocked by rejected invalid sibling: fresh=%t err=%v", fresh, err)
	}
}

// Simulate an old row inserted outside Store admission: continuation must
// still refuse the conflicting original Host Diagnostic sibling on restart.
func TestSQLIndependentFinalHistoricalHostSiblingFencesClaim(t *testing.T) {
	store, db := newStoreWithDB(t)
	interaction, owner, chain := seedSQLInteractionAuthorityFixture(t, store, "historical-host-source-sibling")
	receipt, fresh, err := store.AppendItem(t.Context(), chain[0])
	if err != nil || !fresh {
		t.Fatalf("seed legitimate Host response: fresh=%t err=%v", fresh, err)
	}
	now := time.Now().UTC()
	sourceID := sqlProofFactID("hinteractionitem", interaction.ID, string(harness.InteractionResolved))
	if err := db.Exec(`INSERT INTO agent_harness_items
  (id,turn_id,seq,kind,status,run_id,invocation_id,parent_item_id,payload_json,created_at,updated_at)
  VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
		"historical-alternate-host-response", interaction.TurnID, receipt.Seq+1, string(harness.ItemDiagnostic),
		string(harness.ItemCompleted), owner.ExecutionRefID, owner.ID, sourceID,
		`{"interactionID":"`+interaction.ID+`","boundary":"host_response_verified"}`, now, now).Error; err != nil {
		t.Fatal(err)
	}
	if _, fresh, err := store.AppendItem(t.Context(), chain[1]); !errors.Is(err, harness.ErrConflict) || fresh {
		t.Fatalf("historical competing Host response silently granted continuation: fresh=%t err=%v", fresh, err)
	}
}

// An out-of-band wrong-Turn Diagnostic sibling cannot be allowed to
// disappear solely because a SQL reader filtered on the original Turn ID.
func TestSQLIndependentCrossTurnHostSiblingFailClosed(t *testing.T) {
	store, db := newStoreWithDB(t)
	interaction, owner, chain := seedSQLInteractionAuthorityFixture(t, store, "cross-turn-sibling-red")
	receipt, fresh, err := store.AppendItem(t.Context(), chain[0])
	if err != nil || !fresh {
		t.Fatalf("seed response: fresh=%t err=%v", fresh, err)
	}
	now := time.Now().UTC()
	sourceID := sqlProofFactID("hinteractionitem", interaction.ID, string(harness.InteractionResolved))
	if err := db.Exec(`INSERT INTO agent_harness_items
  (id,turn_id,seq,kind,status,run_id,invocation_id,parent_item_id,payload_json,created_at,updated_at)
  VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
		"foreign-turn-host-diagnostic", "historical-foreign-turn", 1,
		string(harness.ItemDiagnostic), string(harness.ItemCompleted), owner.ExecutionRefID,
		owner.ID, sourceID, `{"interactionID":"`+interaction.ID+`","boundary":"host_response_verified"}`,
		now, now).Error; err != nil {
		t.Fatal(err)
	}
	if _, fresh, err := store.AppendItem(t.Context(), chain[1]); !errors.Is(err, harness.ErrConflict) || fresh {
		t.Fatalf("foreign-turn historical Diagnostic sibling silently allowed Runtime continuation: fresh=%t err=%v; canonical receipt Seq=%d", fresh, err, receipt.Seq)
	}
}

// Public terminal projection and append admission must inspect the SAME
// unique Host proof facts across Turns. A wrong-Turn historical Diagnostic
// must not be filtered out before the authoritative Host predicate runs.
func TestSQLIndependentTerminalProjectionRejectsCrossTurnHostSibling(t *testing.T) {
	store, db := newStoreWithDB(t)
	interaction, owner, chain := seedSQLInteractionAuthorityFixture(t, store, "cross-turn-terminal-projection")
	for _, fact := range chain {
		if _, fresh, err := store.AppendItem(t.Context(), fact); err != nil || !fresh {
			t.Fatalf("valid prerequisite proof rejected: fresh=%t err=%v", fresh, err)
		}
	}
	if err := harnesspostgres.CheckTerminalProjectionForTest(t.Context(), store, interaction.TurnID, owner.ID); err != nil {
		t.Fatalf("original complete Host chain was rejected: %v", err)
	}
	sourceID := sqlProofFactID("hinteractionitem", interaction.ID, string(harness.InteractionResolved))
	now := time.Now().UTC()
	if err := db.Exec(`INSERT INTO agent_harness_items
  (id,turn_id,seq,kind,status,run_id,invocation_id,parent_item_id,payload_json,created_at,updated_at)
  VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
		"wrong-turn-terminal-host-diagnostic", "wrong-turn-terminal", 1,
		string(harness.ItemDiagnostic), string(harness.ItemCompleted), owner.ExecutionRefID, owner.ID, sourceID,
		`{"interactionID":"`+interaction.ID+`","boundary":"host_response_verified"}`, now, now).Error; err != nil {
		t.Fatal(err)
	}
	if err := harnesspostgres.CheckTerminalProjectionForTest(t.Context(), store, interaction.TurnID, owner.ID); !errors.Is(err, harness.ErrConflict) {
		t.Fatalf("terminal reader ignored foreign-Turn competing Host Diagnostic: %v", err)
	}
}

// Benign Attachments under the canonical source are not alternate receipts.
// Terminal evidence scans only a bounded, type-specific set of children.
func TestSQLIndependentTerminalProjectionPreservesLongArtifactHistory(t *testing.T) {
	store, interaction, owner, chain := newSQLInteractionAuthorityFixture(t, "long-host-source-attachments")
	// All 1300 Artifacts attach to the original Resolved Interaction;
	// they are normal business attachments, not Host effect receipts.
	sourceID := sqlProofFactID("hinteractionitem", interaction.ID, string(harness.InteractionResolved))
	for index := 0; index < 1300; index++ {
		artifact := harness.Item{ID: fmt.Sprintf("host-artifact-%04d", index), TurnID: interaction.TurnID,
			InvocationID: owner.ID, Kind: harness.ItemArtifact, Status: harness.ItemCompleted,
			ParentItemID: sourceID, Payload: json.RawMessage(`{"attachment":true}`)}
		if _, fresh, err := store.AppendItem(t.Context(), artifact); err != nil || !fresh {
			t.Fatalf("legitimate Host artifact %d rejected: fresh=%t err=%v", index, fresh, err)
		}
	}
	for index, fact := range chain {
		if _, fresh, err := store.AppendItem(t.Context(), fact); err != nil || !fresh {
			t.Fatalf("canonical Host proof after large artifact history %d: fresh=%t err=%v", index, fresh, err)
		}
	}
	if err := harnesspostgres.CheckTerminalProjectionForTest(t.Context(), store, interaction.TurnID, owner.ID); err != nil {
		t.Fatalf("bounded terminal proof rejected legitimate Host artifact history: %v", err)
	}
}
