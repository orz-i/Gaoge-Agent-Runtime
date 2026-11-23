package profile_test

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-sqlite/profile"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-sqlite/profile/testprofile"
)

func TestInvalidStagedDatabaseIsNeverPublished(t *testing.T) {
	path := filepath.Join(testprofile.Dir(t), "invalid.sqlite")
	initialize := func(ctx context.Context, db *sql.DB) error {
		_, err := db.ExecContext(ctx, "CREATE TABLE incomplete(id INTEGER PRIMARY KEY)")
		return err
	}
	rejected := errors.New("schema incomplete")
	_, err := profile.CreateNew(t.Context(), path, initialize, func(context.Context, *sql.DB) error { return rejected })
	if !errors.Is(err, rejected) {
		t.Fatalf("validation: %v", err)
	}
	if _, err = os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("published invalid schema: %v", err)
	}
}
