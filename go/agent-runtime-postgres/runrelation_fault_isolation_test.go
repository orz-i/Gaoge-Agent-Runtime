package postgres

import (
	"os"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-postgres/models"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/conformance"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/runrelation"
	"gorm.io/gorm"
)

func reconciliationFaultIsolation(t *testing.T, db *gorm.DB) {
	conformance.RunReconciliationFaultIsolationSuite(t, NewKernelStore(db), NewRunRelationStore(db), func(r runrelation.Relation) error {
		return db.Create(&models.RunRelationRecord{ChildRunID: r.ChildRunID, ParentRunID: r.ParentRunID, Kind: string(r.Kind), OwnerNodeID: r.OwnerNodeID, CreatedAt: r.CreatedAt}).Error
	})
}
func TestReconciliationFaultIsolationGORM(t *testing.T) {
	reconciliationFaultIsolation(t, openKernelStoreTestDB(t))
}
func TestRealPostgresReconciliationFaultIsolation(t *testing.T) {
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN not set")
	}
	db, _ := openIsolatedRealPostgres(t, dsn)
	if e := Migrate(db); e != nil {
		t.Fatal(e)
	}
	reconciliationFaultIsolation(t, db)
}
