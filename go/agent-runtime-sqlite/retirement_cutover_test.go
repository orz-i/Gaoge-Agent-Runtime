package sqlite

import (
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

func TestRetirementCutoverRejectsUnfinishedHistory(t *testing.T) {
	for _, upgrade := range []bool{false, true} {
		t.Run(map[bool]string{false: "reopen", true: "v10-upgrade"}[upgrade], func(t *testing.T) {
			ctx := t.Context()
			path := ownedPrivatePath(t, "unfinished.sqlite")
			s, e := CreateNew(ctx, path)
			if e != nil {
				t.Fatal(e)
			}
			rt, e := kernel.New(kernel.Dependencies{Store: s})
			if e != nil {
				t.Fatal(e)
			}
			v, e := rt.Create(ctx, kernel.CreateRequest{ID: "root", Kind: "agent", Actor: kernel.ActorRef{TenantID: "tenant", ActorID: "actor"}, Thread: kernel.ThreadRef{Kind: "conversation", ID: "thread"}, Goal: "original", State: []byte("{}"), ExecutionRequired: true})
			if e != nil {
				t.Fatal(e)
			}
			_, e = s.db.Exec("INSERT INTO runtime_owned_retirements(grant_id,run_id,settled_revision,version,phase,payload_json) VALUES(?,?,?,?,?,?)", "old", v.Run.ID, v.Run.Revision, 1, "RETIRED", "{}")
			if e != nil {
				t.Fatal(e)
			}
			if upgrade {
				if _, e = s.db.Exec("DROP TABLE agent_harness_budgets; DROP TABLE runtime_run_identities; DROP INDEX runtime_managed_history_run_revision; " + managedSchemaDDL[2]); e != nil {
					t.Fatal(e)
				}
				if _, e = s.db.Exec("PRAGMA user_version=10"); e != nil {
					t.Fatal(e)
				}
			}
			if e = s.Close(); e != nil {
				t.Fatal(e)
			}
			if upgrade {
				e = MigrateV10ToV11(ctx, path)
			} else {
				var opened *Store
				opened, e = OpenExisting(ctx, path)
				if opened != nil {
					_ = opened.Close()
				}
			}
			if !errors.Is(e, ErrUnsupportedSchema) || !strings.Contains(e.Error(), "unfinished original retirements") {
				t.Fatalf("legacy pending history was admitted: %v", e)
			}
			raw, e := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?mode=ro")
			if e != nil {
				t.Fatal(e)
			}
			defer func() { _ = raw.Close() }()
			var phase, payload string
			if e = raw.QueryRow("SELECT phase,payload_json FROM runtime_owned_retirements WHERE grant_id='old'").Scan(&phase, &payload); e != nil || phase != "RETIRED" || payload != "{}" {
				t.Fatalf("legacy row was altered: %v %s %s", e, phase, payload)
			}
			var version int
			if e = raw.QueryRow("PRAGMA user_version").Scan(&version); e != nil || version != map[bool]int{true: 10, false: 12}[upgrade] {
				t.Fatalf("schema was changed: %v %d", e, version)
			}

		})
	}
}
