package harnesspostgres_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	harnesspostgres "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness-postgres"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/handoff"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
)

// These are persisted Store facts, not mocked return values. A Handoff can
// have a settled physical Start receipt while its ChildRunner's Run lives in
// an independent Kernel and cannot be cancelled by Harness's local Kernel.
func assertPR82RemoteCancellationOnStore(t *testing.T, store harness.Store, role bool, rootCancel bool) {
	t.Helper()
	ctx := t.Context()
	now := time.Now().UTC()
	rt, err := kernel.New(kernel.Dependencies{Store: memory.NewStore()})
	if err != nil {
		t.Fatal(err)
	}
	ag, err := agent.NewRunner(agent.Dependencies{Runtime: rt, Model: pr72RealContextModel{}})
	if err != nil {
		t.Fatal(err)
	}
	runner, err := harness.NewRunner(harness.Dependencies{Runtime: rt, Agent: ag, Store: store, Clock: pr72RealContextClock{}})
	if err != nil {
		t.Fatal(err)
	}
	turnID := "pr82-remote-cancel"
	rootRunID := "pr82-local-root-run"
	rootID := "pr82-root-invocation"
	childRunID := "pr82-remote-child-run"
	parentItemID := "pr82-delegation-parent"
	turn := harness.Turn{ID: turnID, SessionID: "pr82-session", HostTurn: harness.HostRef{Kind: "message", ID: turnID},
		ConfigSnapshotID: "pr82-config", Status: harness.TurnRunning,
		Revision: 1, CreatedAt: now, UpdatedAt: now}
	if _, _, err := store.CreateTurn(ctx, turn); err != nil {
		t.Fatal(err)
	}
	actor := kernel.ActorRef{TenantID: "tenant", ActorID: "actor"}
	thread := kernel.ThreadRef{Kind: "conversation", ID: turnID}
	if _, err := rt.Create(ctx, kernel.CreateRequest{ID: rootRunID, Kind: agent.RunKind, Actor: actor, Thread: thread,
		RequestID: "root", Goal: "delegate", State: []byte(`{}`)}); err != nil {
		t.Fatal(err)
	}
	root := harness.Invocation{ID: rootID, TurnID: turnID, CapabilityKey: harness.CapabilityAgent,
		ExecutionClass: harness.ExecutionAgent, ExecutionRefID: rootRunID, Status: harness.InvocationRunning,
		Attempt: 1, Revision: 1, OutputRefs: []harness.HostRef{}, CreatedAt: now, UpdatedAt: now}
	if _, _, err := store.CreateInvocation(ctx, root); err != nil {
		t.Fatal(err)
	}
	delegation := handoff.Delegation{ID: "pr82-delegation", MemberID: "specialist", ChildRunID: childRunID, Goal: "inspect", Status: handoff.StatusQueued}
	if role {
		delegation.RoleID = "specialist"
	}
	invocationID, err := harness.InvocationID(turnID, parentItemID, harness.CapabilityAgent, delegation.ID)
	if err != nil {
		t.Fatal(err)
	}
	input, err := json.Marshal(struct {
		handoff.Delegation
		HarnessRoot struct {
			InvocationID   string `json:"invocationID"`
			ExecutionRefID string `json:"executionRefID"`
			Attempt        int    `json:"attempt"`
		} `json:"harnessRoot"`
	}{Delegation: delegation, HarnessRoot: struct {
		InvocationID   string `json:"invocationID"`
		ExecutionRefID string `json:"executionRefID"`
		Attempt        int    `json:"attempt"`
	}{rootID, rootRunID, 1}})
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(input)
	child := harness.Invocation{ID: invocationID, TurnID: turnID, ParentItemID: parentItemID,
		CapabilityKey: harness.CapabilityAgent, DefinitionVersion: harness.RuntimeCapabilityVersion,
		ExecutionClass: harness.ExecutionAgent, ExecutionRefID: childRunID, Input: input, InputHash: hex.EncodeToString(digest[:]),
		Status: harness.InvocationAccepted, Attempt: 1, Revision: 1, OutputRefs: []harness.HostRef{},
		CreatedAt: now, UpdatedAt: now}
	if _, _, err := store.CreateInvocation(ctx, child); err != nil {
		t.Fatal(err)
	}
	grant := harness.Item{ID: "pr82-handoff-grant", TurnID: turnID, InvocationID: child.ID, RunID: childRunID,
		Kind: harness.ItemDiagnostic, Status: harness.ItemStarted, ParentItemID: harness.ExecutionDispatchParentItemID,
		Payload: json.RawMessage(`{"phase":"handoff-start","physicalRunID":"pr82-remote-child-run"}`), CreatedAt: now, UpdatedAt: now}
	if _, _, err := store.AppendItem(ctx, grant); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.AppendItem(ctx, harness.Item{ID: "pr82-handoff-verified-start", TurnID: turnID,
		Kind: harness.ItemDiagnostic, Status: harness.ItemCompleted, ParentItemID: grant.ID,
		Payload: json.RawMessage(`{"boundary":"handoff_start_returned"}`), CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	for i := range 2 {
		if rootCancel {
			_, err = runner.Cancel(ctx, turnID, "stop")
		} else {
			_, err = runner.CancelInvocation(ctx, child.ID, "stop")
		}
		// A repeated direct-child request cannot resume its still-fenced
		// cancellation because no local Kernel Run can prove it completed.
		// Root retry, in contrast, can resume from its cancelled Kernel Run.
		if i == 1 && !rootCancel {
			if !errors.Is(err, harness.ErrConflict) {
				t.Fatalf("attempt %d: repeated child cancel crossed unresolved intent: %v", i, err)
			}
		} else if !errors.Is(err, harness.ErrHandoffCancellationUnverified) {
			t.Fatalf("attempt %d: missing external cancellation proof allowed success: %v", i, err)
		}
		persisted, loadErr := store.GetInvocation(ctx, child.ID)
		if loadErr != nil || persisted.Status != child.Status || persisted.Revision != child.Revision || persisted.ExecutionRefID != child.ExecutionRefID {
			t.Fatalf("attempt %d falsely cancelled remote child: %+v %v", i, persisted, loadErr)
		}
		items, loadErr := store.ListItems(ctx, turnID, 0, 500)
		if loadErr != nil || !harness.ActiveCancellationFence(items) {
			t.Fatalf("attempt %d lost unresolved cancellation intent: %v", i, loadErr)
		}
	}
	rootSnapshot, err := rt.Load(ctx, rootRunID)
	if err != nil {
		t.Fatal(err)
	}
	if rootCancel && rootSnapshot.Run.Status != kernel.RunStatusCancelled {
		t.Fatalf("root cancellation was not applied: %s", rootSnapshot.Run.Status)
	}
	if !rootCancel && rootSnapshot.Run.Status != kernel.RunStatusRunning {
		t.Fatalf("child-only cancellation touched root: %s", rootSnapshot.Run.Status)
	}
}

func TestPR82RemoteHandoffCancellationUsesDurableProofAcrossStores(t *testing.T) {
	for name, create := range rootInvocationStores() {
		t.Run(name, func(t *testing.T) {
			for _, role := range []bool{false, true} {
				for _, rootCancel := range []bool{false, true} {
					mode := "member"
					if role {
						mode = "role"
					}
					if rootCancel {
						mode += "-turn"
					} else {
						mode += "-child"
					}
					t.Run(mode, func(t *testing.T) {
						assertPR82RemoteCancellationOnStore(t, create(t), role, rootCancel)
					})
				}
			}
		})
	}
}

func TestRealPostgresPR82RemoteHandoffCancellationRequiresReceipt(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	for _, role := range []bool{false, true} {
		for _, rootCancel := range []bool{false, true} {
			db, _ := openIsolatedHarnessPostgres(t, dsn)
			if err := harnesspostgres.Migrate(db); err != nil {
				t.Fatal(err)
			}
			store, err := harnesspostgres.New(db)
			if err != nil {
				t.Fatal(err)
			}
			assertPR82RemoteCancellationOnStore(t, store, role, rootCancel)
		}
	}
}
