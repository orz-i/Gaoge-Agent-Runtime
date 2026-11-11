package harnesspostgres_test

import (
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	harnesspostgres "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness-postgres"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// Separate SQL handles model the two Harness processes which could race
// an irreversible physical operation with a cancellation or continuation.
func realAuthorityStorePair(t *testing.T) (*harnesspostgres.Store, *harnesspostgres.Store, *gorm.DB) {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	db, isolated := openIsolatedHarnessPostgres(t, dsn)
	if err := harnesspostgres.Migrate(db); err != nil {
		t.Fatal(err)
	}
	first, err := harnesspostgres.New(db)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(isolated)
	if err != nil {
		t.Fatal(err)
	}
	remoteDB, err := gorm.Open(postgres.Open(parsed.String()), realHarnessPostgresTestConfig())
	if err != nil {
		t.Fatal(err)
	}
	conn, err := remoteDB.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	second, err := harnesspostgres.New(remoteDB)
	if err != nil {
		t.Fatal(err)
	}
	return first, second, db
}

func TestRealPostgresInteractionAuthorityAcrossConnections(t *testing.T) {
	first, second, db := realAuthorityStorePair(t)
	interaction, owner, chain := seedSQLInteractionAuthorityFixture(t, first, "real_chain")
	for index, original := range chain {
		bad := original
		switch index {
		case 0:
			bad.RunID = "another-original-run"
		case 1:
			bad.Payload = json.RawMessage(`{"interactionID":"` + interaction.ID + `","boundary":"not_a_continuation_claim"}`)
		case 2:
			bad.ParentItemID = "another-claim"
		}
		if _, _, err := second.AppendItem(t.Context(), bad); !errors.Is(err, harness.ErrConflict) {
			t.Fatalf("second SQL process accepted forged stage %d: %v", index, err)
		}
		if _, fresh, err := first.AppendItem(t.Context(), original); err != nil || !fresh {
			t.Fatalf("first SQL process failed canonical stage %d: fresh=%t err=%v", index, fresh, err)
		}
		if _, repeated, err := second.AppendItem(t.Context(), original); err != nil || repeated {
			t.Fatalf("second SQL process replayed the same effect at stage %d: fresh=%t err=%v", index, repeated, err)
		}
	}
	observed, err := second.ListItems(t.Context(), interaction.TurnID, 0, 20)
	if err != nil {
		t.Fatal(err)
	}
	originalCompletion := observed[len(observed)-1]
	if !harness.VerifiedInteractionAuthorityAppend(originalCompletion, interaction, owner, observed[:len(observed)-1]) {
		t.Fatal("second SQL process could not read the exact original valid completed proof")
	}
	// Privileged disk corruption is outside Store.AppendItem, but its
	// subsequent read must never turn a forged business receipt into proof.
	if err := db.Exec("UPDATE agent_harness_items SET payload_json = ? WHERE id = ?",
		`{"interactionID":"wrong","boundary":"host_response_verified"}`, chain[0].ID).Error; err != nil {
		t.Fatal(err)
	}
	observed, err = second.ListItems(t.Context(), interaction.TurnID, 0, 20)
	if err != nil {
		t.Fatal(err)
	}
	if harness.VerifiedInteractionAuthorityAppend(observed[len(observed)-1], interaction, owner, observed[:len(observed)-1]) {
		t.Fatal("corrupted historical Host response gained Runtime completion authority")
	}
}

func TestRealPostgresCancellationReceiptOwnerAcrossConnections(t *testing.T) {
	first, second, _ := realAuthorityStorePair(t)
	turn, owner, _ := createWaitingInteractionFixture(t, first, "real_cancel_owner")
	now := time.Now().UTC()
	intent := harness.Item{
		ID: "cancel-original", TurnID: turn.ID, RunID: owner.ExecutionRefID,
		Kind: harness.ItemDiagnostic, Status: harness.ItemStarted,
		ParentItemID: harness.CancellationFenceParentItemID,
		Payload:      json.RawMessage(`{"boundary":"cancel_scope_pending","runID":"` + owner.ExecutionRefID + `"}`),
		CreatedAt:    now, UpdatedAt: now,
	}
	if _, fresh, err := first.AppendItem(t.Context(), intent); err != nil || !fresh {
		t.Fatalf("persist original cancellation: fresh=%t err=%v", fresh, err)
	}
	settled := harness.Item{
		ID: sqlProofFactID("hcancelclosed", intent.ID), TurnID: turn.ID, RunID: intent.RunID,
		Kind: harness.ItemDiagnostic, Status: harness.ItemCompleted,
		ParentItemID: intent.ID, Payload: json.RawMessage(`{"boundary":"cancel_scope_settled"}`),
		CreatedAt: now, UpdatedAt: now,
	}
	bad := settled
	bad.RunID = "foreign-run"
	if _, _, err := second.AppendItem(t.Context(), bad); !errors.Is(err, harness.ErrConflict) {
		t.Fatalf("second process forged a cancellation completion: %v", err)
	}
	next := intent
	next.ID = "cancel-follow-up"
	if _, _, err := second.AppendItem(t.Context(), next); !errors.Is(err, harness.ErrConflict) {
		t.Fatalf("original pending fence was lifted by foreign completion: %v", err)
	}
	if _, fresh, err := first.AppendItem(t.Context(), settled); err != nil || !fresh {
		t.Fatalf("commit valid settlement: fresh=%t err=%v", fresh, err)
	}
	if _, fresh, err := second.AppendItem(t.Context(), next); err != nil || !fresh {
		t.Fatalf("verified original settlement did not release next cancellation: fresh=%t err=%v", fresh, err)
	}
}

// The original Host receipt's sole Diagnostic sibling is a typed authority
// invariant across independent SQL connections, not a local-memory filter.
// Damaged historic rows remain blocking after a process restart.
func TestRealPostgresIndependentHostSourceSiblingCannotAuthorizeContinuation(t *testing.T) {
	first, second, db := realAuthorityStorePair(t)
	interaction, owner, chain := seedSQLInteractionAuthorityFixture(t, first, "real-host-sibling")
	sourceID := sqlProofFactID("hinteractionitem", interaction.ID, string(harness.InteractionResolved))
	foreign := harness.Item{ID: "alternate-host-diagnostic", TurnID: interaction.TurnID,
		InvocationID: owner.ID, RunID: owner.ExecutionRefID,
		Kind: harness.ItemDiagnostic, Status: harness.ItemCompleted, ParentItemID: sourceID,
		Payload:   json.RawMessage(`{"interactionID":"` + interaction.ID + `","boundary":"host_response_verified"}`),
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	if _, fresh, err := second.AppendItem(t.Context(), foreign); !errors.Is(err, harness.ErrConflict) || fresh {
		t.Fatalf("other SQL connection wrote alternate Host receipt: fresh=%t err=%v", fresh, err)
	}
	canonical, fresh, err := first.AppendItem(t.Context(), chain[0])
	if err != nil || !fresh {
		t.Fatalf("original Host receipt rejected after foreign receipt denial: fresh=%t err=%v", fresh, err)
	}
	if _, fresh, err := second.AppendItem(t.Context(), chain[1]); err != nil || !fresh {
		t.Fatalf("original continuation claim rejected on other SQL connection: fresh=%t err=%v", fresh, err)
	}
	// Privileged historical corruption is outside the AppendItem contract.
	// Restarted readers must not silently turn the old conflicting row into
	// authorization for a new continuation or terminal projection.
	now := time.Now().UTC()
	if err := db.Exec(`INSERT INTO agent_harness_items
   (id,turn_id,seq,kind,status,run_id,invocation_id,parent_item_id,payload_json,created_at,updated_at)
   VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
		"historical-host-receipt-conflict", interaction.TurnID, canonical.Seq+2,
		string(harness.ItemDiagnostic), string(harness.ItemCompleted), owner.ExecutionRefID, owner.ID,
		sourceID, `{"interactionID":"`+interaction.ID+`","boundary":"host_response_verified"}`, now, now).Error; err != nil {
		t.Fatal(err)
	}
	if _, fresh, err := second.AppendItem(t.Context(), chain[2]); !errors.Is(err, harness.ErrConflict) || fresh {
		t.Fatalf("other SQL connection allowed a completion over a malformed original Host sibling: fresh=%t err=%v", fresh, err)
	}
}

// The two-connection PostgreSQL proof reader must not pre-filter a malformed
// historical Host sibling by Turn ID. The parent Item ID is globally unique;
// the foreign Turn is negative evidence, never proof of a harmless absence.
func TestRealPostgresIndependentCrossTurnHostSiblingBlocksContinuation(t *testing.T) {
	first, second, db := realAuthorityStorePair(t)
	interaction, owner, chain := seedSQLInteractionAuthorityFixture(t, first, "real-cross-turn-host-sibling")
	if _, fresh, err := first.AppendItem(t.Context(), chain[0]); err != nil || !fresh {
		t.Fatalf("canonical Host callback response: fresh=%t err=%v", fresh, err)
	}
	sourceID := sqlProofFactID("hinteractionitem", interaction.ID, string(harness.InteractionResolved))
	now := time.Now().UTC()
	if err := db.Exec(`INSERT INTO agent_harness_items
   (id,turn_id,seq,kind,status,run_id,invocation_id,parent_item_id,payload_json,created_at,updated_at)
   VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
		"real-foreign-turn-host-diagnostic", "real-foreign-turn", 1,
		string(harness.ItemDiagnostic), string(harness.ItemCompleted), owner.ExecutionRefID,
		owner.ID, sourceID, `{"interactionID":"`+interaction.ID+`","boundary":"host_response_verified"}`,
		now, now).Error; err != nil {
		t.Fatal(err)
	}
	if _, fresh, err := second.AppendItem(t.Context(), chain[1]); !errors.Is(err, harness.ErrConflict) || fresh {
		t.Fatalf("remote SQL connection ignored wrong-Turn historical Host sibling: fresh=%t err=%v", fresh, err)
	}
}

// The globally-scoped SQL Host/Handoff proof readers require this index on
// PostgreSQL too, not just SQLite fixtures. A fresh isolated schema exercises
// the exact production Migrate call before a second process observes it.
func TestRealPostgresIndependentParentProofIndexAcrossConnections(t *testing.T) {
	_, second, db := realAuthorityStorePair(t)
	var indexed struct {
		IndexDef string `gorm:"column:indexdef"`
	}
	if err := db.Raw(`SELECT indexdef FROM pg_indexes
		WHERE schemaname = current_schema() AND tablename = ? AND indexname = ?`,
		"agent_harness_items", "idx_harness_item_parent").Scan(&indexed).Error; err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(indexed.IndexDef, "parent_item_id") {
		t.Fatalf("new PostgreSQL original-parent index absent or wrong: %q", indexed.IndexDef)
	}
	if _, err := second.GetItem(t.Context(), "does-not-exist"); !errors.Is(err, harness.ErrNotFound) {
		t.Fatalf("second connection cannot use the migrated Store: %v", err)
	}
}
