// Package conformance provides repeatable Harness Store contract checks for adapters.
package conformance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"testing"
	"time"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/handoff"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

type RemoteCancellationCase struct {
	MemberRevision bool
	CancelRoot     bool
	Shadow         bool
	LocalCollision bool
}

type unusedModel struct{}

func (unusedModel) Generate(context.Context, model.Request) (model.Response, error) {
	return model.Response{}, errors.New("unexpected model call during cancellation")
}

func proofFactID(prefix, parent string) string {
	digest := sha256.Sum256([]byte(parent + "\x00"))
	return prefix + "_" + hex.EncodeToString(digest[:12])
}

// RemoteHandoffCancellation checks persisted remote ownership. A Handoff can
// have a settled physical Start receipt while its ChildRunner's Run lives in
// an independent Kernel and cannot be cancelled by Harness's local Kernel.
func RemoteHandoffCancellation(t *testing.T, store harness.Store, c RemoteCancellationCase, reopen func() harness.Store) {
	t.Helper()
	ctx := t.Context()
	now := time.Now().UTC()
	rt, err := kernel.New(kernel.Dependencies{Store: memory.NewStore()})
	if err != nil {
		t.Fatal(err)
	}
	ag, err := agent.NewRunner(agent.Dependencies{Runtime: rt, Model: unusedModel{}})
	if err != nil {
		t.Fatal(err)
	}
	turnID := "pr82-remote-cancel"
	rootRunID := "pr82-local-root-run"
	rootID, err := harness.InvocationID(turnID, "", harness.CapabilityAgent, "root")
	if err != nil {
		t.Fatal(err)
	}
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
	// This fixture represents a real admitted root Agent. Its RequestID,
	// Invocation ID and frozen Input must match the physical Kernel Run;
	// arbitrary test IDs are not a valid execution owner receipt.
	rootInput, err := json.Marshal(struct {
		Goal   string           `json:"goal"`
		Actor  kernel.ActorRef  `json:"actor"`
		Thread kernel.ThreadRef `json:"thread"`
	}{Goal: "delegate", Actor: actor, Thread: thread})
	if err != nil {
		t.Fatal(err)
	}
	rootDigest := sha256.Sum256(rootInput)
	root := harness.Invocation{ID: rootID, TurnID: turnID, CapabilityKey: harness.CapabilityAgent,
		Input: rootInput, InputHash: hex.EncodeToString(rootDigest[:]),
		ExecutionRefID: rootRunID, Status: harness.InvocationRunning,
		Attempt: 1, Revision: 1, OutputRefs: []harness.HostRef{}, CreatedAt: now, UpdatedAt: now}
	if _, _, err := store.CreateInvocation(ctx, root); err != nil {
		t.Fatal(err)
	}
	delegation := handoff.Delegation{ID: "pr82-delegation", MemberID: "specialist", ChildRunID: childRunID, Goal: "inspect", Status: handoff.StatusQueued}
	if c.MemberRevision {
		delegation.MemberRevision = "specialist-v1"
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
		ExecutionRefID: childRunID, Input: input, InputHash: hex.EncodeToString(digest[:]),
		Status: harness.InvocationAccepted, Attempt: 1, Revision: 1, OutputRefs: []harness.HostRef{},
		CreatedAt: now, UpdatedAt: now}
	if _, _, err := store.CreateInvocation(ctx, child); err != nil {
		t.Fatal(err)
	}
	if c.LocalCollision {
		// Two independently owned Agent Runs may legitimately share every
		// public task field. Persist the local one before the Handoff Grant.
		if _, err := rt.Create(ctx, kernel.CreateRequest{ID: childRunID, Kind: agent.RunKind,
			Actor: actor, Thread: thread, RequestID: rootRunID + ":" + delegation.ID,
			Goal: delegation.Goal, State: []byte(`{}`)}); err != nil {
			t.Fatal(err)
		}
	}
	if c.Shadow {
		// A2A's local shadow cannot be cancelled using only the native
		// Kernel: remote Task cancellation must be performed by A2A itself.
		if _, err := rt.Create(ctx, kernel.CreateRequest{ID: childRunID, Kind: handoff.RemoteAgentRunKind,
			Actor: actor, Thread: thread, RequestID: delegation.ID, Goal: delegation.Goal,
			State: []byte(`{}`)}); err != nil {
			t.Fatal(err)
		}
	}
	grant := harness.Item{ID: "pr82-handoff-grant", TurnID: turnID, InvocationID: child.ID, RunID: childRunID,
		Kind: harness.ItemDiagnostic, Status: harness.ItemStarted, ParentItemID: harness.ExecutionDispatchParentItemID,
		Payload: json.RawMessage(`{"phase":"handoff-start","physicalRunID":"pr82-remote-child-run"}`), CreatedAt: now, UpdatedAt: now}
	if _, _, err := store.AppendItem(ctx, grant); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.AppendItem(ctx, harness.Item{ID: proofFactID("hdispatchdone", grant.ID), TurnID: turnID,
		InvocationID: grant.InvocationID, RunID: grant.RunID,
		Kind: harness.ItemDiagnostic, Status: harness.ItemCompleted, ParentItemID: grant.ID,
		Payload: json.RawMessage(`{"boundary":"handoff_start_returned"}`), CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	for i := range 2 {
		if reopen != nil {
			store = reopen()
		}
		runner, err := harness.NewRunner(harness.Dependencies{Runtime: rt, Agent: ag, Store: store})
		if err != nil {
			t.Fatal(err)
		}
		if c.CancelRoot {
			_, err = runner.Cancel(ctx, turnID, "stop")
		} else {
			_, err = runner.CancelInvocation(ctx, child.ID, "stop")
		}
		// A repeated direct-child request cannot resume its still-fenced
		// cancellation because no local Kernel Run can prove it completed.
		// Root retry, in contrast, can resume from its cancelled Kernel Run.
		if i == 1 && !c.CancelRoot {
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
	if c.CancelRoot && rootSnapshot.Run.Status != kernel.RunStatusCancelled {
		t.Fatalf("root cancellation was not applied: %s", rootSnapshot.Run.Status)
	}
	if !c.CancelRoot && rootSnapshot.Run.Status != kernel.RunStatusRunning {
		t.Fatalf("child-only cancellation touched root: %s", rootSnapshot.Run.Status)
	}
	if c.Shadow || c.LocalCollision {
		local, err := rt.Load(ctx, childRunID)
		if err != nil || local.Run.Status != kernel.RunStatusRunning {
			t.Fatalf("default Kernel cancelled a remote-owned/shadow Run without route proof: %+v %v", local.Run, err)
		}
	}
}
