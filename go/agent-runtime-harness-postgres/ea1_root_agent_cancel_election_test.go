package harnesspostgres_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	harnesspostgres "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness-postgres"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// These are Host sub-vectors of AR-C, not a claim that a Memory Kernel
// survives restart. The PostgreSQL case closes the old Host pool and restores
// all cancellation/Invocation/budget authority from a newly opened pool.
type ea1CancelControlStore struct {
	*memory.Store
	calls atomic.Int32
}

func (s *ea1CancelControlStore) ApplyControl(ctx context.Context, id string, rev uint64, g kernel.ControlGuard, m kernel.StoreMutation) (kernel.Snapshot, bool, error) {
	s.calls.Add(1)
	return s.Store.ApplyControl(ctx, id, rev, g, m)
}

type ea1CancelHostCut struct {
	harness.Store
	phase   string
	once    sync.Once
	entered chan struct{}
	release chan struct{}
}

func (s *ea1CancelHostCut) AppendItem(ctx context.Context, item harness.Item) (harness.Item, bool, error) {
	intent := item.ParentItemID == harness.CancellationFenceParentItemID
	receipt := item.Status == harness.ItemCompleted && strings.HasPrefix(item.ParentItemID, "hcancel_")
	if s.phase == "receipt-before" && receipt {
		s.once.Do(func() { close(s.entered); <-s.release })
	}
	saved, fresh, err := s.Store.AppendItem(ctx, item)
	if err != nil {
		return saved, fresh, err
	}
	if s.phase == "election-after" && intent && fresh {
		s.once.Do(func() { close(s.entered); <-s.release })
	}
	if s.phase == "receipt-ack-loss" && receipt && fresh {
		return harness.Item{}, false, context.DeadlineExceeded
	}
	return saved, fresh, nil
}

type ea1CancelClock struct{}

func (ea1CancelClock) Now() time.Time { return time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC) }

type ea1CancelForbiddenModel struct{ calls atomic.Int32 }

func (m *ea1CancelForbiddenModel) Generate(context.Context, model.Request) (model.Response, error) {
	m.calls.Add(1)
	return model.Response{}, errors.New("cancellation must not Generate")
}

type ea1CancelHostFixture struct {
	peer        harness.Store
	peerLedgers budget.LedgerStore
	host        harness.Store
	ledgers     budget.LedgerStore
	reopen      func() (harness.Store, budget.LedgerStore)
}

func ea1CancelHostBackend(t *testing.T, persistent bool) ea1CancelHostFixture {
	t.Helper()
	if !persistent {
		h, l := harness.NewMemoryStore(), budget.NewMemoryLedgerStore()
		return ea1CancelHostFixture{h, l, h, l, func() (harness.Store, budget.LedgerStore) { return h, l }}
	}
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	db, isolated := openIsolatedHarnessPostgres(t, dsn)
	if err := harnesspostgres.Migrate(db); err != nil {
		t.Fatal(err)
	}
	h, err := harnesspostgres.New(db)
	if err != nil {
		t.Fatal(err)
	}
	peerDB, err := gorm.Open(postgres.Open(isolated), realHarnessPostgresTestConfig())
	if err != nil {
		t.Fatal(err)
	}
	peerPool, err := peerDB.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = peerPool.Close() })
	peer, err := harnesspostgres.New(peerDB)
	if err != nil {
		t.Fatal(err)
	}
	return ea1CancelHostFixture{peer, peer, h, h, func() (harness.Store, budget.LedgerStore) {
		old, err := db.DB()
		if err != nil {
			t.Fatal(err)
		}
		if err = old.Close(); err != nil {
			t.Fatal(err)
		}
		if err = peerPool.Close(); err != nil {
			t.Fatal(err)
		}
		freshDB, err := gorm.Open(postgres.Open(isolated), realHarnessPostgresTestConfig())
		if err != nil {
			t.Fatal(err)
		}
		pool, err := freshDB.DB()
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = pool.Close() })
		fresh, err := harnesspostgres.New(freshDB)
		if err != nil {
			t.Fatal(err)
		}
		return fresh, fresh
	}}
}

func TestEA1RootAgentCancelHostElection(t *testing.T) {
	for _, phase := range []string{"election-after", "receipt-before", "receipt-ack-loss"} {
		t.Run(phase, func(t *testing.T) { ea1RunCancelHostCut(t, false, phase) })
	}
}
func TestRealPostgresEA1RootAgentCancelHostElection(t *testing.T) {
	for _, phase := range []string{"election-after", "receipt-before", "receipt-ack-loss"} {
		t.Run(phase, func(t *testing.T) { ea1RunCancelHostCut(t, true, phase) })
	}
}
func ea1RunCancelHostCut(t *testing.T, persistent bool, phase string) {
	t.Helper()
	f := ea1CancelHostBackend(t, persistent)
	physical := &ea1CancelControlStore{Store: memory.NewStore()}
	rt, err := kernel.New(kernel.Dependencies{Store: physical})
	if err != nil {
		t.Fatal(err)
	}
	// Seed exactly the frozen authority of an already admitted root, as the
	// existing native cancellation suite does. Only cancellation uses a live entry.
	now := ea1CancelClock{}.Now()
	actor := kernel.ActorRef{TenantID: "ea1-tenant", ActorID: "ea1-actor"}
	hostThread := harness.HostRef{Kind: "conversation", ID: "ea1-cancel-thread"}
	hostTurn := harness.HostRef{Kind: "conversation_turn", ID: "ea1-cancel-turn"}
	sid, err := harness.SessionID(hostThread)
	if err != nil {
		t.Fatal(err)
	}
	tid, err := harness.TurnID(sid, hostTurn)
	if err != nil {
		t.Fatal(err)
	}
	iid, err := harness.InvocationID(tid, "", harness.CapabilityAgent, "parent")
	if err != nil {
		t.Fatal(err)
	}
	rid := "ea1-cancel-root"
	thread := kernel.ThreadRef{Kind: hostThread.Kind, ID: hostThread.ID}
	limits := budget.Limits{MaxTotalTokens: 400, MaxLLMCalls: 8}
	config, err := harness.SealConfigSnapshot(tid, harness.ConfigSnapshot{AdmissionHash: strings.Repeat("a", 64), SharedBudget: &limits}, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = f.host.CreateSession(t.Context(), harness.Session{ID: sid, HostThread: hostThread, Actor: actor, Revision: 1, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if _, _, err = f.host.PutConfigSnapshot(t.Context(), config); err != nil {
		t.Fatal(err)
	}
	if _, _, err = f.host.CreateTurn(t.Context(), harness.Turn{ID: tid, SessionID: sid, HostTurn: hostTurn, ConfigSnapshotID: config.ID, Status: harness.TurnRunning, Revision: 1, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	initial, err := rt.Create(t.Context(), kernel.CreateRequest{ID: rid, Kind: agent.RunKind, Actor: actor, Thread: thread, RequestID: "parent", Goal: "coordinate", State: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	input, err := json.Marshal(struct {
		Goal   string           `json:"goal"`
		Actor  kernel.ActorRef  `json:"actor"`
		Thread kernel.ThreadRef `json:"thread"`
	}{"coordinate", actor, thread})
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(input)
	owner := harness.Invocation{ID: iid, TurnID: tid, CapabilityKey: harness.CapabilityAgent, DefinitionVersion: harness.RuntimeCapabilityVersion, ExecutionClass: harness.ExecutionAgent, Input: input, InputHash: fmt.Sprintf("%x", hash), ExecutionRefID: rid, Status: harness.InvocationRunning, Attempt: 1, Revision: 1, OutputRefs: []harness.HostRef{}, CreatedAt: now, UpdatedAt: now}
	if _, _, err = f.host.CreateInvocation(t.Context(), owner); err != nil {
		t.Fatal(err)
	}
	if _, _, err = f.host.AppendItem(t.Context(), harness.Item{ID: "ea1-root-invocation-item", TurnID: tid, Kind: harness.ItemInvocation, Status: harness.ItemStarted, RunID: rid, InvocationID: iid, Payload: json.RawMessage(`{"executionClass":"agent"}`), CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	c := budget.Coordinator{Store: f.ledgers}
	if _, err = c.Ensure(t.Context(), tid, limits); err != nil {
		t.Fatal(err)
	}
	if _, err = c.RegisterRun(t.Context(), tid, rid, budget.RunBudget{}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"unsent", "dispatched"} {
		if _, err = c.Reserve(t.Context(), tid, budget.Reservation{ID: id, RunID: rid, RequestHash: "original-" + id, Requested: budget.Usage{LLMCalls: 1, InputTokens: 3, OutputTokens: 2, TotalTokens: 5}}, true); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = c.Dispatch(t.Context(), tid, "dispatched"); err != nil {
		t.Fatal(err)
	}
	before, err := f.ledgers.LoadBudget(t.Context(), tid)
	if err != nil {
		t.Fatal(err)
	}
	provider := &ea1CancelForbiddenModel{}
	makeRunner := func(h harness.Store, l budget.LedgerStore) *harness.Runner {
		ag, e := agent.NewRunner(agent.Dependencies{Runtime: rt, Model: provider})
		if e != nil {
			t.Fatal(e)
		}
		b, e := harness.NewBudgetMiddleware(harness.BudgetMiddlewareDependencies{Store: h, Ledgers: l, Clock: ea1CancelClock{}})
		if e != nil {
			t.Fatal(e)
		}
		r, e := harness.NewRunner(harness.Dependencies{Runtime: rt, Agent: ag, Store: h, Clock: ea1CancelClock{}, Budget: b})
		if e != nil {
			t.Fatal(e)
		}
		return r
	}
	cut := &ea1CancelHostCut{Store: f.host, phase: phase, entered: make(chan struct{}), release: make(chan struct{})}
	first := makeRunner(cut, f.ledgers)
	done := make(chan error, 1)
	var release sync.Once
	unblock := func() { release.Do(func() { close(cut.release) }) }
	t.Cleanup(unblock)
	go func() { _, e := first.Cancel(context.Background(), tid, "original protective stop"); done <- e }()
	if phase != "receipt-ack-loss" {
		select {
		case <-cut.entered:
		case <-time.After(10 * time.Second):
			t.Fatal("actual cancellation did not reach cutpoint")
		}
		items, e := f.host.ListItems(t.Context(), tid, 0, 500)
		if e != nil {
			t.Fatal(e)
		}
		intent := ea1OnlyCancelIntent(t, items, tid, rid)
		if !harness.ActiveCancellationFence(items) {
			t.Fatal("unsettled original election missing")
		}
		snap, e := rt.Load(t.Context(), rid)
		if e != nil {
			t.Fatal(e)
		}
		ledger, e := f.ledgers.LoadBudget(t.Context(), tid)
		if e != nil {
			t.Fatal(e)
		}
		if phase == "election-after" {
			if !reflect.DeepEqual(snap, initial) || !reflect.DeepEqual(ledger, before) || physical.calls.Load() != 0 {
				t.Fatal("post-election/pre-cancel cut mutated original Kernel or full budget")
			}
			original, e := f.host.GetInvocation(t.Context(), iid)
			if e != nil || !reflect.DeepEqual(original, owner) {
				t.Fatalf("original Invocation changed before physical cancel: %v", e)
			}
			_, e = makeRunner(f.peer, f.peerLedgers).Cancel(t.Context(), tid, "competing cancellation")
			if !errors.Is(e, harness.ErrConflict) {
				t.Fatalf("competing uncommitted-root cancellation=%v", e)
			}
			after, e := f.host.ListItems(t.Context(), tid, 0, 500)
			if e != nil {
				t.Fatal(e)
			}
			if !reflect.DeepEqual(intent, ea1OnlyCancelIntent(t, after, tid, rid)) {
				t.Fatal("loser replaced original election")
			}
			again, e := rt.Load(t.Context(), rid)
			if e != nil || !reflect.DeepEqual(again, initial) || physical.calls.Load() != 0 {
				t.Fatal("loser mutated native owner")
			}
			againBudget, e := f.ledgers.LoadBudget(t.Context(), tid)
			if e != nil || !reflect.DeepEqual(againBudget, before) {
				t.Fatal("loser changed full shared budget")
			}
		} else {
			if snap.Run.Status != kernel.RunStatusCancelled || physical.calls.Load() != 1 {
				t.Fatal("receipt barrier was not after actual Kernel commit")
			}
			ea1AssertCancelledBudget(t, before, ledger)
			if _, e = makeRunner(f.peer, f.peerLedgers).Refresh(t.Context(), tid); !errors.Is(e, harness.ErrConflict) {
				t.Fatalf("Refresh bypassed original pending receipt: %v", e)
			}
		}
		unblock()
	}
	select {
	case err = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("cancellation did not return")
	}
	if phase == "receipt-ack-loss" {
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("expected committed receipt ACK loss: %v", err)
		}
	} else if err != nil {
		t.Fatal(err)
	}
	items, err := f.host.ListItems(t.Context(), tid, 0, 500)
	if err != nil {
		t.Fatal(err)
	}
	originalIntent := ea1OnlyCancelIntent(t, items, tid, rid)
	receipts := ea1CancelReceipts(items, originalIntent.ID)
	if !harness.VerifiedCancellationFenceReceipt(originalIntent, receipts) || harness.ActiveCancellationFence(items) {
		t.Fatal("actual original completion is not verified")
	}
	terminal, err := rt.Load(t.Context(), rid)
	if err != nil || terminal.Run.Status != kernel.RunStatusCancelled || physical.calls.Load() != 1 {
		t.Fatalf("physical cancellation count/outcome: %v", err)
	}
	terminalOwner, err := f.host.GetInvocation(t.Context(), iid)
	if err != nil {
		t.Fatal(err)
	}
	if terminalOwner.TurnID != owner.TurnID || terminalOwner.ParentItemID != owner.ParentItemID || terminalOwner.CapabilityKey != owner.CapabilityKey || terminalOwner.DefinitionVersion != owner.DefinitionVersion || terminalOwner.ID != owner.ID || terminalOwner.ExecutionRefID != owner.ExecutionRefID || terminalOwner.InputHash != owner.InputHash || !reflect.DeepEqual(terminalOwner.Input, owner.Input) || terminalOwner.Attempt != owner.Attempt || terminalOwner.ExecutionClass != owner.ExecutionClass || terminalOwner.Status != harness.InvocationCancelled {
		t.Fatal("terminal projection lost original Invocation source/route")
	}
	ledger, err := f.ledgers.LoadBudget(t.Context(), tid)
	if err != nil {
		t.Fatal(err)
	}
	ea1AssertCancelledBudget(t, before, ledger)
	// Only this phase claims Host durable reopen. Memory uses a fresh Runner
	// on the same Store and is deliberately not described as persistent restart.
	if phase == "receipt-ack-loss" {
		h, l := f.reopen()
		recovered, e := h.ListItems(t.Context(), tid, 0, 500)
		if e != nil || !reflect.DeepEqual(items, recovered) {
			t.Fatalf("reopen changed durable timeline: %v", e)
		}
		recoveredOwner, e := h.GetInvocation(t.Context(), iid)
		if e != nil || !reflect.DeepEqual(terminalOwner, recoveredOwner) {
			t.Fatalf("reopen lost frozen owner: %v", e)
		}
		recoveredBudget, e := l.LoadBudget(t.Context(), tid)
		if e != nil || !reflect.DeepEqual(ledger, recoveredBudget) {
			t.Fatalf("reopen changed full ledger: %v", e)
		}
		if _, e = makeRunner(h, l).Cancel(t.Context(), tid, "reconcile after receipt ACK loss"); e != nil {
			t.Fatal(e)
		}
		final, e := rt.Load(t.Context(), rid)
		if e != nil || !reflect.DeepEqual(terminal, final) || physical.calls.Load() != 1 {
			t.Fatal("receipt recovery repeated native cancellation or changed epoch/revision")
		}
		replayedBudget, e := l.LoadBudget(t.Context(), tid)
		if e != nil || !reflect.DeepEqual(ledger, replayedBudget) {
			t.Fatal("receipt recovery changed full ledger")
		}
		finalItems, e := h.ListItems(t.Context(), tid, 0, 500)
		if e != nil {
			t.Fatal(e)
		}
		saved, e := h.GetItem(t.Context(), originalIntent.ID)
		if e != nil || !reflect.DeepEqual(saved, originalIntent) || !reflect.DeepEqual(ea1CancelReceipts(finalItems, originalIntent.ID), receipts) {
			t.Fatal("recovery replaced original election/receipt")
		}
	}
	if provider.calls.Load() != 0 {
		t.Fatal("cancellation or recovery replayed Model")
	}
}
func ea1OnlyCancelIntent(t *testing.T, items []harness.Item, tid, rid string) harness.Item {
	t.Helper()
	var result harness.Item
	count := 0
	for _, v := range items {
		if v.ParentItemID == harness.CancellationFenceParentItemID {
			count++
			result = v
		}
	}
	if count != 1 || result.TurnID != tid || result.RunID != rid || !harness.ValidCancellationFenceIntentAdmission(result) {
		t.Fatalf("expected exactly one source-bound election: count=%d item=%+v", count, result)
	}
	return result
}
func ea1CancelReceipts(items []harness.Item, id string) []harness.Item {
	var out []harness.Item
	for _, v := range items {
		if v.ParentItemID == id {
			out = append(out, v)
		}
	}
	return out
}
func ea1AssertCancelledBudget(t *testing.T, before, after budget.Ledger) {
	t.Helper()
	expected := budget.CloneLedger(before)
	expected.Cancelled = true
	expected.Revision++
	for id, v := range expected.Reservations {
		if !v.Dispatched && v.Status != budget.ReservationSettled {
			v.Status = budget.ReservationReleased
			v.Active = false
			expected.Reservations[id] = v
		}
	}
	if !reflect.DeepEqual(expected, after) {
		t.Fatalf("full ledger differs from exactly one authorized Cancel transition:\nexpected=%+v\nactual=%+v", expected, after)
	}
}
