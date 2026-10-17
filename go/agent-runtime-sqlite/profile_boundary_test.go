package sqlite_test

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"

	sqlite "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-sqlite"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

// Embedded V1's supported isolation boundary is one private SQLite file per
// trusted Profile, NOT a shared file with untrusted raw Run-ID operations.
// Reusing the same Run ID in independent Profile databases must never mix
// state or events, including after both Store handles are closed/reopened.
// This is file isolation, NOT shared-DB multi-Profile authorization.
func TestSeparatePrivateProfilesKeepRunIDsAndRecoveryIndependentAfterReopen(t *testing.T) {
	pathA := filepath.Join(privateDir(t), "profile.sqlite")
	pathB := filepath.Join(privateDir(t), "profile.sqlite")
	openNew := func(path string) (*sqlite.Store, *kernel.Runtime) {
		t.Helper()
		store, err := sqlite.CreateNew(t.Context(), path)
		if err != nil {
			t.Fatal(err)
		}
		runtime, err := kernel.New(kernel.Dependencies{Store: store})
		if err != nil {
			t.Fatal(err)
		}
		return store, runtime
	}
	firstA, rtA := openNew(pathA)
	firstB, rtB := openNew(pathB)
	create := func(rt *kernel.Runtime, actor, state string) kernel.Snapshot {
		t.Helper()
		snapshot, err := rt.Create(t.Context(), kernel.CreateRequest{
			ID: "overlapping-run-id", Kind: "test.profile",
			Actor:  kernel.ActorRef{TenantID: "local", ActorID: actor},
			Thread: kernel.ThreadRef{Kind: "conversation", ID: "thread"},
			Goal:   "profile boundary", State: json.RawMessage(state),
			Events: []kernel.EventDraft{{Type: "profile." + actor, Wakeup: true}},
		})
		if err != nil {
			t.Fatal(err)
		}
		return snapshot
	}
	a := create(rtA, "profile-A", `{"data":"A-private"}`)
	b := create(rtB, "profile-B", `{"data":"B-private"}`)
	if a.Run.ID != b.Run.ID || string(a.State) == string(b.State) {
		t.Fatal("fixture failed to reuse identical logical Run IDs across separate databases")
	}
	_, err := rtA.Create(t.Context(), kernel.CreateRequest{
		ID: "A-private-id", Kind: "test.profile",
		Actor:  kernel.ActorRef{TenantID: "local", ActorID: "profile-A"},
		Thread: kernel.ThreadRef{Kind: "conversation", ID: "thread"},
		Goal:   "private A", State: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = rtB.Load(t.Context(), "A-private-id"); !errors.Is(err, kernel.ErrNotFound) {
		t.Fatalf("another Profile unexpectedly saw A-only identity: %v", err)
	}
	if err := firstA.Close(); err != nil {
		t.Fatal(err)
	}
	if err := firstB.Close(); err != nil {
		t.Fatal(err)
	}
	checkReopened := func(path, actor, state string) {
		t.Helper()
		store, err := sqlite.OpenExisting(t.Context(), path)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = store.Close() }()
		rt, err := kernel.New(kernel.Dependencies{Store: store})
		if err != nil {
			t.Fatal(err)
		}
		snapshot, err := rt.Load(t.Context(), "overlapping-run-id")
		if err != nil || snapshot.Run.Actor.ActorID != actor || string(snapshot.State) != state {
			t.Fatalf("profile crossed database after restart: %+v err=%v", snapshot, err)
		}
		events, err := rt.ListEvents(t.Context(), snapshot.Run.ID, 0, 20)
		if err != nil || len(events) != 2 || events[1].Type != "profile."+actor {
			t.Fatalf("profile event journal was mixed: %+v err=%v", events, err)
		}
	}
	checkReopened(pathA, "profile-A", `{"data":"A-private"}`)
	checkReopened(pathB, "profile-B", `{"data":"B-private"}`)
	probe, err := sqlite.OpenExisting(t.Context(), pathB)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = probe.Close() }()
	if _, err = probe.Load(t.Context(), "A-private-id"); !errors.Is(err, kernel.ErrNotFound) {
		t.Fatalf("Profile B accessed A's distinct Run after Store reopen: %v", err)
	}
}
