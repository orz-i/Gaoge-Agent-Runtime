package postgres

import (
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

func TestRealPostgresRunJournalOutboxRollbackTogether(t *testing.T) {
	dsn := requireRecoveryPostgres(t)
	for _, table := range []string{"agent_kernel_events", "agent_kernel_transition_outbox"} {
		t.Run(table, func(t *testing.T) {
			db, isolatedDSN := openIsolatedRealPostgres(t, dsn)
			if err := Migrate(db); err != nil {
				t.Fatal(err)
			}
			runtime, err := kernel.New(kernel.Dependencies{Store: NewKernelStore(db)})
			if err != nil {
				t.Fatal(err)
			}
			before, err := runtime.Create(t.Context(), kernel.CreateRequest{
				ID: "atomic-transition", Kind: "test", Goal: "verify atomic rollback",
				Actor:  kernel.ActorRef{TenantID: "tenant", ActorID: "actor"},
				Thread: kernel.ThreadRef{Kind: "test", ID: "thread"}, State: json.RawMessage(`{"step":1}`),
			})
			if err != nil {
				t.Fatal(err)
			}
			// PostgreSQL rejects the actual INSERT after the aggregate update.
			// NOT VALID leaves existing rows intact while rejecting new rows.
			if err = db.Exec(fmt.Sprintf("ALTER TABLE %s ADD CONSTRAINT injected_write_failure CHECK (false) NOT VALID", table)).Error; err != nil {
				t.Fatal(err)
			}
			mutation := kernel.Mutation{
				Status: kernel.RunStatusRunning, State: json.RawMessage(`{"step":2}`),
				Events: []kernel.EventDraft{{Type: "test.wakeup", Wakeup: true}},
			}
			if _, err = runtime.Apply(t.Context(), before.Run.ID, before.Run.Revision, mutation); err == nil {
				t.Fatal("injected PostgreSQL constraint did not reject transition")
			}
			// Read through an independent connection, so uncommitted writes cannot
			// accidentally satisfy the assertions.
			restarted := NewKernelStore(reopenRecoveryPostgres(t, isolatedDSN))
			after, err := restarted.Load(t.Context(), before.Run.ID)
			if err != nil || !reflect.DeepEqual(after, before) {
				t.Fatalf("failed transition changed aggregate: before=%#v after=%#v err=%v", before, after, err)
			}
			events, err := restarted.ListEvents(t.Context(), before.Run.ID, before.EventHead, 10)
			if err != nil || len(events) != 0 {
				t.Fatalf("failed transition left journal rows: %#v, %v", events, err)
			}
			claim := kernel.TransitionClaimRequest{WorkerID: "recovery", Limit: 10, LeaseDuration: time.Minute, Now: time.Now().Add(time.Minute)}
			claims, err := restarted.ClaimTransitions(t.Context(), claim)
			if err != nil || len(claims) != 0 {
				t.Fatalf("failed transition left wakeups: %#v, %v", claims, err)
			}
			if err = db.Exec(fmt.Sprintf("ALTER TABLE %s DROP CONSTRAINT injected_write_failure", table)).Error; err != nil {
				t.Fatal(err)
			}
			committed, err := runtime.Apply(t.Context(), before.Run.ID, before.Run.Revision, mutation)
			if err != nil || committed.Run.Revision != before.Run.Revision+1 || committed.EventHead != before.EventHead+1 {
				t.Fatalf("retry after rollback = %#v, %v", committed, err)
			}
			claims, err = restarted.ClaimTransitions(t.Context(), claim)
			if err != nil || len(claims) != 1 || claims[0].Transition.Revision != committed.Run.Revision ||
				len(claims[0].Transition.Events) != 1 || !claims[0].Transition.Events[0].Wakeup {
				t.Fatalf("successful retry lost atomic wakeup: %#v, %v", claims, err)
			}
		})
	}
}
