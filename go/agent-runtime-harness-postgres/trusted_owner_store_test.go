package harnesspostgres_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"sync"
	"testing"
	"time"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

// These tests deliberately exercise the genuine Store implementations rather
// than a fault-injection wrapper that fabricates GetSession/GetInvocation
// returns. ADR 0017 defines the Store as a trusted integrity boundary.
func TestTrustedHarnessStoresFreezeSessionAndRootInvocationOwners(t *testing.T) {
	for name, build := range rootInvocationStores() {
		t.Run(name, func(t *testing.T) {
			assertTrustedHarnessStoreOwners(t, build(t), "owner-"+name)
		})
	}
}

func assertTrustedHarnessStoreOwners(t *testing.T, store harness.Store, prefix string) {
	t.Helper()
	now := time.Date(2026, 11, 6, 0, 0, 0, 0, time.UTC)
	actor := kernel.ActorRef{TenantID: "tenant-b", ActorID: "original-owner"}
	thread := kernel.ThreadRef{Kind: "conversation", ID: "tenant-b-thread"}
	hostThread := harness.HostRef{Kind: "conversation", ID: prefix + "-tenant-b-thread"}
	sid, err := harness.SessionID(hostThread)
	if err != nil {
		t.Fatal(err)
	}
	session := harness.Session{
		ID: sid, HostThread: hostThread, Actor: actor,
		Revision: 1, CreatedAt: now, UpdatedAt: now,
	}
	created, fresh, err := store.CreateSession(t.Context(), session)
	if err != nil || !fresh || created.Actor != actor {
		t.Fatalf("original owner not created: created=%+v fresh=%t err=%v", created, fresh, err)
	}
	for _, variant := range []struct {
		name   string
		mutate func(*harness.Session)
	}{
		{"same", func(*harness.Session) {}},
		{"other_tenant", func(v *harness.Session) { v.Actor.TenantID = "tenant-a" }},
		{"other_actor", func(v *harness.Session) { v.Actor.ActorID = "user-a" }},
		{"foreign_host_thread", func(v *harness.Session) { v.HostThread.ID = "other-thread" }},
		{"foreign_host_kind", func(v *harness.Session) { v.HostThread.Kind = "workspace" }},
	} {
		t.Run("session-"+variant.name, func(t *testing.T) {
			candidate := session
			variant.mutate(&candidate)
			loaded, inserted, createErr := store.CreateSession(t.Context(), candidate)
			if variant.name == "same" {
				if createErr != nil || inserted || loaded.Actor != actor {
					t.Fatalf("idempotent replay changed owner: %+v fresh=%t err=%v", loaded, inserted, createErr)
				}
			} else if !errors.Is(createErr, harness.ErrConflict) || inserted {
				t.Fatalf("Session ID rebound by %s: %+v fresh=%t err=%v", variant.name, loaded, inserted, createErr)
			}
			persisted, readErr := store.GetSession(t.Context(), sid)
			if readErr != nil || persisted.Actor != actor || persisted.HostThread != hostThread {
				t.Fatalf("read changed immutable owner: %+v err=%v", persisted, readErr)
			}
		})
	}
	// Race concurrent hostile replay against a pre-existing immutable owner.
	const workers = 24
	start := make(chan struct{})
	results := make(chan error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			<-start
			forged := session
			if index%2 == 0 {
				forged.Actor.TenantID = "tenant-a"
			} else {
				forged.Actor.ActorID = "intruder"
			}
			_, _, createErr := store.CreateSession(t.Context(), forged)
			results <- createErr
		}(i)
	}
	close(start)
	wg.Wait()
	close(results)
	for createErr := range results {
		if !errors.Is(createErr, harness.ErrConflict) {
			t.Fatalf("concurrent actor replacement gained Session authority: %v", createErr)
		}
	}
	after, err := store.GetSession(t.Context(), sid)
	if err != nil || after.Actor != actor || after.HostThread != hostThread {
		t.Fatalf("concurrent replay retargeted original Session: %+v %v", after, err)
	}

	turn := harness.HostRef{Kind: "conversation_turn", ID: "turn-one"}
	tid, err := harness.TurnID(sid, turn)
	if err != nil {
		t.Fatal(err)
	}
	config, err := harness.SealConfigSnapshot(tid, harness.ConfigSnapshot{Model: "fixture"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = store.PutConfigSnapshot(t.Context(), config); err != nil {
		t.Fatal(err)
	}
	if _, _, err = store.CreateTurn(t.Context(), harness.Turn{
		ID: tid, SessionID: sid, HostTurn: turn, ConfigSnapshotID: config.ID,
		Status: harness.TurnAccepted, Revision: 1, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	input, hash := trustedRootIdentityInput(t, actor, thread, "complete original work")
	rootID, err := harness.AgentInvocationID(tid, turn.ID)
	if err != nil {
		t.Fatal(err)
	}
	root := harness.Invocation{
		ID: rootID, TurnID: tid, CapabilityKey: harness.CapabilityAgent,
		DefinitionVersion: harness.RuntimeCapabilityVersion, ExecutionClass: harness.ExecutionAgent,
		Input: input, InputHash: hash, ExecutionRefID: harness.AgentExecutionRefID(rootID),
		Status: harness.InvocationAccepted, Attempt: 1, Revision: 1,
		OutputRefs: []harness.HostRef{}, CreatedAt: now, UpdatedAt: now,
	}
	original, inserted, err := store.CreateInvocation(t.Context(), root)
	if err != nil || !inserted || original.ID != rootID {
		t.Fatalf("original root create: %+v fresh=%t err=%v", original, inserted, err)
	}
	if replay, inserted, err := store.CreateInvocation(t.Context(), root); err != nil || inserted || replay.ID != rootID {
		t.Fatalf("root replay failed: %+v fresh=%t err=%v", replay, inserted, err)
	}

	// Persistent root identity remains immutable even when a hostile caller
	// rewrites both Input.Actor/Thread AND its unkeyed SHA-256 InputHash.
	rng := rand.New(rand.NewSource(20261106))
	for n := 0; n < 96; n++ {
		changed := root
		nextActor, nextThread := actor, thread
		switch rng.Intn(5) {
		case 0:
			nextActor.TenantID = fmt.Sprintf("tenant-%d", n)
		case 1:
			nextActor.ActorID = fmt.Sprintf("owner-%d", n)
		case 2:
			nextThread.Kind = "job"
		case 3:
			nextThread.ID = fmt.Sprintf("other-thread-%d", n)
		case 4:
			changed.DefinitionVersion = fmt.Sprintf("version-%d", n)
		}
		changed.Input, changed.InputHash = trustedRootIdentityInput(t, nextActor, nextThread, "complete original work")
		if changed.DefinitionVersion == root.DefinitionVersion && string(changed.Input) == string(root.Input) {
			t.Fatalf("test mutation did not change frozen identity: %d", n)
		}
		if _, fresh, err := store.CreateInvocation(t.Context(), changed); !errors.Is(err, harness.ErrConflict) || fresh {
			t.Fatalf("seeded root replay retargeting accepted n=%d err=%v fresh=%t", n, err, fresh)
		}
		if _, err := store.UpdateInvocation(t.Context(), changed, original.Revision); !errors.Is(err, harness.ErrConflict) {
			t.Fatalf("seeded root CAS retargeting accepted n=%d err=%v", n, err)
		}
	}
	// Even a caller holding a returned raw JSON slice cannot mutate the
	// Store's frozen Input via aliasing or the execution-ref index.
	fetched, err := store.GetInvocation(t.Context(), rootID)
	if err != nil {
		t.Fatal(err)
	}
	if len(fetched.Input) == 0 {
		t.Fatal("empty frozen input")
	}
	fetched.Input[0] ^= 0x1
	persisted, err := store.GetInvocation(t.Context(), rootID)
	if err != nil || string(persisted.Input) != string(input) || persisted.InputHash != hash {
		t.Fatalf("Store retained caller-mutated root input: %+v %v", persisted, err)
	}
	byRef, err := store.GetInvocationByExecutionRefID(t.Context(), root.ExecutionRefID)
	if err != nil || byRef.ID != rootID || string(byRef.Input) != string(input) || byRef.InputHash != hash {
		t.Fatalf("execution ref switched frozen root: %+v %v", byRef, err)
	}
	// The same frozen root legitimately progresses status using CAS, while
	// stale revisions and changes to the immutable Input remain rejected.
	running := persisted
	running.Status = harness.InvocationRunning
	running, err = store.UpdateInvocation(t.Context(), running, persisted.Revision)
	if err != nil || running.Revision != persisted.Revision+1 {
		t.Fatalf("status CAS failed: %+v %v", running, err)
	}
	if _, err := store.UpdateInvocation(t.Context(), persisted, persisted.Revision); !errors.Is(err, harness.ErrConflict) {
		t.Fatalf("stale CAS reopened older owner: %v", err)
	}
	afterRun, err := store.GetInvocation(t.Context(), rootID)
	if err != nil || afterRun.Status != harness.InvocationRunning || string(afterRun.Input) != string(input) ||
		afterRun.InputHash != hash || afterRun.ExecutionRefID != root.ExecutionRefID {
		t.Fatalf("status progression changed owner: %+v %v", afterRun, err)
	}
}

func trustedRootIdentityInput(t *testing.T, actor kernel.ActorRef, thread kernel.ThreadRef, goal string) (json.RawMessage, string) {
	t.Helper()
	input, err := json.Marshal(struct {
		Goal   string           `json:"goal"`
		Actor  kernel.ActorRef  `json:"actor"`
		Thread kernel.ThreadRef `json:"thread"`
	}{Goal: goal, Actor: actor, Thread: thread})
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(input)
	return input, hex.EncodeToString(sum[:])
}
