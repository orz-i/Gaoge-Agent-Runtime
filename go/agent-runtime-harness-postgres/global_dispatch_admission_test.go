package harnesspostgres_test

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	harnesspostgres "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness-postgres"
	"gorm.io/gorm"
)

// The same globally unique physical grant and malformed historical sibling
// are exercised against real SQL Store admission, not only the Application
// read-only recovery endpoint. No custom test Store implementation is used.
func pr103SQLGlobalPhysicalAdmission(t *testing.T, db *gorm.DB, operation string, corrupted bool) {
	t.Helper()
	store := pr103SQLBacking(t, db)
	runtime := pr103SQLRuntime(t)
	turnID, _ := pr103SQLSeedRootAgent(t, store, runtime)
	root, err := store.GetInvocation(t.Context(), pr103RootID(t, store, turnID))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	phase, boundary := "start", "physical_dispatch_rejected"
	if operation == "new-physical-grant" {
		phase, boundary = "resume", "physical_dispatch_returned"
	}
	id := "hdispatch_original_store_global_admission"
	payload, err := json.Marshal(struct {
		Phase         string `json:"phase"`
		PhysicalRunID string `json:"physicalRunID"`
	}{phase, root.ExecutionRefID})
	if err != nil {
		t.Fatal(err)
	}
	grant, created, err := store.AppendItem(t.Context(), harness.Item{
		ID: id, TurnID: turnID, Kind: harness.ItemDiagnostic,
		Status: harness.ItemStarted, InvocationID: root.ID,
		RunID: root.ExecutionRefID, ParentItemID: harness.ExecutionDispatchParentItemID,
		Payload: payload, CreatedAt: now, UpdatedAt: now,
	})
	if err != nil || !created {
		t.Fatalf("original physical grant: created=%v err=%v", created, err)
	}
	_, created, err = store.AppendItem(t.Context(), harness.Item{
		ID: pr103RetryID("hdispatchdone", id), TurnID: turnID,
		Kind: harness.ItemDiagnostic, Status: harness.ItemCompleted,
		InvocationID: root.ID, RunID: root.ExecutionRefID,
		ParentItemID: grant.ID,
		Payload:      json.RawMessage(`{"boundary":"` + boundary + `"}`),
		CreatedAt:    now, UpdatedAt: now,
	})
	if err != nil || !created {
		t.Fatalf("original physical completion: created=%v err=%v", created, err)
	}
	complete, err := store.ListItemChildren(t.Context(), grant.ID, harness.ItemChildProofAll)
	if err != nil || len(complete) != 1 {
		t.Fatalf("original complete dispatch receipt: count=%d err=%v", len(complete), err)
	}
	if got, ok := harness.VerifiedExecutionDispatchReceipt(grant, complete); !ok || got != boundary {
		t.Fatalf("original valid physical receipt: boundary=%s ok=%v", got, ok)
	}

	// Ensure terminal preconditions were established while original evidence
	// was still complete, so any later denial isolates the historical
	// contradictory global child rather than an unrelated status error.
	switch operation {
	case "retry":
		fallthrough
	case "terminal-turn":
		failed := root
		failed.Status = harness.InvocationFailed
		failed.UpdatedAt = now.Add(time.Second)
		failed, err = store.UpdateInvocation(t.Context(), failed, failed.Revision)
		if err != nil {
			t.Fatalf("baseline original no-effect terminal Invocation: %v", err)
		}
		root = failed
		if operation == "retry" {
			turn, turnErr := store.GetTurn(t.Context(), turnID)
			if turnErr != nil {
				t.Fatal(turnErr)
			}
			turn.Status = harness.TurnFailed
			turn.UpdatedAt = now.Add(time.Second)
			if _, err = store.UpdateTurn(t.Context(), turn, turn.Revision); err != nil {
				t.Fatalf("baseline terminal Turn before Retry: %v", err)
			}
		}
	}
	if corrupted {
		// Direct DB insert simulates an older/malformed persisted row.
		// Normal Store.AppendItem would correctly refuse its foreign Turn
		// and mismatched owner; these are historical integrity tests.
		if err = db.Exec(`INSERT INTO agent_harness_items
			(id,turn_id,seq,kind,status,run_id,invocation_id,parent_item_id,payload_json,created_at,updated_at)
			VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
			"legacy_other_turn_second_physical_receipt", "legacy-foreign-turn", 2,
			string(harness.ItemArtifact), string(harness.ItemCompleted),
			"other-run", "other-invocation", grant.ID, "{}", now, now,
		).Error; err != nil {
			t.Fatalf("insert historical conflicting physical receipt: %v", err)
		}
		if two, readErr := store.ListItemChildren(t.Context(), grant.ID, harness.ItemChildProofAll); readErr != nil || len(two) != 2 {
			t.Fatalf("global physical parent index hid historical sibling: count=%d err=%v", len(two), readErr)
		}
	}

	expectDenied := corrupted
	switch operation {
	case "retry":
		next, rotateErr := store.RetryInvocation(t.Context(), root.ID, root.Revision, "next-sql-attempt-run", now.Add(time.Minute))
		if expectDenied {
			if !errors.Is(rotateErr, harness.ErrConflict) || next.ID != "" {
				t.Fatalf("SQL Retry accepted globally contradictory original receipt: next=%+v err=%v", next, rotateErr)
			}
		} else if rotateErr != nil || next.Attempt != root.Attempt+1 {
			t.Fatalf("valid original receipt unexpectedly blocked SQL Retry: next=%+v err=%v", next, rotateErr)
		}
	case "new-physical-grant":
		_, fresh, grantErr := store.AppendItem(t.Context(), harness.Item{
			ID: "hdispatch_next_sql_parent_resume", TurnID: turnID,
			Kind: harness.ItemDiagnostic, Status: harness.ItemStarted,
			InvocationID: root.ID, RunID: root.ExecutionRefID,
			ParentItemID: harness.ExecutionDispatchParentItemID,
			Payload:      json.RawMessage(`{"phase":"resume","physicalRunID":"parent-agent-run"}`),
			CreatedAt:    now, UpdatedAt: now,
		})
		if expectDenied {
			if !errors.Is(grantErr, harness.ErrConflict) || fresh {
				t.Fatalf("SQL admitted new grant with conflicting old physical receipt: fresh=%v err=%v", fresh, grantErr)
			}
		} else if grantErr != nil || !fresh {
			t.Fatalf("SQL rejected valid second physical segment: fresh=%v err=%v", fresh, grantErr)
		}
	case "terminal-invocation":
		next := root
		next.Status = harness.InvocationFailed
		next.UpdatedAt = now.Add(time.Second)
		_, updateErr := store.UpdateInvocation(t.Context(), next, next.Revision)
		if expectDenied && !errors.Is(updateErr, harness.ErrConflict) {
			t.Fatalf("SQL terminal Invocation ignored conflicting original effect: %v", updateErr)
		}
		if !expectDenied && updateErr != nil {
			t.Fatalf("baseline SQL terminal Invocation refused valid original no-effect: %v", updateErr)
		}
	case "terminal-turn":
		turn, turnErr := store.GetTurn(t.Context(), turnID)
		if turnErr != nil {
			t.Fatal(turnErr)
		}
		turn.Status = harness.TurnFailed
		turn.UpdatedAt = now.Add(time.Second)
		_, updateErr := store.UpdateTurn(t.Context(), turn, turn.Revision)
		if expectDenied && !errors.Is(updateErr, harness.ErrConflict) {
			t.Fatalf("SQL terminal Turn ignored conflicting original effect: %v", updateErr)
		}
		if !expectDenied && updateErr != nil {
			t.Fatalf("baseline SQL terminal Turn refused valid original no-effect: %v", updateErr)
		}
	default:
		t.Fatalf("unknown physical admission operation %q", operation)
	}
}

func TestSQLGlobalOriginalDispatchAdmissionMatrix(t *testing.T) {
	for _, operation := range []string{"retry", "new-physical-grant", "terminal-invocation", "terminal-turn"} {
		t.Run(operation, func(t *testing.T) {
			for _, corruption := range []bool{false, true} {
				title := "valid-original"
				if corruption {
					title = "foreign-turn-contradiction"
				}
				t.Run(title, func(t *testing.T) {
					_, db := newStoreWithDB(t)
					pr103SQLGlobalPhysicalAdmission(t, db, operation, corruption)
				})
			}
		})
	}
}

func TestRealPostgresGlobalOriginalDispatchAdmissionMatrix(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	for _, operation := range []string{"retry", "new-physical-grant", "terminal-invocation", "terminal-turn"} {
		t.Run(operation, func(t *testing.T) {
			for _, corruption := range []bool{false, true} {
				title := "valid-original"
				if corruption {
					title = "foreign-turn-contradiction"
				}
				t.Run(title, func(t *testing.T) {
					db, _ := openIsolatedHarnessPostgres(t, dsn)
					if err := harnesspostgres.Migrate(db); err != nil {
						t.Fatal(err)
					}
					pr103SQLGlobalPhysicalAdmission(t, db, operation, corruption)
				})
			}
		})
	}
}
