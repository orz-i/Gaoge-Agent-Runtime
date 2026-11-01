package harnesspostgres_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	harnesspostgres "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness-postgres"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestPR72RetryCASPreservesPendingPhysicalExecutionInMemoryAndSQL(t *testing.T) {
	for name, create := range rootInvocationStores() {
		t.Run(name, func(t *testing.T) {
			store := create(t)
			now := time.Now().UTC()
			turn := harness.Turn{
				ID: "retry_unsettled_" + name, SessionID: "session_retry_" + name,
				HostTurn:         harness.HostRef{Kind: "conversation_turn", ID: "host_retry_" + name},
				ConfigSnapshotID: "retry_config_" + name, Status: harness.TurnRunning,
				Revision: 1, CreatedAt: now, UpdatedAt: now,
			}
			if _, fresh, err := store.CreateTurn(t.Context(), turn); err != nil || !fresh {
				t.Fatalf("create turn: fresh=%t err=%v", fresh, err)
			}
			input := json.RawMessage(`{"task":"remote"}`)
			digest := sha256.Sum256(input)
			child := harness.Invocation{
				ID: "retry-unsettled-child", TurnID: turn.ID, ParentItemID: "some-parent-item",
				CapabilityKey: harness.CapabilityTeam, DefinitionVersion: harness.RuntimeCapabilityVersion,
				ExecutionClass: harness.ExecutionTeam, ExecutionRefID: "old-in-doubt-run",
				Input: input, InputHash: hex.EncodeToString(digest[:]),
				Status: harness.InvocationAccepted, Attempt: 1, Revision: 1,
				OutputRefs: []harness.HostRef{}, CreatedAt: now, UpdatedAt: now,
			}
			child, created, err := store.CreateInvocation(t.Context(), child)
			if err != nil || !created {
				t.Fatalf("create child=%t err=%v", created, err)
			}
			grant := harness.Item{
				ID: "in-doubt-physical-grant", TurnID: turn.ID, InvocationID: child.ID, RunID: child.ExecutionRefID,
				Kind: harness.ItemDiagnostic, Status: harness.ItemStarted,
				ParentItemID: harness.ExecutionDispatchParentItemID,
				Payload:      json.RawMessage(`{"phase":"start","physicalRunID":"old-in-doubt-run"}`),
				CreatedAt:    now, UpdatedAt: now,
			}
			if _, fresh, err := store.AppendItem(t.Context(), grant); err != nil || !fresh {
				t.Fatalf("claim physical grant: fresh=%t err=%v", fresh, err)
			}
			child.Status = harness.InvocationFailed // stale/concurrent failure projection
			_, err = store.UpdateInvocation(t.Context(), child, child.Revision)
			if !errors.Is(err, harness.ErrConflict) {
				t.Fatalf("failed projection bypassed pending grant: %v", err)
			}
			_, err = store.RetryInvocation(t.Context(), child.ID, child.Revision, "new-run-must-not-exist", now.Add(time.Second))
			if !errors.Is(err, harness.ErrConflict) {
				t.Fatalf("retried unknown original: %v", err)
			}
			unchanged, err := store.GetInvocation(t.Context(), child.ID)
			if err != nil || unchanged.Status != harness.InvocationAccepted || unchanged.ExecutionRefID != grant.RunID || unchanged.Attempt != 1 || unchanged.Revision != child.Revision {
				t.Fatalf("failed rotation changed persisted state: %+v err=%v", unchanged, err)
			}
			settled := harness.Item{ID: "settled-grant", TurnID: turn.ID, InvocationID: child.ID, RunID: child.ExecutionRefID,
				Kind: harness.ItemDiagnostic, Status: harness.ItemCompleted,
				ParentItemID: grant.ID, Payload: json.RawMessage(`{"boundary":"physical_dispatch_rejected"}`),
				CreatedAt: now, UpdatedAt: now}
			if _, fresh, err := store.AppendItem(t.Context(), settled); err != nil || !fresh {
				t.Fatalf("settle: %t %v", fresh, err)
			}
			// Once the no-effect completion is durable, terminal projection
			// and then explicit retry can safely rotate the identity.
			child, err = store.UpdateInvocation(t.Context(), child, child.Revision)
			if err != nil {
				t.Fatalf("settled failure projection: %v", err)
			}
			retried, err := store.RetryInvocation(t.Context(), child.ID, child.Revision, "allowed-new-run", now.Add(time.Second))
			if err != nil || retried.ExecutionRefID != "allowed-new-run" || retried.Attempt != 2 || retried.Status != harness.InvocationAccepted {
				t.Fatalf("settled grant did not admit a legal retry: %+v err=%v", retried, err)
			}
		})
	}
}

// Real PostgreSQL cross-connection check: failure projection and identity
// rotation must observe the first connection's pending physical dispatch.
func TestRealPostgresPR72PendingGrantPreventsFailureAndRetryAcrossConnections(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN required")
	}
	db, isolated := openIsolatedHarnessPostgres(t, dsn)
	if err := harnesspostgres.Migrate(db); err != nil {
		t.Fatal(err)
	}
	first, err := harnesspostgres.New(db)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(isolated)
	if err != nil {
		t.Fatal(err)
	}
	secondDB, err := gorm.Open(postgres.Open(parsed.String()), realHarnessPostgresTestConfig())
	if err != nil {
		t.Fatal(err)
	}
	conn, err := secondDB.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	second, err := harnesspostgres.New(secondDB)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	turn := harness.Turn{
		ID: "pr72-real-fence-turn", SessionID: "pr72-real-fence-session",
		HostTurn:         harness.HostRef{Kind: "conversation_turn", ID: "pr72-real-fence-host"},
		ConfigSnapshotID: "pr72-real-fence-config", Status: harness.TurnRunning,
		Revision: 1, CreatedAt: now, UpdatedAt: now,
	}
	if _, created, e := first.CreateTurn(t.Context(), turn); e != nil || !created {
		t.Fatalf("turn=%t %v", created, e)
	}
	input := json.RawMessage(`{"task":"no duplicate effects"}`)
	digest := sha256.Sum256(input)
	inv := harness.Invocation{
		ID: "pr72-real-fence-inv", TurnID: turn.ID, ParentItemID: "pr72-parent",
		CapabilityKey: harness.CapabilityTeam, DefinitionVersion: harness.RuntimeCapabilityVersion,
		ExecutionClass: harness.ExecutionTeam, Input: input, InputHash: hex.EncodeToString(digest[:]),
		ExecutionRefID: "pr72-original-effect", Status: harness.InvocationAccepted,
		Attempt: 1, Revision: 1, OutputRefs: []harness.HostRef{}, CreatedAt: now, UpdatedAt: now,
	}
	if _, created, e := first.CreateInvocation(t.Context(), inv); e != nil || !created {
		t.Fatalf("inv=%t %v", created, e)
	}
	grant := harness.Item{
		ID: "pr72-original-dispatch-grant", TurnID: turn.ID, InvocationID: inv.ID, RunID: inv.ExecutionRefID,
		Kind: harness.ItemDiagnostic, Status: harness.ItemStarted, ParentItemID: harness.ExecutionDispatchParentItemID,
		Payload: json.RawMessage(`{"phase":"start"}`), CreatedAt: now, UpdatedAt: now,
	}
	if _, created, e := first.AppendItem(t.Context(), grant); e != nil || !created {
		t.Fatalf("grant=%t %v", created, e)
	}
	failed := inv
	failed.Status = harness.InvocationFailed
	if _, e := second.UpdateInvocation(t.Context(), failed, inv.Revision); !errors.Is(e, harness.ErrConflict) {
		t.Fatalf("second process terminalized in-flight original: %v", e)
	}
	if _, e := second.RetryInvocation(t.Context(), inv.ID, inv.Revision, "pr72-new-effect", now.Add(time.Second)); !errors.Is(e, harness.ErrConflict) {
		t.Fatalf("second process rotated unfinished original: %v", e)
	}
	current, e := second.GetInvocation(t.Context(), inv.ID)
	if e != nil || current.Status != harness.InvocationAccepted || current.ExecutionRefID != inv.ExecutionRefID || current.Attempt != 1 {
		t.Fatalf("state altered across handles: %+v %v", current, e)
	}
	settled := harness.Item{ID: "pr72-original-dispatch-settled", TurnID: turn.ID,
		InvocationID: inv.ID, RunID: inv.ExecutionRefID,
		Kind: harness.ItemDiagnostic, Status: harness.ItemCompleted, ParentItemID: grant.ID,
		Payload: json.RawMessage(`{"boundary":"feature_start_no_effect"}`), CreatedAt: now, UpdatedAt: now}
	if _, created, e := first.AppendItem(t.Context(), settled); e != nil || !created {
		t.Fatalf("settle=%t %v", created, e)
	}
	failed, e = second.UpdateInvocation(t.Context(), failed, inv.Revision)
	if e != nil {
		t.Fatalf("settled no-effect cannot fail: %v", e)
	}
	retried, e := second.RetryInvocation(t.Context(), inv.ID, failed.Revision, "pr72-verified-new-effect", now.Add(time.Second))
	if e != nil || retried.ExecutionRefID != "pr72-verified-new-effect" || retried.Attempt != 2 {
		t.Fatalf("verified no-effect retry: %+v %v", retried, e)
	}
}
