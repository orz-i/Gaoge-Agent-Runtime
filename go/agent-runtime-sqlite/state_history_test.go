package sqlite

import (
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/conformance"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"testing"
)

func TestStateHistoryNativeSQLite(t *testing.T) {
	path := ownedPrivatePath(t, "history.sqlite")
	first, e := CreateNew(t.Context(), path)
	if e != nil {
		t.Fatal(e)
	}
	defer first.Close()
	reopen := func() kernel.Store {
		s, e := OpenExisting(t.Context(), path)
		if e != nil {
			t.Fatal(e)
		}
		t.Cleanup(func() { _ = s.Close() })
		return s
	}
	conformance.RunStateHistorySuite(t, first, reopen, func() (int, error) {
		var n int
		e := first.db.QueryRow("SELECT count(*) FROM runtime_state_history").Scan(&n)
		return n, e
	}, func(insert bool) error {
		q := "DELETE FROM runtime_state_history WHERE run_id='state-history' AND stream=1 AND generation=1 AND ordinal=1"
		if insert {
			q = "INSERT INTO runtime_state_history VALUES('state-history',1,1,1,'{}')"
		}
		_, e := first.db.Exec(q)
		return e
	}, func() error {
		_, e := first.db.Exec("DELETE FROM runtime_state_history WHERE stream=1 AND ordinal=0")
		return e
	})
}
