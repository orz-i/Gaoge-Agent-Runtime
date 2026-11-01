package harness_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/handoff"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/team"
)

func TestPR82RawRelationCollisionPreservesRootIntent(t *testing.T) {
	for _, mode := range []string{"foreign", "identical", "terminal-identical"} {
		for _, ownerScope := range []string{"none", "other-turn", "same-turn", "other-handoff", "same-handoff"} {
			t.Run(mode+"/"+ownerScope, func(t *testing.T) { pr82RawRelationRootCollision(t, mode, ownerScope) })
		}
	}
}

func pr82RawRelationRootCollision(t *testing.T, mode, ownerScope string) {
	runtime := newFeatureInvocationRuntime(t)
	store := harness.NewMemoryStore()
	relations := newFeatureInvocationRelations(t)
	children := &pr82OrphanMemberChildren{runs: map[string]kernel.Snapshot{}}
	coordinator, err := handoff.New(children)
	if err != nil {
		t.Fatal(err)
	}
	feature, err := team.NewRunner(team.Dependencies{Runtime: runtime, Handoffs: coordinator, Relations: relations, MaxMembers: 4})
	if err != nil {
		t.Fatal(err)
	}
	runner, err := harness.NewRunner(harness.Dependencies{
		Runtime: runtime, Agent: loadingFeatureAgent{runtime: runtime}, Store: store,
		Clock: featureInvocationClock{}, Teams: feature, Relations: relations,
	})
	if err != nil {
		t.Fatal(err)
	}
	failed, err := runner.StartTeamTurn(t.Context(), harness.TeamTurnRequest{
		StartRequest: harness.StartRequest{
			HostThread: harness.HostRef{Kind: testThreadKind, ID: "pr82-orphan-root-thread"},
			HostTurn:   harness.HostRef{Kind: testContextHostKind, ID: "pr82-orphan-root-turn"},
			Actor:      kernel.ActorRef{TenantID: testTenant, ActorID: testActor},
			Thread:     kernel.ThreadRef{Kind: testThreadKind, ID: "pr82-orphan-root-thread"},
			RequestID:  "pr82-orphan-start", Goal: "two independent team members",
			Config: harness.ConfigSnapshot{Model: "fixture-model"},
		},
		Mode:    team.ExecutionParallel,
		Members: []team.Member{{ID: "failing", Goal: "fail-fast"}, {ID: "running", Goal: "continues"}},
		Join:    handoff.Join{Mode: handoff.JoinAll, Quorum: 1, FailurePolicy: handoff.FailureFailFast},
	})
	if !errors.Is(err, team.ErrTeamFailed) || failed.Turn.Status != harness.TurnFailed {
		t.Fatalf("expected terminal FailFast Team, got %v %s", err, failed.Turn.Status)
	}
	root, ok := harness.TopLevelInvocation(failed)
	if !ok || root.Status != harness.InvocationFailed {
		t.Fatalf("root status: %+v", root)
	}
	descendants, err := relations.ListChildren(t.Context(), root.ExecutionRefID)
	if err != nil || len(descendants) != 2 {
		t.Fatalf("missing physical member relations: %+v %v", descendants, err)
	}
	living := 0
	for _, child := range descendants {
		snap, loadErr := children.LoadRun(t.Context(), child.ChildRunID)
		if loadErr != nil {
			t.Fatal(loadErr)
		}
		if snap.Run.Status == kernel.RunStatusRunning {
			living++
		}
	}
	if living != 1 {
		t.Fatalf("expected exactly one member still running; got %d", living)
	}
	invocations, err := store.ListInvocations(t.Context(), failed.Turn.ID)
	if err != nil || len(invocations) != 1 {
		t.Fatalf("member unexpectedly has Harness Invocation: %+v %v", invocations, err)
	}
	items, err := store.ListItems(t.Context(), failed.Turn.ID, 0, 500)
	if err != nil || harness.PendingExecutionDispatch(items, "") {
		t.Fatalf("root dispatch grant did not settle: %v", err)
	}
	// Final independent merge audit: the external Member owns a completely
	// different Kernel. Foreign local Runs happen to share *both* member IDs.
	// The local Kernel must not become a physical cancel-owner merely by ID.
	for _, relation := range descendants {
		request := kernel.CreateRequest{
			ID: relation.ChildRunID, Kind: agent.RunKind,
			Actor:     kernel.ActorRef{TenantID: "foreign-tenant", ActorID: "foreign-actor"},
			Thread:    kernel.ThreadRef{Kind: "conversation", ID: "different-thread"},
			RequestID: "foreign-collision", Goal: "not the real remote member", State: []byte(`{}`),
		}
		if mode != "foreign" {
			remote, loadErr := children.LoadRun(t.Context(), relation.ChildRunID)
			if loadErr != nil {
				t.Fatal(loadErr)
			}
			request.Actor, request.Thread = remote.Run.Actor, remote.Run.Thread
			request.Goal, request.RequestID = remote.Run.Goal, remote.Run.RequestID
		}
		local, localErr := runtime.Create(t.Context(), request)
		if localErr == nil && mode == "terminal-identical" {
			_, localErr = runtime.Cancel(t.Context(), local.Run.ID, local.Run.Revision, "unrelated local completion")
		}
		if localErr != nil {
			t.Fatalf("seed foreign local collision: %v", localErr)
		}
	}
	// A separate native Invocation cannot lend ownership to the raw remote
	// Feature edge, even when it belongs to the same Turn.
	if ownerScope != "none" {
		for _, relation := range descendants {
			foreignTurn := failed.Turn
			if strings.HasPrefix(ownerScope, "other-") {
				foreignTurn.ID = "foreign-turn-" + relation.ChildRunID
				foreignTurn.Status = harness.TurnRunning
				if _, _, err := store.CreateTurn(t.Context(), foreignTurn); err != nil {
					t.Fatal(err)
				}
			}
			foreignInvocation := root
			foreignInvocation.ID = "foreign-inv-" + relation.ChildRunID
			foreignInvocation.TurnID = foreignTurn.ID
			foreignInvocation.ExecutionRefID = relation.ChildRunID
			foreignInvocation.ExecutionClass = harness.ExecutionAgent
			foreignInvocation.CapabilityKey = harness.CapabilityAgent
			foreignInvocation.Status = harness.InvocationRunning
			if strings.HasPrefix(ownerScope, "same-") {
				foreignInvocation.ParentItemID = "unrelated-local-parent"
			}
			if strings.HasSuffix(ownerScope, "-handoff") {
				foreignInvocation.ParentItemID = "foreign-handoff-item-" + relation.ChildRunID
				delegationID := "foreign-delegation-" + relation.ChildRunID
				foreignInvocation.ID, err = harness.InvocationID(foreignTurn.ID, foreignInvocation.ParentItemID, harness.CapabilityAgent, delegationID)
				if err != nil {
					t.Fatal(err)
				}
				foreignInvocation.Status = harness.InvocationAccepted
				foreignInvocation.Input, err = json.Marshal(map[string]any{"id": delegationID, "memberID": "foreign-member", "childRunID": relation.ChildRunID, "goal": "unrelated accepted Handoff", "harnessRoot": map[string]any{"invocationID": "foreign-root", "executionRefID": "foreign-root-run", "attempt": 1}})
				if err != nil {
					t.Fatal(err)
				}
				digest := sha256.Sum256(foreignInvocation.Input)
				foreignInvocation.InputHash = hex.EncodeToString(digest[:])
			}
			if _, _, err := store.CreateInvocation(t.Context(), foreignInvocation); err != nil {
				t.Fatal(err)
			}
		}
	}
	// Independent live Team repro: the Harness root is Failed while its
	// real physical ChildRunner still has a Running member.
	_, cancelledRootErr := runner.Cancel(t.Context(), failed.Turn.ID, "stop remaining Team members")
	children.mu.Lock()
	stillRunning := 0
	for _, member := range children.runs {
		if member.Run.Status == kernel.RunStatusRunning {
			stillRunning++
		}
	}
	children.mu.Unlock()
	if !errors.Is(cancelledRootErr, harness.ErrRuntimeCancellationUnverified) || stillRunning != 1 {
		t.Fatalf("unverified remote Member must preserve failed root cleanup: err=%v running=%d", cancelledRootErr, stillRunning)
	}
	for _, relation := range descendants {
		local, loadErr := runtime.Load(t.Context(), relation.ChildRunID)
		expected := kernel.RunStatusRunning
		if mode == "terminal-identical" {
			expected = kernel.RunStatusCancelled
		}
		if loadErr != nil || local.Run.Status != expected {
			t.Fatalf("foreign local Run changed: %+v %v", local.Run, loadErr)
		}
	}
	items, err = store.ListItems(t.Context(), failed.Turn.ID, 0, 500)
	if err != nil || !harness.ActiveCancellationFence(items) {
		t.Fatalf("unknown external cleanup lost durable cancellation intent: %v", err)
	}
	// A Failed root is terminal in Kernel, so the same unresolved cancellation
	// intent must be resumable without allocating another execution Attempt.
	if _, retryErr := runner.Cancel(t.Context(), failed.Turn.ID, "retry remote Member cleanup"); !errors.Is(retryErr, harness.ErrRuntimeCancellationUnverified) {
		t.Fatalf("failed root did not resume cleanup under original intent: %v", retryErr)
	}
	_, err = store.RetryInvocation(t.Context(), root.ID, root.Revision, "new-root-run-must-not-start", featureInvocationClock{}.Now())
	if !errors.Is(err, harness.ErrConflict) {
		t.Fatalf("old physical member continued after root retry: %v", err)
	}
	unchanged, err := store.GetInvocation(t.Context(), root.ID)
	if err != nil || unchanged.Attempt != root.Attempt || unchanged.ExecutionRefID != root.ExecutionRefID {
		t.Fatalf("failed Retry mutated root: %+v %v", unchanged, err)
	}
}

func TestPR82RawRelationLocalCollisionBypassesDirectCancel(t *testing.T) {
	runtime := newFeatureInvocationRuntime(t)
	store := harness.NewMemoryStore()
	relations := newFeatureInvocationRelations(t)
	children := &pr82OrphanMemberChildren{runs: map[string]kernel.Snapshot{}}
	coordinator, err := handoff.New(children)
	if err != nil {
		t.Fatal(err)
	}
	feature, err := team.NewRunner(team.Dependencies{Runtime: runtime, Handoffs: coordinator, Relations: relations, MaxMembers: 4})
	if err != nil {
		t.Fatal(err)
	}
	runner, err := harness.NewRunner(harness.Dependencies{
		Runtime: runtime, Agent: loadingFeatureAgent{runtime: runtime}, Store: store,
		Clock: featureInvocationClock{}, Teams: feature, Relations: relations,
	})
	if err != nil {
		t.Fatal(err)
	}
	failed, err := runner.StartTeamTurn(t.Context(), harness.TeamTurnRequest{
		StartRequest: harness.StartRequest{
			HostThread: harness.HostRef{Kind: testThreadKind, ID: "pr82-orphan-root-thread"},
			HostTurn:   harness.HostRef{Kind: testContextHostKind, ID: "pr82-orphan-root-turn"},
			Actor:      kernel.ActorRef{TenantID: testTenant, ActorID: testActor},
			Thread:     kernel.ThreadRef{Kind: testThreadKind, ID: "pr82-orphan-root-thread"},
			RequestID:  "pr82-orphan-start", Goal: "two independent team members",
			Config: harness.ConfigSnapshot{Model: "fixture-model"},
		},
		Mode:    team.ExecutionParallel,
		Members: []team.Member{{ID: "failing", Goal: "fail-fast"}, {ID: "running", Goal: "continues"}},
		Join:    handoff.Join{Mode: handoff.JoinAll, Quorum: 1, FailurePolicy: handoff.FailureFailFast},
	})
	if !errors.Is(err, team.ErrTeamFailed) || failed.Turn.Status != harness.TurnFailed {
		t.Fatalf("expected terminal FailFast Team, got %v %s", err, failed.Turn.Status)
	}
	root, ok := harness.TopLevelInvocation(failed)
	if !ok || root.Status != harness.InvocationFailed {
		t.Fatalf("root status: %+v", root)
	}
	descendants, err := relations.ListChildren(t.Context(), root.ExecutionRefID)
	if err != nil || len(descendants) != 2 {
		t.Fatalf("missing physical member relations: %+v %v", descendants, err)
	}
	living := 0
	for _, child := range descendants {
		snap, loadErr := children.LoadRun(t.Context(), child.ChildRunID)
		if loadErr != nil {
			t.Fatal(loadErr)
		}
		if snap.Run.Status == kernel.RunStatusRunning {
			living++
		}
	}
	if living != 1 {
		t.Fatalf("expected exactly one member still running; got %d", living)
	}
	invocations, err := store.ListInvocations(t.Context(), failed.Turn.ID)
	if err != nil || len(invocations) != 1 {
		t.Fatalf("member unexpectedly has Harness Invocation: %+v %v", invocations, err)
	}
	items, err := store.ListItems(t.Context(), failed.Turn.ID, 0, 500)
	if err != nil || harness.PendingExecutionDispatch(items, "") {
		t.Fatalf("root dispatch grant did not settle: %v", err)
	}
	// Same ID has no physical execution-owner meaning across distinct Kernels.
	for _, r := range descendants {
		if _, e := runtime.Create(t.Context(), kernel.CreateRequest{
			ID: r.ChildRunID, Kind: agent.RunKind,
			Actor:     kernel.ActorRef{TenantID: "other-tenant", ActorID: "other-actor"},
			Thread:    kernel.ThreadRef{Kind: "conversation", ID: "foreign-thread"},
			RequestID: "foreign-request", Goal: "foreign-owner-not-team", State: []byte(`{}`),
		}); e != nil {
			t.Fatal(e)
		}
	}
	remoteID := ""
	for _, r := range descendants {
		remote, e := children.LoadRun(t.Context(), r.ChildRunID)
		if e != nil {
			t.Fatal(e)
		}
		if remote.Run.Status == kernel.RunStatusRunning {
			remoteID = r.ChildRunID
		}
	}
	if remoteID == "" {
		t.Fatal("no actual running remote member")
	}
	observed, e := runner.Load(t.Context(), failed.Turn.ID)
	if e != nil {
		t.Fatal(e)
	}
	taskID := ""
	for _, subtask := range observed.Subtasks {
		if subtask.RunID == remoteID {
			taskID = subtask.ID
		}
	}
	if taskID == "" {
		t.Fatalf("missing remote member Subtask: %+v", observed.Subtasks)
	}
	_, cancelErr := runner.CancelSubtask(t.Context(), failed.Turn.ID, taskID, "stop real remote member")
	remote, e := children.LoadRun(t.Context(), remoteID)
	if e != nil {
		t.Fatal(e)
	}
	items, e = store.ListItems(t.Context(), failed.Turn.ID, 0, 500)
	if e != nil {
		t.Fatal(e)
	}
	if !errors.Is(cancelErr, harness.ErrRuntimeCancellationUnverified) || remote.Run.Status != kernel.RunStatusRunning || !harness.ActiveCancellationFence(items) {
		t.Fatalf("P1: CancelSubtask claimed successful remote cleanup based on a colliding foreign local Run; remote=%s intent=%v", remote.Run.Status, harness.ActiveCancellationFence(items))
	}
}

func TestPR82RawRelationLocalCollisionPoisonsSubtaskRead(t *testing.T) {
	runtime := newFeatureInvocationRuntime(t)
	store := harness.NewMemoryStore()
	relations := newFeatureInvocationRelations(t)
	children := &pr82OrphanMemberChildren{runs: map[string]kernel.Snapshot{}}
	coordinator, err := handoff.New(children)
	if err != nil {
		t.Fatal(err)
	}
	feature, err := team.NewRunner(team.Dependencies{Runtime: runtime, Handoffs: coordinator, Relations: relations, MaxMembers: 4})
	if err != nil {
		t.Fatal(err)
	}
	runner, err := harness.NewRunner(harness.Dependencies{
		Runtime: runtime, Agent: loadingFeatureAgent{runtime: runtime}, Store: store,
		Clock: featureInvocationClock{}, Teams: feature, Relations: relations,
	})
	if err != nil {
		t.Fatal(err)
	}
	failed, err := runner.StartTeamTurn(t.Context(), harness.TeamTurnRequest{
		StartRequest: harness.StartRequest{
			HostThread: harness.HostRef{Kind: testThreadKind, ID: "pr82-orphan-root-thread"},
			HostTurn:   harness.HostRef{Kind: testContextHostKind, ID: "pr82-orphan-root-turn"},
			Actor:      kernel.ActorRef{TenantID: testTenant, ActorID: testActor},
			Thread:     kernel.ThreadRef{Kind: testThreadKind, ID: "pr82-orphan-root-thread"},
			RequestID:  "pr82-orphan-start", Goal: "two independent team members",
			Config: harness.ConfigSnapshot{Model: "fixture-model"},
		},
		Mode:    team.ExecutionParallel,
		Members: []team.Member{{ID: "failing", Goal: "fail-fast"}, {ID: "running", Goal: "continues"}},
		Join:    handoff.Join{Mode: handoff.JoinAll, Quorum: 1, FailurePolicy: handoff.FailureFailFast},
	})
	if !errors.Is(err, team.ErrTeamFailed) || failed.Turn.Status != harness.TurnFailed {
		t.Fatalf("expected terminal FailFast Team, got %v %s", err, failed.Turn.Status)
	}
	root, ok := harness.TopLevelInvocation(failed)
	if !ok || root.Status != harness.InvocationFailed {
		t.Fatalf("root status: %+v", root)
	}
	descendants, err := relations.ListChildren(t.Context(), root.ExecutionRefID)
	if err != nil || len(descendants) != 2 {
		t.Fatalf("missing physical member relations: %+v %v", descendants, err)
	}
	living := 0
	for _, child := range descendants {
		snap, loadErr := children.LoadRun(t.Context(), child.ChildRunID)
		if loadErr != nil {
			t.Fatal(loadErr)
		}
		if snap.Run.Status == kernel.RunStatusRunning {
			living++
		}
	}
	if living != 1 {
		t.Fatalf("expected exactly one member still running; got %d", living)
	}
	invocations, err := store.ListInvocations(t.Context(), failed.Turn.ID)
	if err != nil || len(invocations) != 1 {
		t.Fatalf("member unexpectedly has Harness Invocation: %+v %v", invocations, err)
	}
	items, err := store.ListItems(t.Context(), failed.Turn.ID, 0, 500)
	if err != nil || harness.PendingExecutionDispatch(items, "") {
		t.Fatalf("root dispatch grant did not settle: %v", err)
	}
	// Same ID has no physical execution-owner meaning across distinct Kernels.
	for _, r := range descendants {
		if _, e := runtime.Create(t.Context(), kernel.CreateRequest{
			ID: r.ChildRunID, Kind: agent.RunKind,
			Actor:     kernel.ActorRef{TenantID: "other-tenant", ActorID: "other-actor"},
			Thread:    kernel.ThreadRef{Kind: "conversation", ID: "foreign-thread"},
			RequestID: "foreign-request", Goal: "foreign-owner-not-team", State: []byte(`{}`),
		}); e != nil {
			t.Fatal(e)
		}
	}
	observed, e := runner.Load(t.Context(), failed.Turn.ID)
	if e != nil {
		t.Fatal(e)
	}
	for _, subtask := range observed.Subtasks {
		if subtask.Goal == "foreign-owner-not-team" {
			t.Fatalf("P1: foreign other-tenant local Run leaked into external Team Subtask: %+v", subtask)
		}
	}
}
