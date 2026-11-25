package sqlite

import (
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/conformance"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/runrelation"
)

func TestSQLiteReconciliationFaultIsolation(t *testing.T) {
	s, e := CreateNew(t.Context(), ownedPrivatePath(t, "legacy-relation.sqlite"))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = s.Close() })
	conformance.RunReconciliationFaultIsolationSuite(t, s, NewRunRelationStore(s), func(r runrelation.Relation) error {
		_, err := s.db.ExecContext(t.Context(), `INSERT INTO runtime_run_relations(child_run_id,parent_run_id,kind,owner_node_id,created_ns) VALUES(?,?,?,?,?)`, r.ChildRunID, r.ParentRunID, string(r.Kind), r.OwnerNodeID, r.CreatedAt.UnixNano())
		return err
	})
}
