package postgres

import (
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-postgres/models"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/conformance"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"gorm.io/gorm"
	"os"
	"testing"
)

func verifyStateHistory(t *testing.T, db *gorm.DB, reopen func() kernel.Store) {
	conformance.RunStateHistorySuite(t, NewKernelStore(db), reopen, func() (int, error) {
		var n int64
		e := db.Model(&models.KernelStateHistoryRecord{}).Count(&n).Error
		return int(n), e
	}, func(insert bool) error {
		q := "DELETE FROM agent_kernel_state_history WHERE run_id='state-history' AND stream=1 AND generation=1 AND ordinal=1"
		if insert {
			q = "INSERT INTO agent_kernel_state_history(run_id,stream,generation,ordinal,payload_json) VALUES('state-history',1,1,1,'{}')"
		}
		return db.Exec(q).Error
	}, func() error {
		return db.Exec("DELETE FROM agent_kernel_state_history WHERE stream=1 AND ordinal=0").Error
	})
}
func TestStateHistoryGORM(t *testing.T) {
	db := openKernelStoreTestDB(t)
	verifyStateHistory(t, db, func() kernel.Store { return NewKernelStore(db) })
}
func TestRealPostgresStateHistory(t *testing.T) {
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN not configured")
	}
	db, isolated := openIsolatedRealPostgres(t, dsn)
	if e := Migrate(db); e != nil {
		t.Fatal(e)
	}
	verifyStateHistory(t, db, func() kernel.Store { return NewKernelStore(reopenRecoveryPostgres(t, isolated)) })
}
