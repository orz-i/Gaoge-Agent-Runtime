// Package sqlledger persists the shared budget CAS in a caller-selected SQL
// connection or transaction. It owns neither execution nor transaction lifetime.
package sqlledger

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
)

// Keep the existing PostgreSQL physical table so adopting a shared transaction
// does not duplicate or silently abandon historical reservations and receipts.
const Table = "agent_harness_budgets"
const SQLiteDDL = "CREATE TABLE agent_harness_budgets (id TEXT PRIMARY KEY, revision INTEGER NOT NULL CHECK(revision > 0), payload_json TEXT NOT NULL)"

type Record struct {
	ID          string `gorm:"primaryKey;size:64"`
	Revision    uint64 `gorm:"not null"`
	PayloadJSON string `gorm:"type:text;not null"`
}

func (Record) TableName() string { return Table }

type Connection interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

type Store struct {
	DB Connection
	// Lock is set only inside a PostgreSQL transaction, never for public reads.
	Lock bool
}

var _ budget.LedgerStore = Store{}

func (s Store) LoadBudget(ctx context.Context, id string) (budget.Ledger, error) {
	var l budget.Ledger
	var rev uint64
	var raw string
	q := "SELECT revision,payload_json FROM " + Table + " WHERE id=$1"
	if s.Lock {
		q += " FOR UPDATE"
	}
	err := s.DB.QueryRowContext(ctx, q, id).Scan(&rev, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return l, budget.ErrLedgerNotFound
	}
	if err != nil {
		return l, err
	}
	if err = json.Unmarshal([]byte(raw), &l); err != nil {
		return budget.Ledger{}, err
	}
	if l.ID != id || l.Revision != rev || rev == 0 {
		return budget.Ledger{}, budget.ErrLedgerConflict
	}
	return l, nil
}
func (s Store) SaveBudget(ctx context.Context, l budget.Ledger, rev uint64) error {
	if err := budget.ValidateLedgerWrite(l, rev); err != nil {
		return err
	}
	raw, err := json.Marshal(l)
	if err != nil {
		return err
	}
	var result sql.Result
	if rev == 0 {
		result, err = s.DB.ExecContext(ctx, "INSERT INTO "+Table+" (id,revision,payload_json) VALUES ($1,$2,$3) ON CONFLICT (id) DO NOTHING", l.ID, l.Revision, string(raw))
	} else {
		result, err = s.DB.ExecContext(ctx, "UPDATE "+Table+" SET revision=$1,payload_json=$2 WHERE id=$3 AND revision=$4", l.Revision, string(raw), l.ID, rev)
	}
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return budget.ErrLedgerConflict
	}
	return nil
}

// Apply participates in the existing caller's transaction. No external
// callback is invoked, and a failed Run CAS rolls this write back.
func (s Store) Apply(ctx context.Context, runID string, m *budget.LedgerMutation) error {
	if m == nil {
		return nil
	}
	current, err := s.LoadBudget(ctx, m.ScopeID())
	if err != nil {
		return err
	}
	if err = m.Validate(current, runID); err != nil {
		return err
	}
	return s.SaveBudget(ctx, m.Next(), m.ExpectedRevision())
}
