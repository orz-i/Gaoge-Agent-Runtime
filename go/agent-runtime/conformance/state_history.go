package conformance

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"testing"
)

// RunStateHistorySuite uses opaque fixture states only to verify the
// persistence boundary. Actual Agent seals and physical recovery are
// exercised by existing Agent/Managed conformance.
func RunStateHistorySuite(t *testing.T, store kernel.Store, reopen func() kernel.Store, counts func() (int, error), collision func(bool) error, corrupt func() error) {
	t.Helper()
	ctx := t.Context()
	rt, e := kernel.New(kernel.Dependencies{Store: store})
	if e != nil {
		t.Fatal(e)
	}
	message := `{"role":"user","content":"` + strings.Repeat(`original evidence `, 32) + `"}`
	transcript := `[` + message + `]`
	frozen := `[{"role":"system","content":"original policy"},` + message + `]`
	a := json.RawMessage(`{"messages":` + transcript + `,"modelInvocations":[{"status":"consumed","id":"one"},{"status":"pending","id":"two","request":{"messages":` + frozen + `}}],"_originalStateEvidence":{"version":"agent-state-opaque-v4","digest":"fixture"}}`)
	b := json.RawMessage(`{"messages":[` + message + `,{"role":"assistant","content":"answer"}],"modelInvocations":[{"status":"consumed","id":"one"},{"status":"completed","id":"two","request":{"messages":` + frozen + `}}],"_originalStateEvidence":{"version":"agent-state-opaque-v4","digest":"fixture"}}`)
	c := json.RawMessage(`{"messages":[{"role":"user","content":"compacted"}],"modelInvocations":[{"status":"consumed","id":"one"},{"status":"consumed","id":"two"}],"_originalStateEvidence":{"version":"agent-state-opaque-v4","digest":"fixture"}}`)
	original, e := rt.Create(ctx, kernel.CreateRequest{ID: "state-history", Kind: "agent", Actor: kernel.ActorRef{TenantID: "tenant", ActorID: "actor"}, Thread: kernel.ThreadRef{Kind: "test", ID: "thread"}, Goal: "exact history", State: a})
	if e != nil {
		t.Fatal(e)
	}
	check := func(s kernel.Snapshot, want json.RawMessage, n int) {
		t.Helper()
		if !bytes.Equal(s.State, want) {
			t.Fatalf("logical state changed: %s", s.State)
		}
		count, e := counts()
		if e != nil || count != n {
			t.Fatalf("archive rows=%d want=%d err=%v", count, n, e)
		}
	}
	check(original, a, 2)
	next, e := rt.Apply(ctx, original.Run.ID, original.Run.Revision, kernel.Mutation{Status: kernel.RunStatusRunning, State: b})
	if e != nil {
		t.Fatal(e)
	}
	check(next, b, 3)
	// An old revision cannot append a different history or alter the prefix.
	if _, e = rt.Apply(ctx, original.Run.ID, original.Run.Revision, kernel.Mutation{Status: kernel.RunStatusRunning, State: c}); !errors.Is(e, kernel.ErrConflict) {
		t.Fatalf("stale write accepted: %v", e)
	}
	fresh := reopen()
	observed, e := fresh.Load(ctx, next.Run.ID)
	if e != nil {
		t.Fatal(e)
	}
	check(observed, b, 3)
	rt, e = kernel.New(kernel.Dependencies{Store: fresh})
	if e != nil {
		t.Fatal(e)
	}
	// Force the second archive append to conflict after the first has
	// executed; the native CAS and first append must both roll back.
	if e = collision(true); e != nil {
		t.Fatal(e)
	}
	if _, e = rt.Apply(ctx, next.Run.ID, next.Run.Revision, kernel.Mutation{Status: kernel.RunStatusRunning, State: c}); e == nil {
		t.Fatal("conflicting history append committed")
	}
	unchanged, e := fresh.Load(ctx, next.Run.ID)
	if e != nil || unchanged.Run.Revision != next.Run.Revision {
		t.Fatal("failed history append advanced Run", e)
	}
	check(unchanged, b, 4)
	if e = collision(false); e != nil {
		t.Fatal(e)
	}
	compacted, e := rt.Apply(ctx, next.Run.ID, next.Run.Revision, kernel.Mutation{Status: kernel.RunStatusRunning, State: c})
	if e != nil {
		t.Fatal(e)
	}
	check(compacted, c, 5)
	latest, e := reopen().Load(ctx, compacted.Run.ID)
	if e != nil {
		t.Fatal(e)
	}
	check(latest, c, 5)
	if e = corrupt(); e != nil {
		t.Fatal(e)
	}
	if _, e = reopen().Load(ctx, next.Run.ID); e == nil {
		t.Fatal("missing original history accepted")
	}
}
