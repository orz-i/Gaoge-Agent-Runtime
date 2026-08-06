package postgres

import (
	"path/filepath"
	"testing"

	"github.com/orz-i/Gaoge/sdk/go/agent-runtime-postgres/models"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/conformance"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/kernel"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestKernelStoreConformance(t *testing.T) {
	t.Parallel()
	conformance.RunKernelStoreSuite(t, func(tb testing.TB) kernel.Store {
		tb.Helper()
		db := openKernelStoreTestDB(tb)
		return NewKernelStore(db)
	})
}

func openKernelStoreTestDB(tb testing.TB) *gorm.DB {
	tb.Helper()
	dsn := "file:" + filepath.ToSlash(filepath.Join(tb.TempDir(), "kernel.db")) +
		"?_busy_timeout=5000&_journal_mode=WAL"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		tb.Fatalf("open kernel store test db: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		tb.Fatalf("resolve kernel store sql db: %v", err)
	}
	tb.Cleanup(func() { _ = sqlDB.Close() })
	if err = db.AutoMigrate(&models.KernelRunRecord{}, &models.KernelEventRecord{}); err != nil {
		tb.Fatalf("migrate kernel store test db: %v", err)
	}
	return db
}
