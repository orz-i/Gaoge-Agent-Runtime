package postgres

import (
	"os"
	"strings"
	"testing"
)

func TestRealPostgresOwnedPhysicalBindingGuards(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN not configured")
	}
	db, _ := openIsolatedRealPostgres(t, dsn)
	if e := Migrate(db); e != nil {
		t.Fatal(e)
	}
	runOwnedPhysicalBindingGuards(t, db)
}
