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
	// The SQLite runtime owns the whole private database, not merely its
	// expected tables. Unknown persistent objects (notably triggers) can
	// change a successful INSERT into a missing or altered durable record.
	type schemaObject struct{ kind, name string }
	expected := make(map[schemaObject]struct{}, len(schemaDDL))
	for _, ddl := range schemaDDL {
		parts := strings.Fields(ddl)
		if len(parts) < 3 {
			return fmt.Errorf("%w: malformed expected DDL", ErrUnsupportedSchema)
		}
		kind, name := strings.ToLower(parts[1]), parts[2]
		expected[schemaObject{kind, name}] = struct{}{}
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
	rows, err := s.db.QueryContext(ctx, "SELECT type,name FROM sqlite_master WHERE type IN ('table','index','view','trigger')")
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var kind, name string
		if err := rows.Scan(&kind, &name); err != nil {
			return err
		}
		// SQLite's implicit UNIQUE/PRIMARY KEY indexes use the reserved
		// sqlite_ prefix. Do not use LIKE 'sqlite_%': '_' is a wildcard.
		if strings.HasPrefix(strings.ToLower(name), "sqlite_") {
			continue
		}
		if _, ok := expected[schemaObject{kind, name}]; !ok {
			return fmt.Errorf("%w: unexpected %s %s", ErrUnsupportedSchema, kind, name)
		}
	}
	return rows.Err()
}
