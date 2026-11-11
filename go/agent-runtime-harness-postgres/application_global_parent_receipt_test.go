package harnesspostgres_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	harnesspostgres "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness-postgres"
	"gorm.io/gorm"
)

// Real Host and Agent effects run through the original Harness Runner.
// ONLY the historical corrupt child Item is inserted directly. The modern
// first-party AppendItem contract prevents such foreign-Turn writes.
type pr103GlobalOriginalEvidenceCase struct {
	name             string
	parent           string // dispatch, host-source, host-receipt
	kind             harness.ItemKind
	foreignTurn      bool
	reservedHostID   bool
	ordinaryArtifact bool
}

func pr103SQLGlobalOriginalEvidenceSibling(t *testing.T, db *gorm.DB, tc pr103GlobalOriginalEvidenceCase) {
	t.Helper()
	base := pr103SQLBacking(t, db)
	fault := &pr103ApplicationSQLFault{Store: base}
	runtime := pr103SQLRuntime(t)
	feature := &pr103ApplicationSQLAgent{runtime: runtime}
	executor := &pr103ApplicationSQLExecutor{}
	handler := &pr103ApplicationSQLHandler{}
	runner := pr103SQLRunner(t, fault, runtime, feature, executor, handler)
	turnID, parentItemID := pr103SQLSeedRootAgent(t, base, runtime)
	initial, err := runner.StartApplicationInvocation(t.Context(), turnID, harness.ApplicationInvocationRequest{
		ParentItemID: parentItemID, RequestID: "sql-global-receipt-child",
		Goal: "original decision", CapabilityKey: "story.author_decision",
		DefinitionVersion: "v1", Input: json.RawMessage("{}"),
	})
	if err != nil || len(initial.Interactions) != 1 {
		t.Fatalf("original Application Host interaction: %v", err)
	}
	interaction := initial.Interactions[0]
	_, err = runner.ResolveInteraction(t.Context(), turnID, interaction.ID, pr103SQLResponse())
	if err == nil || !fault.childReopen.Load() {
		t.Fatalf("post-original-child fault was not injected: %v", err)
	}
	restarted := pr103SQLRunner(t, pr103SQLBacking(t, db), runtime, feature, executor, handler)
	_, err = restarted.ResumeTurn(t.Context(), turnID)
	if err != nil || feature.resumes != 1 || handler.calls != 1 || executor.calls != 1 {
		t.Fatalf("original parent physical Resume not returned: err=%v parent=%d host=%d app=%d",
			err, feature.resumes, handler.calls, executor.calls)
	}
	if _, err = restarted.ReconcileInteraction(t.Context(), turnID, interaction.ID); err != nil {
		t.Fatalf("baseline original settled parent receipt invalid: %v", err)
	}
	if _, err = restarted.ResolveInteraction(t.Context(), turnID, interaction.ID, pr103SQLResponse()); err != nil {
		t.Fatalf("baseline product response replay invalid: %v", err)
	}

	items, err := base.ListItems(t.Context(), turnID, 0, 500)
	if err != nil {
		t.Fatal(err)
	}
	var grant, hostSource, hostReceipt harness.Item
	for _, item := range items {
		if item.ParentItemID == harness.ExecutionDispatchParentItemID && item.InvocationID != interaction.InvocationID {
			phase, ok := harness.VerifiedExecutionDispatchGrant(item)
			if ok && phase == "resume" {
				grant = item
			}
		}
		if item.Kind == harness.ItemInteraction && item.Status == harness.ItemCompleted &&
			item.InvocationID == interaction.InvocationID && strings.HasPrefix(item.ID, "hinteractionitem_") {
			hostSource = item
		}
		if item.Kind == harness.ItemDiagnostic && item.Status == harness.ItemCompleted &&
			item.InvocationID == interaction.InvocationID && strings.HasPrefix(item.ID, "hbizverified_") {
			hostReceipt = item
		}
	}
	if grant.ID == "" || hostSource.ID == "" || hostReceipt.ID == "" || hostReceipt.ParentItemID != hostSource.ID {
		t.Fatalf("no complete original proof chain: dispatch=%s source=%s host=%s", grant.ID, hostSource.ID, hostReceipt.ID)
	}
	physical, err := base.ListItemChildren(t.Context(), grant.ID, harness.ItemChildProofAll)
	if err != nil || len(physical) != 1 {
		t.Fatalf("valid original physical Grant should have one child: count=%d err=%v", len(physical), err)
	}
	if boundary, valid := harness.VerifiedExecutionDispatchReceipt(grant, physical); !valid || boundary != "physical_dispatch_returned" {
		t.Fatalf("invalid original physical receipt: %s %v", boundary, valid)
	}
	source, err := base.ListItemChildren(t.Context(), hostSource.ID, harness.ItemChildProofHostAuthority)
	if err != nil || len(source) != 1 || source[0].ID != hostReceipt.ID {
		t.Fatalf("original Host source fact not unique: count=%d err=%v", len(source), err)
	}

	parentID := grant.ID
	scope := harness.ItemChildProofAll
	baselineCount := 1
	switch tc.parent {
	case "dispatch":
	case "host-source":
		parentID = hostSource.ID
		scope = harness.ItemChildProofHostAuthority
	case "host-receipt":
		parentID = hostReceipt.ID
		baselineCount = 0
	case "orphan-claim", "orphan-done":
		parentID = "unrelated-historical-parent"
		baselineCount = 0
	default:
		t.Fatalf("invalid proof parent: %q", tc.parent)
	}

	siblingTurn := "foreign-corrupt-history"
	if !tc.foreignTurn {
		siblingTurn = turnID
	}
	id := fmt.Sprintf("historical-%s-%s-%t", tc.parent, tc.kind, tc.foreignTurn)
	if tc.reservedHostID {
		id = "hbizverified_other_historical_authority"
	}
	if tc.parent == "orphan-claim" {
		id = pr103RetryID("hcontclaim", interaction.ID)
	}
	if tc.parent == "orphan-done" {
		id = pr103RetryID("hcontdone", interaction.ID)
	}
	insert := db.Exec(`INSERT INTO agent_harness_items
		(id,turn_id,seq,kind,status,run_id,invocation_id,parent_item_id,payload_json,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
		id, siblingTurn, 1000000, string(tc.kind), string(harness.ItemCompleted),
		grant.RunID, grant.InvocationID, parentID, "{}", time.Now().UTC(), time.Now().UTC(),
	)
	if insert.Error != nil {
		t.Fatalf("cannot seed historical inconsistent sibling: %v", insert.Error)
	}
	globally, err := base.ListItemChildren(t.Context(), parentID, scope)
	expected := baselineCount + 1
	if tc.ordinaryArtifact {
		expected = baselineCount
	}
	if err != nil || len(globally) != expected {
		t.Fatalf("global proof reader hid/elevated original sibling: expected=%d actual=%d err=%v",
			expected, len(globally), err)
	}
	if tc.parent == "dispatch" {
		if boundary, valid := harness.VerifiedExecutionDispatchReceipt(grant, globally); valid {
			t.Fatalf("typed receipt check accepted contradictory original siblings: %s", boundary)
		}
	}

	_, err = restarted.ReconcileInteraction(t.Context(), turnID, interaction.ID)
	if tc.ordinaryArtifact {
		if err != nil {
			t.Fatalf("ordinary Artifact incorrectly invalidated valid Host source: %v", err)
		}
		_, err = restarted.ResolveInteraction(t.Context(), turnID, interaction.ID, pr103SQLResponse())
		if err != nil {
			t.Fatalf("idempotent original Host replay rejected ordinary Artifact: %v", err)
		}
	} else {
		if !errors.Is(err, harness.ErrConflict) {
			t.Fatalf("historical %s cross-Turn sibling silently settled original proof: %v", tc.parent, err)
		}
		_, err = restarted.ResolveInteraction(t.Context(), turnID, interaction.ID, pr103SQLResponse())
		if !errors.Is(err, harness.ErrConflict) {
			t.Fatalf("response replay bypassed global %s completeness: %v", tc.parent, err)
		}
	}
	if handler.calls != 1 || executor.calls != 1 || feature.resumes != 1 {
		t.Fatalf("recovery repeated physical work: host=%d executor=%d parent=%d",
			handler.calls, executor.calls, feature.resumes)
	}
}

var pr103GlobalOriginalEvidenceCases = []pr103GlobalOriginalEvidenceCase{
	{"dispatch-foreign-Turn-Artifact", "dispatch", harness.ItemArtifact, true, false, false},
	{"dispatch-foreign-Turn-Diagnostic", "dispatch", harness.ItemDiagnostic, true, false, false},
	{"dispatch-same-Turn-Artifact", "dispatch", harness.ItemArtifact, false, false, false},
	{"host-source-foreign-Turn-Diagnostic", "host-source", harness.ItemDiagnostic, true, false, false},
	{"host-source-reserved-ID-Artifact", "host-source", harness.ItemArtifact, true, true, false},
	{"host-receipt-foreign-Turn-Artifact", "host-receipt", harness.ItemArtifact, true, false, false},
	{"orphan-foreign-Turn-claim-ID", "orphan-claim", harness.ItemDiagnostic, true, false, false},
	{"orphan-foreign-Turn-done-ID", "orphan-done", harness.ItemDiagnostic, true, false, false},
	{"host-source-ordinary-Artifact-remains-data", "host-source", harness.ItemArtifact, true, false, true},
}

func TestSQLApplicationGlobalParentReceiptHistoricalCorruption(t *testing.T) {
	for _, tc := range pr103GlobalOriginalEvidenceCases {
		t.Run(tc.name, func(t *testing.T) {
			_, db := newStoreWithDB(t)
			pr103SQLGlobalOriginalEvidenceSibling(t, db, tc)
		})
	}
}

func TestRealPostgresApplicationGlobalParentReceiptHistoricalCorruption(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	for _, tc := range pr103GlobalOriginalEvidenceCases {
		t.Run(tc.name, func(t *testing.T) {
			db, _ := openIsolatedHarnessPostgres(t, dsn)
			if err := harnesspostgres.Migrate(db); err != nil {
				t.Fatal(err)
			}
			pr103SQLGlobalOriginalEvidenceSibling(t, db, tc)
		})
	}
}
