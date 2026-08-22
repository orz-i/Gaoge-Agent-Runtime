package harness

import (
	"context"
	"testing"
	"time"

	runtimecontext "github.com/orz-i/Gaoge/sdk/go/agent-runtime/context"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/model"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/plugin"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/runrelation"
)

type contextRunRelationFixture map[string]runrelation.Relation

const (
	contextRunHostTurnKind = "conversation_turn"
	contextRunTestActorID   = "actor"
	contextRunTestTenantID  = "tenant"
)

func (fixture contextRunRelationFixture) GetByChild(_ context.Context, childRunID string) (runrelation.Relation, error) {
	value, ok := fixture[childRunID]
	if !ok {
		return runrelation.Relation{}, runrelation.ErrNotFound
	}
	return value, nil
}

func TestContextRunMiddlewareRehydratesTopLevelAgentAsOwner(t *testing.T) {
	for _, operation := range []plugin.RunOperation{plugin.RunStart, plugin.RunResume, plugin.RunResolveApproval} {
		t.Run(string(operation), func(t *testing.T) {
			t.Parallel()
			store, turn, checkpoint, root := contextRunMiddlewareFixture(t)
			middleware, err := NewContextRunMiddleware(store, nil)
			if err != nil {
				t.Fatal(err)
			}

			_, err = middleware.Run(t.Context(), plugin.RunInvocation{
				Operation: operation, Kind: kernel.RunKind(ExecutionAgent), RunID: root.ExecutionRefID,
			}, func(ctx context.Context) (kernel.Snapshot, error) {
				assertContextRunHydration(t, ctx, turn.ID, ContextWindowOwner, checkpoint)
				return kernel.Snapshot{}, nil
			})
			if err != nil {
				t.Fatalf("run middleware: %v", err)
			}
		})
	}
}

func TestContextRunMiddlewareRehydratesDescendantAgentReadOnly(t *testing.T) {
	store, turn, checkpoint, root := contextRunMiddlewareFixture(t)
	const childRunID = "agent-plan-step-child"
	middleware, err := NewContextRunMiddleware(store, contextRunRelationFixture{
		childRunID: {
			ParentRunID: root.ExecutionRefID, ChildRunID: childRunID,
			Kind: runrelation.KindPlanStep, OwnerNodeID: "step-1", CreatedAt: time.Now().UTC(),
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	_, err = middleware.Run(t.Context(), plugin.RunInvocation{
		Operation: plugin.RunResume, Kind: kernel.RunKind(ExecutionAgent), RunID: childRunID,
	}, func(ctx context.Context) (kernel.Snapshot, error) {
		assertContextRunHydration(t, ctx, turn.ID, ContextWindowReadOnly, checkpoint)
		return kernel.Snapshot{}, nil
	})
	if err != nil {
		t.Fatalf("run middleware: %v", err)
	}
}

func TestContextRunMiddlewareReplacesStaleProcessCheckpointWithDurableTurnHead(t *testing.T) {
	store, turn, checkpoint, root := contextRunMiddlewareFixture(t)
	manager := runtimecontext.NewManager(runtimecontext.Dependencies{})
	messages := runtimecontext.Materialize(checkpoint.Window)
	messages = append(messages, model.Message{Role: model.RoleAssistant, Content: "durable continuation"})
	next, err := manager.Capture(t.Context(), runtimecontext.CaptureRequest{
		Previous: checkpoint, StaticFingerprint: checkpoint.StaticFingerprint,
		RunID: root.ExecutionRefID, Messages: messages,
	})
	if err != nil {
		t.Fatal(err)
	}
	turn, err = store.CommitContextCheckpoint(t.Context(), ContextCheckpointCommit{
		TurnID: turn.ID, ExpectedTurnRevision: turn.Revision,
		ExpectedTurnCheckpointID: checkpoint.ID, ExpectedHeadCheckpointID: checkpoint.ID,
		Checkpoint: next, UpdatedAt: turn.UpdatedAt.Add(time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}
	middleware, err := NewContextRunMiddleware(store, nil)
	if err != nil {
		t.Fatal(err)
	}
	stale := withContextWindowBinding(withContextCheckpoint(t.Context(), checkpoint), turn.ID, ContextWindowOwner)
	_, err = middleware.Run(stale, plugin.RunInvocation{
		Operation: plugin.RunResume, Kind: kernel.RunKind(ExecutionAgent), RunID: root.ExecutionRefID,
	}, func(ctx context.Context) (kernel.Snapshot, error) {
		assertContextRunHydration(t, ctx, turn.ID, ContextWindowOwner, next)
		return kernel.Snapshot{}, nil
	})
	if err != nil {
		t.Fatalf("run middleware: %v", err)
	}
}

func contextRunMiddlewareFixture(t *testing.T) (*MemoryStore, Turn, runtimecontext.Checkpoint, Invocation) {
	t.Helper()
	now := time.Date(2026, 8, 22, 12, 30, 0, 0, time.UTC)
	store := NewMemoryStore()
	manager := runtimecontext.NewManager(runtimecontext.Dependencies{})
	checkpoint, err := manager.Open(t.Context(), runtimecontext.OpenRequest{
		ScopeID: "session-context-resume", StaticFingerprint: runtimecontext.StaticFingerprint("stable"),
		SourcePath: []string{"message-context-resume"}, Instructions: "stable instructions",
		Entries: []runtimecontext.Entry{{
			ID: "entry-context-resume", SourceID: "message-context-resume", TurnID: "host-turn",
			Message: model.Message{Role: model.RoleUser, Content: "persist this context"},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	turn, fresh, err := store.CreateTurn(t.Context(), Turn{
		ID: "turn-context-resume", SessionID: checkpoint.ScopeID,
		HostTurn: HostRef{Kind: contextRunHostTurnKind, ID: "host-turn"}, ConfigSnapshotID: "config-context-resume",
		Status: TurnRunning, Revision: 1, CreatedAt: now, UpdatedAt: now,
	})
	if err != nil || !fresh {
		t.Fatalf("create turn fresh=%v err=%v", fresh, err)
	}
	turn, err = store.CommitContextCheckpoint(t.Context(), ContextCheckpointCommit{
		TurnID: turn.ID, ExpectedTurnRevision: turn.Revision, Checkpoint: checkpoint, UpdatedAt: now.Add(time.Second),
	})
	if err != nil {
		t.Fatalf("commit checkpoint: %v", err)
	}
	root, err := newDirectAgentInvocation(
		turn.ID, "context-resume-request", "resume root",
		kernel.ActorRef{TenantID: contextRunTestTenantID, ActorID: contextRunTestActorID},
		kernel.ThreadRef{Kind: "conversation", ID: "thread"}, nil, now.Add(2*time.Second),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = store.CreateInvocation(t.Context(), root); err != nil {
		t.Fatal(err)
	}
	return store, turn, checkpoint, root
}

func assertContextRunHydration(
	t *testing.T,
	ctx context.Context,
	turnID string,
	access ContextWindowAccess,
	checkpoint runtimecontext.Checkpoint,
) {
	t.Helper()
	binding, ok := CurrentContextWindowBinding(ctx)
	if !ok || binding.TurnID != turnID || binding.Access != access {
		t.Fatalf("binding = %#v, ok=%v", binding, ok)
	}
	got, ok := CurrentContextCheckpoint(ctx)
	if !ok || !sameContextCheckpointRef(got, contextCheckpointRef(checkpoint)) {
		t.Fatalf("checkpoint = %#v, ok=%v", got, ok)
	}
}
