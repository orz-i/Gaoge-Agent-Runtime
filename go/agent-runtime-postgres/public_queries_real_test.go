package postgres

import (
	"os"
	"testing"

	"gorm.io/gorm"
)

func openPublicKernelDB(t *testing.T) *gorm.DB {
	t.Helper()
	if dsn := os.Getenv("TEST_POSTGRES_DSN"); dsn != "" {
		db, _ := openIsolatedRealPostgres(t, dsn)
		if err := Migrate(db); err != nil {
			t.Fatal(err)
		}
		return db
	}
	return openKernelStoreTestDB(t)
}
