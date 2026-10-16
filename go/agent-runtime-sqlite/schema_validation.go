package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// The first published Schema v1 is still in development. Validate its
// complete current DDL: a development file with the same user_version but
// an obsolete table/index shape must fail closed, never be reinterpreted.
// Once released, incompatible persisted formats require explicit migrations.
func (s *Store) validateSchema(ctx context.Context) error {
	for _, ddl := range schemaDDL {
		parts := strings.Fields(ddl)
		if len(parts) < 3 {
			return fmt.Errorf("%w: malformed expected DDL", ErrUnsupportedSchema)
		}
		kind, name := strings.ToLower(parts[1]), parts[2]
		var actual string
		err := s.db.QueryRowContext(ctx, "SELECT sql FROM sqlite_master WHERE type=? AND name=?", kind, name).Scan(&actual)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w: missing %s %s", ErrUnsupportedSchema, kind, name)
		}
		if err != nil {
			return err
		}
		// SQLite returns the defining SQL including embedded whitespace.
		// Normalizing spacing/case tolerates benign cosmetic storage changes.
		normal := func(v string) string { return strings.ToLower(strings.Join(strings.Fields(v), " ")) }
		if normal(actual) != normal(ddl) {
			return fmt.Errorf("%w: unexpected %s definition for %s", ErrUnsupportedSchema, kind, name)
		}
	}
	return nil
}
