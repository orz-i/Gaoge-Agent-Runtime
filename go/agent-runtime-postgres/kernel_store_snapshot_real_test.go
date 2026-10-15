package postgres

import (
	"os"
	"strings"
	"testing"
)

func TestRealPostgresKernelStoreApplyReturnsOwnCommittedSnapshot(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	db, _ := openIsolatedRealPostgres(t, dsn)
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	testKernelStoreApplyOwnSnapshot(t, db)
}

func TestRealPostgresKernelStoreCreateReturnsOwnCommittedSnapshot(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	db, _ := openIsolatedRealPostgres(t, dsn)
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	testKernelStoreCreateOwnSnapshot(t, db)
}
