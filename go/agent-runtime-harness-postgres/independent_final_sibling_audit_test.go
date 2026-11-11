package harnesspostgres

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// The SQL terminal-Handoff reader must not hide a contradictory receipt by
// filtering either its Kind or Run before the shared authority verifier.
func TestSQLIndependentFinalHandoffReaderKeepsForeignRunDelegationSibling(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:terminal-handoff-independent-sibling-%d?mode=memory&cache=shared", time.Now().UnixNano())), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)
	facts := []harness.Item{
		{ID: "hid_original-started", TurnID: "turn", InvocationID: "root", RunID: "child-run", Kind: harness.ItemDelegation, Status: harness.ItemStarted, Seq: 1, Payload: json.RawMessage(`{"origin":1}`), CreatedAt: now, UpdatedAt: now},
		{ID: "hid_original-completed", TurnID: "turn", InvocationID: "root", RunID: "child-run", Kind: harness.ItemDelegation, Status: harness.ItemCompleted, ParentItemID: "hid_original-started", Seq: 2, Payload: json.RawMessage(`{"result":1}`), CreatedAt: now, UpdatedAt: now},
		{ID: "foreign-kind-terminal-result", TurnID: "turn", InvocationID: "root", RunID: "wrong-child-run", Kind: harness.ItemDelegation, Status: harness.ItemCompleted, ParentItemID: "hid_original-started", Seq: 3, Payload: json.RawMessage(`{"result":2}`), CreatedAt: now, UpdatedAt: now},
		{ID: "ordinary-delegation-attachment", TurnID: "turn", InvocationID: "root", RunID: "child-run", Kind: harness.ItemArtifact, Status: harness.ItemCompleted, ParentItemID: "hid_original-started", Seq: 4, Payload: json.RawMessage(`{"file":"attachment"}`), CreatedAt: now, UpdatedAt: now},
	}
	for _, fact := range facts {
		record, err := itemToRecord(fact)
		if err != nil {
			t.Fatal(err)
		}
		if err = db.Create(&record).Error; err != nil {
			t.Fatal(err)
		}
	}
	owners := []harness.Invocation{{ID: "child", TurnID: "turn", ParentItemID: facts[0].ID, ExecutionRefID: "child-run", Status: harness.InvocationAccepted}}
	read, err := terminalHandoffChildEvidenceTx(db, "turn", owners)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, item := range read {
		if item.ID == facts[2].ID {
			found = true
		}
	}
	if !found {
		t.Fatal("SQL terminal projection filtered out a competing Delegation receipt with a foreign physical Run")
	}
	for _, item := range read {
		if item.ID == facts[3].ID {
			t.Fatal("non-authority Artifact was mistaken for a terminal Delegation result")
		}
	}
}
