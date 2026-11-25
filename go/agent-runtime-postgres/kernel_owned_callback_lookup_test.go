package postgres

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"gorm.io/gorm"
)

func verifyOwnedCallbackLookupMissingRun(t *testing.T, db *gorm.DB) {
	t.Helper()
	// PostgreSQL physical grant schema does not have a Run FK. Simulate
	// an inconsistent persisted self-scoped row, not a public API admission.
	g := kernel.ChildExecutionGrant{ID: "orphan-grant", ChildRunID: "missing-root", ExpectedRevision: 1,
		ExecutionOwnerID: "owner", ExecutionLeaseID: "lease", ExecutionEpoch: 1, AdmissionSourceHash: strings.Repeat("a", 64), AdmissionOperation: "start"}
	row := pgPhysicalRecord(g, g.ChildRunID)
	row.CallbackReturnRevision = 1
	row.CallbackReturnSourceHash = strings.Repeat("b", 64)
	row.CallbackHostCompleted = true
	if e := db.Create(&row).Error; e != nil {
		t.Fatal(e)
	}
	rt, e := kernel.New(kernel.Dependencies{Store: NewKernelStore(db)})
	if e != nil {
		t.Fatal(e)
	}
	// Query both the native port and Runtime: a matching orphan is a
	// conflict, a missing identity is absent, and neither can succeed.
	for _, lookup := range []func(context.Context, kernel.ChildExecutionGrant) (kernel.OwnedPhysicalCallbackCompletion, error){
		NewKernelStore(db).LookupOwnedPhysicalCallbackReturn, rt.LookupOwnedPhysicalCallbackReturn,
	} {
		got, e := lookup(t.Context(), g)
		if !errors.Is(e, kernel.ErrExecutionConflict) || got != (kernel.OwnedPhysicalCallbackCompletion{}) {
			t.Fatalf("matching orphan did not report conflict: %+v %v", got, e)
		}
		foreign := g
		foreign.ID = "absent-grant"
		foreign.ChildRunID = "absent-run"
		got, e = lookup(t.Context(), foreign)
		if !errors.Is(e, kernel.ErrNotFound) || got != (kernel.OwnedPhysicalCallbackCompletion{}) {
			t.Fatalf("absent identity did not report absence: %+v %v", got, e)
		}
	}
}
func TestOwnedCallbackLookupMissingRunGORM(t *testing.T) {
	verifyOwnedCallbackLookupMissingRun(t, openKernelStoreTestDB(t))
}
func TestRealPostgresOwnedCallbackLookupMissingRun(t *testing.T) {
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("real postgres not configured")
	}
	db, _ := openIsolatedRealPostgres(t, dsn)
	if e := Migrate(db); e != nil {
		t.Fatal(e)
	}
	verifyOwnedCallbackLookupMissingRun(t, db)
}
