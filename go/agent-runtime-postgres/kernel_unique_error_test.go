package postgres

import (
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"
)

func TestUniqueConflictClassificationUsesPostgresSQLSTATE(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		unique bool
	}{
		{"localized unique violation", &pgconn.PgError{Code: "23505", Message: "重复键值违反唯一约束"}, true},
		{"wrapped localized unique violation", fmt.Errorf("commit transaction: %w", &pgconn.PgError{Code: "23505", Message: "значение ключа нарушает ограничение уникальности"}), true},
		{"english nonunique violation", &pgconn.PgError{Code: "23503", Message: "duplicate key is referenced by another record"}, false},
		{"localized nonunique violation", &pgconn.PgError{Code: "23514", Message: "唯一约束不是这个检查失败的原因"}, false},
		{"GORM translated conflict", gorm.ErrDuplicatedKey, true},
		{"legacy sqlite unique violation", errors.New("UNIQUE constraint failed: id"), true},
		{"generic timeout", errors.New("network timeout"), false},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if got := isKernelUniqueConstraint(test.err); got != test.unique {
				t.Fatalf("SQLSTATE classification: got=%v, expected=%v", got, test.unique)
			}
		})
	}
}
