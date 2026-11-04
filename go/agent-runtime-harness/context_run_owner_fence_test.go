package harness

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	runtimecontext "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/context"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/handoff"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/runrelation"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

// Independent audit: a process-context bound to one Harness Turn must not
// authorize another physical Agent Run to borrow its transcript or rights.
// The foreign run has a distinct, durable Turn/Context (separate session).
func TestIndependentContextMiddlewareRebindsForeignRunToItsOwnDurableTurn(t *testing.T) {
	store, firstTurn, firstCheckpoint, _ := contextRunMiddlewareFixture(t)
	manager := runtimecontext.NewManager(runtimecontext.Dependencies{})
	secondCheckpoint, err := manager.Open(t.Context(), runtimecontext.OpenRequest{
		ScopeID: "session-context-foreign", StaticFingerprint: runtimecontext.StaticFingerprint("foreign-static"),
		SourcePath: []string{"message-context-foreign"}, Instructions: "secret foreign session instruction",
		Entries: []runtimecontext.Entry{{ID: "entry-context-foreign", SourceID: "message-context-foreign", TurnID: "host-turn-foreign",
			Message: model.Message{Role: model.RoleUser, Content: model.TextParts("foreign private context")}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 22, 15, 30, 0, 0, time.UTC)
	foreignTurn, created, err := store.CreateTurn(t.Context(), Turn{
		ID: "turn-context-foreign", SessionID: secondCheckpoint.ScopeID,
		HostTurn:         HostRef{Kind: contextRunHostTurnKind, ID: "host-turn-foreign"},
		ConfigSnapshotID: "config-context-foreign", Status: TurnRunning, Revision: 1, CreatedAt: now, UpdatedAt: now,
	})
	if err != nil || !created {
		t.Fatalf("create foreign turn: fresh=%t err=%v", created, err)
	}
	foreignTurn, err = store.CommitContextCheckpoint(t.Context(), ContextCheckpointCommit{
		TurnID: foreignTurn.ID, ExpectedTurnRevision: foreignTurn.Revision, Checkpoint: secondCheckpoint, UpdatedAt: now.Add(time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}
	foreignInvocation, err := newAgentInvocation(foreignTurn.ID, "foreign-context-run-request", "foreign private goal",
		kernel.ActorRef{TenantID: "other-tenant", ActorID: "other-actor"},
		kernel.ThreadRef{Kind: "conversation", ID: "other-thread"}, nil, nil, now.Add(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = store.CreateInvocation(t.Context(), foreignInvocation); err != nil {
		t.Fatal(err)
	}
	middleware, err := NewContextRunMiddleware(store, nil)
	if err != nil {
		t.Fatal(err)
	}
	// This context is legitimate for firstTurn, but is NOT a grant for the
	// independent second Run. Middleware must read the foreign Run's durable
	// root Invocation instead of trusting the inherited owner binding.
	inherited := withContextWindowBinding(withContextCheckpoint(t.Context(), firstCheckpoint), firstTurn.ID, ContextWindowOwner)
	_, err = middleware.Run(inherited, plugin.RunInvocation{
		Operation: plugin.RunResume, Kind: kernel.RunKind(ExecutionAgent), RunID: foreignInvocation.ExecutionRefID,
	}, func(ctx context.Context) (kernel.Snapshot, error) {
		binding, ok := CurrentContextWindowBinding(ctx)
		if !ok || binding.TurnID != foreignTurn.ID || binding.Access != ContextWindowOwner {
			t.Errorf("FOREIGN CONTEXT OWNER BORROWED: actual=%+v ok=%v want Turn=%s", binding, ok, foreignTurn.ID)
		}
		checkpoint, ok := CurrentContextCheckpoint(ctx)
		if !ok || checkpoint.ID != secondCheckpoint.ID {
			t.Errorf("FOREIGN TRANSCRIPT EXPOSED: got=%s ok=%v want=%s", checkpoint.ID, ok, secondCheckpoint.ID)
		}
		return kernel.Snapshot{}, nil
	})
	if err != nil {
		t.Fatalf("Run foreign independent: %v", err)
	}
	// An unbound (but still nonempty) stale Checkpoint is also not the
	// foreign Run's authority. Rehydrate from the durable foreign Turn.
	unbound := withContextCheckpoint(t.Context(), firstCheckpoint)
	_, err = middleware.Run(unbound, plugin.RunInvocation{
		Operation: plugin.RunResume, Kind: kernel.RunKind(ExecutionAgent), RunID: foreignInvocation.ExecutionRefID,
	}, func(ctx context.Context) (kernel.Snapshot, error) {
		assertContextRunHydration(t, ctx, foreignTurn.ID, ContextWindowOwner, secondCheckpoint)
		return kernel.Snapshot{}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// A descendant Runtime Run inherits only read-only Context authority,
// even when invoked with a process Context still carrying root owner rights.
func TestContextRunMiddlewareDowngradesInheritedOwnerForDescendant(t *testing.T) {
	store, turn, checkpoint, root := contextRunMiddlewareFixture(t)
	const childRunID = "child-with-inherited-owner"
	middleware, err := NewContextRunMiddleware(store, contextRunRelationFixture{
		childRunID: {ParentRunID: root.ExecutionRefID, ChildRunID: childRunID,
			Kind: runrelation.KindCapability, OwnerNodeID: "child-inherited", CreatedAt: time.Now().UTC()},
	})
	if err != nil {
		t.Fatal(err)
	}
	inherited := withContextWindowBinding(withContextCheckpoint(t.Context(), checkpoint), turn.ID, ContextWindowOwner)
	_, err = middleware.Run(inherited, plugin.RunInvocation{Operation: plugin.RunResume, Kind: kernel.RunKind(ExecutionAgent), RunID: childRunID}, func(ctx context.Context) (kernel.Snapshot, error) {
		binding, ok := CurrentContextWindowBinding(ctx)
		if !ok || binding.TurnID != turn.ID || binding.Access != ContextWindowReadOnly {
			t.Errorf("DESCENDANT INHERITED OWNER PRIVILEGES: binding=%+v ok=%v", binding, ok)
		}
		got, ok := CurrentContextCheckpoint(ctx)
		if !ok || got.ID != checkpoint.ID {
			t.Errorf("read-only descendant lost valid checkpoint: got=%s ok=%v want=%s", got.ID, ok, checkpoint.ID)
		}
		return kernel.Snapshot{}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// A physical Run without a verifiable Harness owner may still execute as a
// standalone Agent. It must never borrow a different Turn's inherited
// Context Window transcript/owner authority.
func TestContextRunMiddlewareUnresolvedRunDropsInheritedContext(t *testing.T) {
	store, turn, checkpoint, _ := contextRunMiddlewareFixture(t)
	middleware, err := NewContextRunMiddleware(store, nil)
	if err != nil {
		t.Fatal(err)
	}
	inherited := withContextWindowBinding(withContextCheckpoint(t.Context(), checkpoint), turn.ID, ContextWindowOwner)
	_, err = middleware.Run(inherited, plugin.RunInvocation{Operation: plugin.RunResume, Kind: kernel.RunKind(ExecutionAgent), RunID: "unrelated-standalone-agent"}, func(ctx context.Context) (kernel.Snapshot, error) {
		binding, hasBinding := CurrentContextWindowBinding(ctx)
		got, hasCheckpoint := CurrentContextCheckpoint(ctx)
		if hasBinding || hasCheckpoint {
			t.Errorf("UNRESOLVED RUN BORROWED FOREIGN CONTEXT: binding=%+v bound=%t checkpoint=%s active=%t", binding, hasBinding, got.ID, hasCheckpoint)
		}
		return kernel.Snapshot{}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// A same-Run caller can intentionally narrow access to read-only. Durable
// ownership must not silently grant stronger permissions than that context.
func TestContextRunMiddlewareRetainsExplicitReadOnlyOnSameRun(t *testing.T) {
	store, turn, checkpoint, root := contextRunMiddlewareFixture(t)
	middleware, err := NewContextRunMiddleware(store, nil)
	if err != nil {
		t.Fatal(err)
	}
	readOnly := withContextWindowBinding(withContextCheckpoint(t.Context(), checkpoint), turn.ID, ContextWindowReadOnly)
	_, err = middleware.Run(readOnly, plugin.RunInvocation{Operation: plugin.RunResume, Kind: kernel.RunKind(ExecutionAgent), RunID: root.ExecutionRefID}, func(ctx context.Context) (kernel.Snapshot, error) {
		binding, ok := CurrentContextWindowBinding(ctx)
		if !ok || binding.TurnID != turn.ID || binding.Access != ContextWindowReadOnly {
			t.Errorf("explicit read-only binding was upgraded: %+v ok=%v", binding, ok)
		}
		return kernel.Snapshot{}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// A Handoff specialist is excluded from supervisor Context even if the
// physical Start/Resume is invoked with its parent's process-local binding.
func TestContextRunMiddlewareHandoffIgnoresInheritedSupervisorContext(t *testing.T) {
	store, turn, checkpoint, root := contextRunMiddlewareFixture(t)
	delegation := handoff.Delegation{
		ID: "specialist-delegation", MemberID: "specialist", RoleID: "specialist",
		ChildRunID: "specialist-inherited", Goal: "specialized task", Status: handoff.StatusQueued,
	}
	input, hash, err := marshalInvocationValue(delegation)
	if err != nil {
		t.Fatal(err)
	}
	input, hash, err = sealChildRootInput(input, hash, root)
	if err != nil {
		t.Fatal(err)
	}
	id, err := InvocationID(turn.ID, "delegated-parent", CapabilityAgent, delegation.ID)
	if err != nil {
		t.Fatal(err)
	}
	child := root
	child.ID, child.ExecutionRefID, child.ParentItemID, child.Input, child.InputHash = id, delegation.ChildRunID, "delegated-parent", input, hash
	if _, _, err = store.CreateInvocation(t.Context(), child); err != nil {
		t.Fatal(err)
	}
	middleware, err := NewContextRunMiddleware(store, nil)
	if err != nil {
		t.Fatal(err)
	}
	inherited := withContextWindowBinding(withContextCheckpoint(t.Context(), checkpoint), turn.ID, ContextWindowOwner)
	_, err = middleware.Run(inherited, plugin.RunInvocation{Operation: plugin.RunResume, Kind: kernel.RunKind(ExecutionAgent), RunID: child.ExecutionRefID}, func(ctx context.Context) (kernel.Snapshot, error) {
		binding, hasBinding := CurrentContextWindowBinding(ctx)
		got, hasCheckpoint := CurrentContextCheckpoint(ctx)
		if hasBinding || hasCheckpoint {
			t.Errorf("SPECIALIST LEAKED SUPERVISOR CONTEXT: binding=%+v bound=%t checkpoint=%s active=%t", binding, hasBinding, got.ID, hasCheckpoint)
		}
		return kernel.Snapshot{}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// A bare stale checkpoint without an execution binding is still process
// context, not evidence that an unrelated standalone physical Run owns it.
func TestContextRunMiddlewareUnboundCheckpointCannotLeakWithoutDurableRun(t *testing.T) {
	store, _, checkpoint, _ := contextRunMiddlewareFixture(t)
	middleware, err := NewContextRunMiddleware(store, nil)
	if err != nil {
		t.Fatal(err)
	}
	inherited := withContextCheckpoint(t.Context(), checkpoint)
	for _, runID := range []string{"unrelated-standalone-agent", ""} {
		t.Run(runID, func(t *testing.T) {
			_, err = middleware.Run(inherited, plugin.RunInvocation{Operation: plugin.RunResume, Kind: kernel.RunKind(ExecutionAgent), RunID: runID}, func(ctx context.Context) (kernel.Snapshot, error) {
				if b, ok := CurrentContextWindowBinding(ctx); ok {
					t.Errorf("unowned binding leaked: %+v", b)
				}
				if cp, ok := CurrentContextCheckpoint(ctx); ok {
					t.Errorf("unowned checkpoint leaked: %+v", cp.ID)
				}
				return kernel.Snapshot{}, nil
			})
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

// Independent audit: a delegated specialist's child is inside the same
// isolated subtree. Looking only at the leaf RunID fails to notice that
// its parent already excluded the supervisor's Context Window.
func TestIndependentContextRunHandoffDescendantsNeverInheritSupervisorContext(t *testing.T) {
	for _, inherited := range []bool{false, true} {
		testName := "fresh_context"
		if inherited {
			testName = "inherited_supervisor_context"
		}
		t.Run(testName, func(t *testing.T) {
			store, turn, checkpoint, root := contextRunMiddlewareFixture(t)
			delegation := handoff.Delegation{
				ID: "isolated-delegation", MemberID: "isolated-specialist", RoleID: "specialist",
				ChildRunID: "specialist-root-run", Goal: "independent task", Status: handoff.StatusQueued,
			}
			input, inputHash, err := marshalInvocationValue(delegation)
			if err != nil {
				t.Fatal(err)
			}
			input, inputHash, err = sealChildRootInput(input, inputHash, root)
			if err != nil {
				t.Fatal(err)
			}
			id, err := InvocationID(turn.ID, "specialist-parent-item", CapabilityAgent, delegation.ID)
			if err != nil {
				t.Fatal(err)
			}
			specialist := root
			specialist.ID, specialist.ExecutionRefID, specialist.ParentItemID, specialist.Input, specialist.InputHash = id, delegation.ChildRunID, "specialist-parent-item", input, inputHash
			if _, _, err = store.CreateInvocation(t.Context(), specialist); err != nil {
				t.Fatal(err)
			}
			// A nested Agent/Capability Run spawned by the Specialist. It is not
			// indexed as an independent Harness Invocation: relation traversal is
			// how the middleware determines its read-only context inheritance.
			descendantID := "specialist-inner-agent"
			relations := contextRunRelationFixture{
				descendantID: runrelation.Relation{ParentRunID: specialist.ExecutionRefID, ChildRunID: descendantID,
					Kind: runrelation.KindCapability, OwnerNodeID: "specialist-child-step", CreatedAt: time.Now().UTC()},
			}
			middleware, err := NewContextRunMiddleware(store, relations)
			if err != nil {
				t.Fatal(err)
			}
			ctx := t.Context()
			if inherited {
				ctx = withContextWindowBinding(withContextCheckpoint(ctx, checkpoint), turn.ID, ContextWindowOwner)
			}
			_, err = middleware.Run(ctx, plugin.RunInvocation{Operation: plugin.RunResume, Kind: kernel.RunKind(ExecutionAgent), RunID: descendantID}, func(got context.Context) (kernel.Snapshot, error) {
				binding, hasBinding := CurrentContextWindowBinding(got)
				window, hasWindow := CurrentContextCheckpoint(got)
				if hasBinding || hasWindow {
					t.Errorf("SPECIALIST DESCENDANT LEAKED SUPERVISOR CONTEXT: bound=%t binding=%+v checkpoint=%s active=%t", hasBinding, binding, window.ID, hasWindow)
				}
				return kernel.Snapshot{}, nil
			})
			if err != nil {
				t.Fatalf("specialist descendant hydrate: %v", err)
			}
		})
	}
}

// The relation lookup is an ownership input, not permission to accept a
// relation with a foreign ChildRunID. A wrong child reference cannot cause
// the current Run to materialize an unrelated ancestor's Context.
func TestIndependentContextRunRejectsRelationWhoseChildDoesNotMatchLookup(t *testing.T) {
	store, turn, checkpoint, root := contextRunMiddlewareFixture(t)
	const unrelated = "unrelated-run-id"
	middleware, err := NewContextRunMiddleware(store, contextRunRelationFixture{
		unrelated: runrelation.Relation{ParentRunID: root.ExecutionRefID, ChildRunID: "OTHER-RUN-ID",
			Kind: runrelation.KindCapability, OwnerNodeID: "nonmatching-owner", CreatedAt: time.Now().UTC()},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = middleware.Run(t.Context(), plugin.RunInvocation{Operation: plugin.RunResume, Kind: kernel.RunKind(ExecutionAgent), RunID: unrelated}, func(ctx context.Context) (kernel.Snapshot, error) {
		if binding, ok := CurrentContextWindowBinding(ctx); ok {
			t.Errorf("INVALID RELATION ADMITTED FOREIGN CONTEXT: Turn=%s access=%s expected none", binding.TurnID, binding.Access)
		}
		if got, ok := CurrentContextCheckpoint(ctx); ok {
			t.Errorf("INVALID RELATION LOADED FOREIGN CHECKPOINT: got=%s foreign=%s from Turn=%s", got.ID, checkpoint.ID, turn.ID)
		}
		return kernel.Snapshot{}, nil
	})
	if err == nil {
		// Unknown-run-without-context is a valid fail-closed outcome; a real
		// contradictory durable relation should preferably return ErrConflict.
		return
	}
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("unexpected invalid relation error: %v", err)
	}
}

type auditOnceFailedContextLookup struct {
	Store
	reads int
}

func (store *auditOnceFailedContextLookup) GetInvocationByExecutionRefID(ctx context.Context, runID string) (Invocation, error) {
	store.reads++
	if store.reads == 1 {
		return Invocation{}, errors.New("transient owner-index lookup failure")
	}
	return store.Store.GetInvocationByExecutionRefID(ctx, runID)
}

// The direct Specialist exclusion read can fail once while a repeat in the
// resolver succeeds. A transient owner-lookup error may not turn an isolated
// Specialist into an ordinary read-only child of the Supervisor.
func TestIndependentHandoffFirstOwnershipReadErrorMustNotExposeSupervisor(t *testing.T) {
	store, turn, checkpoint, root := contextRunMiddlewareFixture(t)
	delegation := handoff.Delegation{ID: "lookup-failure-handoff", MemberID: "specialist", RoleID: "specialist",
		ChildRunID: "handoff-lookup-fail", Goal: "specialized task", Status: handoff.StatusQueued}
	input, hash, err := marshalInvocationValue(delegation)
	if err != nil {
		t.Fatal(err)
	}
	input, hash, err = sealChildRootInput(input, hash, root)
	if err != nil {
		t.Fatal(err)
	}
	id, err := InvocationID(turn.ID, "lookup-failure-parent", CapabilityAgent, delegation.ID)
	if err != nil {
		t.Fatal(err)
	}
	child := root
	child.ID, child.ExecutionRefID, child.ParentItemID, child.Input, child.InputHash = id, delegation.ChildRunID, "lookup-failure-parent", input, hash
	if _, _, err = store.CreateInvocation(t.Context(), child); err != nil {
		t.Fatal(err)
	}
	unreliable := &auditOnceFailedContextLookup{Store: store}
	middleware, err := NewContextRunMiddleware(unreliable, nil)
	if err != nil {
		t.Fatal(err)
	}
	inherited := withContextWindowBinding(withContextCheckpoint(t.Context(), checkpoint), turn.ID, ContextWindowOwner)
	_, err = middleware.Run(inherited, plugin.RunInvocation{Operation: plugin.RunResume, Kind: kernel.RunKind(ExecutionAgent), RunID: child.ExecutionRefID}, func(ctx context.Context) (kernel.Snapshot, error) {
		if binding, ok := CurrentContextWindowBinding(ctx); ok {
			t.Errorf("TRANSIENT OWNER LOOKUP FAILURE EXPOSED SUPERVISOR TURN: %+v", binding)
		}
		if cp, ok := CurrentContextCheckpoint(ctx); ok {
			t.Errorf("TRANSIENT OWNER LOOKUP FAILURE EXPOSED SUPERVISOR WINDOW: %s", cp.ID)
		}
		return kernel.Snapshot{}, nil
	})
	if err == nil || !strings.Contains(err.Error(), "transient owner-index lookup failure") {
		t.Errorf("transient ownership uncertainty must fail closed, got err=%v", err)
	}
}

// Invalid physical child-index records must never elect another Run's
// Context owner, even if they happen to point to an existing root Turn.
func TestContextRunRelationRejectsMalformedOwnerLinks(t *testing.T) {
	for _, tc := range []struct{ name, child, parent string }{
		{name: "wrong_child", child: "different-run", parent: "root"},
		{name: "empty_child", child: "", parent: "root"},
		{name: "self_parent", child: "foreign-run", parent: "foreign-run"},
		{name: "blank_parent", child: "foreign-run", parent: ""},
		{name: "padded_parent", child: "foreign-run", parent: " root "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, _, _, root := contextRunMiddlewareFixture(t)
			parent := tc.parent
			if parent == "root" {
				parent = root.ExecutionRefID
			}
			middleware, err := NewContextRunMiddleware(store, contextRunRelationFixture{
				"foreign-run": {ChildRunID: tc.child, ParentRunID: parent,
					Kind: runrelation.KindCapability, OwnerNodeID: "wrong-index", CreatedAt: time.Now().UTC()},
			})
			if err != nil {
				t.Fatal(err)
			}
			called := false
			_, err = middleware.Run(t.Context(), plugin.RunInvocation{Operation: plugin.RunResume, Kind: kernel.RunKind(ExecutionAgent), RunID: "foreign-run"}, func(ctx context.Context) (kernel.Snapshot, error) {
				called = true
				return kernel.Snapshot{}, nil
			})
			if !errors.Is(err, ErrConflict) || called {
				t.Fatalf("invalid relation granted Context: err=%v next=%t", err, called)
			}
		})
	}
}

func TestContextRunRelationCycleFailsClosed(t *testing.T) {
	store, _, _, _ := contextRunMiddlewareFixture(t)
	relations := contextRunRelationFixture{
		"a": {ParentRunID: "b", ChildRunID: "a", Kind: runrelation.KindCapability, OwnerNodeID: "owner-a", CreatedAt: time.Now().UTC()},
		"b": {ParentRunID: "a", ChildRunID: "b", Kind: runrelation.KindCapability, OwnerNodeID: "owner-b", CreatedAt: time.Now().UTC()},
	}
	middleware, err := NewContextRunMiddleware(store, relations)
	if err != nil {
		t.Fatal(err)
	}
	called := false
	_, err = middleware.Run(t.Context(), plugin.RunInvocation{Operation: plugin.RunResume, Kind: kernel.RunKind(ExecutionAgent), RunID: "a"}, func(context.Context) (kernel.Snapshot, error) {
		called = true
		return kernel.Snapshot{}, nil
	})
	if !errors.Is(err, ErrConflict) || called {
		t.Fatalf("cyclic ownership granted context: err=%v next=%t", err, called)
	}
}

type auditRelationAppearsOnSecondRead struct {
	childID  string
	parentID string
	lookups  int
}

func (s *auditRelationAppearsOnSecondRead) GetByChild(_ context.Context, childID string) (runrelation.Relation, error) {
	if childID != s.childID {
		return runrelation.Relation{}, runrelation.ErrNotFound
	}
	s.lookups++
	if s.lookups == 1 {
		return runrelation.Relation{}, runrelation.ErrNotFound
	}
	return runrelation.Relation{ChildRunID: s.childID, ParentRunID: s.parentID,
		Kind: runrelation.KindCapability, OwnerNodeID: "specialist-grandchild", CreatedAt: time.Now().UTC()}, nil
}

// Independent TOCTOU audit. The two reads of the same relation key can
// differ when an immutable relation is created between lookups. The run's
// Handoff isolation cannot disappear just because the first ancestry scan
// temporarily reported no relation before a later owner scan found one.
func TestIndependentContextRunFirstMissingThenPresentHandoffRelationNeverExposesSupervisor(t *testing.T) {
	for _, inherit := range []bool{false, true} {
		mode := "fresh"
		if inherit {
			mode = "inherited_supervisor"
		}
		t.Run(mode, func(t *testing.T) {
			store, turn, checkpoint, root := contextRunMiddlewareFixture(t)
			delegation := handoff.Delegation{ID: "late-relation-delegation", MemberID: "specialist", RoleID: "specialist",
				ChildRunID: "late-specialist", Goal: "isolated task", Status: handoff.StatusQueued}
			input, hash, err := marshalInvocationValue(delegation)
			if err != nil {
				t.Fatal(err)
			}
			input, hash, err = sealChildRootInput(input, hash, root)
			if err != nil {
				t.Fatal(err)
			}
			id, err := InvocationID(turn.ID, "late-relation-parent", CapabilityAgent, delegation.ID)
			if err != nil {
				t.Fatal(err)
			}
			specialist := root
			specialist.ID, specialist.ExecutionRefID, specialist.ParentItemID, specialist.Input, specialist.InputHash = id, delegation.ChildRunID, "late-relation-parent", input, hash
			if _, _, err = store.CreateInvocation(t.Context(), specialist); err != nil {
				t.Fatal(err)
			}
			const descendant = "late-relation-grandchild"
			relationSource := &auditRelationAppearsOnSecondRead{childID: descendant, parentID: specialist.ExecutionRefID}
			middleware, err := NewContextRunMiddleware(store, relationSource)
			if err != nil {
				t.Fatal(err)
			}
			ctx := t.Context()
			if inherit {
				ctx = withContextWindowBinding(withContextCheckpoint(ctx, checkpoint), turn.ID, ContextWindowOwner)
			}
			invoked := false
			_, err = middleware.Run(ctx, plugin.RunInvocation{Operation: plugin.RunResume, Kind: kernel.RunKind(ExecutionAgent), RunID: descendant}, func(got context.Context) (kernel.Snapshot, error) {
				invoked = true
				if binding, ok := CurrentContextWindowBinding(got); ok {
					t.Errorf("TOCTOU ALLOWED SUPERVISOR TURN: %+v", binding)
				}
				if cp, ok := CurrentContextCheckpoint(got); ok {
					t.Errorf("TOCTOU ALLOWED SUPERVISOR CHECKPOINT: %s", cp.ID)
				}
				return kernel.Snapshot{}, nil
			})
			if err != nil || !invoked || relationSource.lookups != 1 {
				t.Fatalf("single-scan Context proof violated: err=%v invoked=%t relationLookups=%d", err, invoked, relationSource.lookups)
			}
		})
	}
}

// Independent audit of the actual read-only Tool entry: the middleware's
// excluded specialist Run must not be able to retrieve exact archived
// Supervisor message bytes through a separately registered local Tool.
func TestIndependentHandoffArtifactReadNeverLeaksSupervisorArchive(t *testing.T) {
	store, turn, checkpoint, root := contextRunMiddlewareFixture(t)
	const secret = "SUPERVISOR_ARCHIVED_PRIVATE_CONTENT_2026"
	artifact, err := runtimecontext.NewArtifact(runtimecontext.ArtifactCompaction,
		turn.SessionID, checkpoint.Generation, checkpoint.CoveredThroughSourceID, secret, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = store.PutContextArtifact(t.Context(), artifact); err != nil {
		t.Fatal(err)
	}
	delegation := handoff.Delegation{ID: "archive-delegation", MemberID: "specialist", RoleID: "specialist",
		ChildRunID: "archive-specialist", Goal: "isolated work", Status: handoff.StatusQueued}
	input, hash, err := marshalInvocationValue(delegation)
	if err != nil {
		t.Fatal(err)
	}
	input, hash, err = sealChildRootInput(input, hash, root)
	if err != nil {
		t.Fatal(err)
	}
	id, err := InvocationID(turn.ID, "archive-parent-item", CapabilityAgent, delegation.ID)
	if err != nil {
		t.Fatal(err)
	}
	child := root
	child.ID, child.ExecutionRefID, child.ParentItemID, child.Input, child.InputHash = id, delegation.ChildRunID, "archive-parent-item", input, hash
	if _, _, err = store.CreateInvocation(t.Context(), child); err != nil {
		t.Fatal(err)
	}
	const descendantID = "specialist-archive-grandchild"
	relations := contextRunRelationFixture{descendantID: {ParentRunID: child.ExecutionRefID, ChildRunID: descendantID,
		Kind: runrelation.KindCapability, OwnerNodeID: "descendant", CreatedAt: time.Now().UTC()}}
	handler := NewContextArtifactToolHandler(store, relations)
	for _, runID := range []string{child.ExecutionRefID, descendantID} {
		t.Run(runID, func(t *testing.T) {
			result, err := handler.Execute(t.Context(), tools.ExecutionRequest{RunID: runID,
				Call: tools.Call{ID: "read-private-archive", ToolKey: ContextArtifactToolKey,
					Arguments: mustContextArtifactArguments(t, artifact.ID, 0, contextArtifactMaxLimit)}})
			if !errors.Is(err, ErrConflict) || len(result.Content) != 0 ||
				strings.Contains(string(result.Content), secret) || json.Valid(result.Content) {
				t.Errorf("ISOLATED SPECIALIST READ SUPERVISOR ARCHIVE OR RETURNED WRONG ERROR: run=%s artifact=%s payload=%s err=%v", runID, artifact.ID, string(result.Content), err)
			}
		})
	}
}

// Independent integrity audit: the same-run fast path must not accept a
// process-local Checkpoint whose identity metadata matches the durable
// Turn but whose message body was changed without updating its hash.
func TestIndependentContextRunSameTurnTamperedCheckpointPayloadCannotBypassDurableLoad(t *testing.T) {
	store, turn, checkpoint, root := contextRunMiddlewareFixture(t)
	altered := runtimecontext.CloneCheckpoint(checkpoint)
	changed := false
	for index := range altered.Window.Entries {
		if altered.Window.Entries[index].Message.Role == model.RoleUser {
			altered.Window.Entries[index].Message.Content = model.TextParts("INJECTED FALSE CONTEXT FROM PROCESS MEMORY")
			changed = true
			break
		}
	}
	if !changed {
		t.Fatal("fixture contains no editable user message")
	}
	if runtimecontext.ValidCheckpoint(altered) {
		t.Fatal("altered checkpoint unexpectedly has valid hash")
	}
	if !sameContextCheckpointRef(altered, turn.ContextRef) {
		t.Fatal("expected altered checkpoint to preserve metadata identity for audit")
	}
	middleware, err := NewContextRunMiddleware(store, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, access := range []ContextWindowAccess{ContextWindowOwner, ContextWindowReadOnly} {
		t.Run(string(access), func(t *testing.T) {
			injected := withContextWindowBinding(withContextCheckpoint(t.Context(), altered), turn.ID, access)
			_, err = middleware.Run(injected, plugin.RunInvocation{Operation: plugin.RunResume, Kind: kernel.RunKind(ExecutionAgent), RunID: root.ExecutionRefID}, func(ctx context.Context) (kernel.Snapshot, error) {
				materialized, ok := CurrentContextCheckpoint(ctx)
				if !ok {
					t.Fatal("trusted Turn Context was unexpectedly discarded")
				}
				if !runtimecontext.ValidCheckpoint(materialized) {
					t.Errorf("INVALID LOCAL CHECKPOINT PAYLOAD WAS ACCEPTED: access=%s id=%s", access, materialized.ID)
				}
				if model.TextValue(materialized.Window.Entries[0].Message.Content) != model.TextValue(checkpoint.Window.Entries[0].Message.Content) {
					t.Errorf("UNTRUSTED PROCESS DATA REPLACED DURABLE TRANSCRIPT: access=%s", access)
				}
				return kernel.Snapshot{}, nil
			})
			if err != nil {
				t.Fatalf("hydrate run: %v", err)
			}
		})
	}
}

type auditCorruptHandoffInvocationReadStore struct {
	Store
	target string
}

func (f auditCorruptHandoffInvocationReadStore) GetInvocationByExecutionRefID(ctx context.Context, id string) (Invocation, error) {
	got, err := f.Store.GetInvocationByExecutionRefID(ctx, id)
	if err == nil && id == f.target {
		got.InputHash = "checksum-does-not-match-stored-input"
	}
	return got, err
}

// An indexed Invocation is an authority input. A persisted Handoff
// containing a mismatched frozen InputHash is not proof that a descendant
// may inherit its Supervisor history as an ordinary read-only child.
func TestIndependentContextRunInvalidHandoffInvocationInputHashMustFailClosed(t *testing.T) {
	store, turn, checkpoint, root := contextRunMiddlewareFixture(t)
	delegation := handoff.Delegation{ID: "corrupt-handoff", MemberID: "specialist", RoleID: "specialist",
		ChildRunID: "corrupt-specialist", Goal: "independent task", Status: handoff.StatusQueued}
	input, hash, err := marshalInvocationValue(delegation)
	if err != nil {
		t.Fatal(err)
	}
	input, hash, err = sealChildRootInput(input, hash, root)
	if err != nil {
		t.Fatal(err)
	}
	id, err := InvocationID(turn.ID, "corrupt-parent-item", CapabilityAgent, delegation.ID)
	if err != nil {
		t.Fatal(err)
	}
	child := root
	child.ID, child.ExecutionRefID, child.ParentItemID, child.Input, child.InputHash = id, delegation.ChildRunID, "corrupt-parent-item", input, hash
	if _, _, err = store.CreateInvocation(t.Context(), child); err != nil {
		t.Fatal(err)
	}
	wrapper := auditCorruptHandoffInvocationReadStore{Store: store, target: child.ExecutionRefID}
	corrupt, err := wrapper.GetInvocationByExecutionRefID(t.Context(), child.ExecutionRefID)
	if err != nil || validInvocation(corrupt) {
		t.Fatalf("invalid Handoff was not corrupted as intended: valid=%v err=%v", validInvocation(corrupt), err)
	}
	middleware, err := NewContextRunMiddleware(wrapper, nil)
	if err != nil {
		t.Fatal(err)
	}
	inherited := withContextWindowBinding(withContextCheckpoint(t.Context(), checkpoint), turn.ID, ContextWindowOwner)
	invoked := false
	_, err = middleware.Run(inherited, plugin.RunInvocation{Operation: plugin.RunResume, Kind: kernel.RunKind(ExecutionAgent), RunID: child.ExecutionRefID}, func(ctx context.Context) (kernel.Snapshot, error) {
		invoked = true
		if binding, has := CurrentContextWindowBinding(ctx); has {
			t.Errorf("CORRUPT HANDOFF CANNOT GRANT SUPERVISOR CONTEXT: %+v", binding)
		}
		if check, has := CurrentContextCheckpoint(ctx); has {
			t.Errorf("CORRUPT HANDOFF EXPOSED SUPERVISOR CHECKPOINT: %s", check.ID)
		}
		return kernel.Snapshot{}, nil
	})
	if !errors.Is(err, ErrConflict) || invoked {
		t.Errorf("invalid Handoff must fail closed before executing next: err=%v invoked=%t", err, invoked)
	}
}

// An adapter must not return a Checkpoint with matching durable identity but
// invalid transcript bytes; the model boundary must independently reject it.
type auditDurableCheckpointReadStore struct{ Store }

func (s auditDurableCheckpointReadStore) GetContextCheckpoint(ctx context.Context, id string) (runtimecontext.Checkpoint, error) {
	cp, err := s.Store.GetContextCheckpoint(ctx, id)
	if err == nil && len(cp.Window.Entries) > 0 {
		cp.Window.Entries[0].Message.Content = model.TextParts("ATTACKER_MODIFIED_DURABLE_TRANSCRIPT")
	}
	return cp, err
}
func TestIndependentAuditDurableCheckpointCorruptionMustFailClosed(t *testing.T) {
	store, _, checkpoint, root := contextRunMiddlewareFixture(t)
	corrupted := auditDurableCheckpointReadStore{Store: store}
	fetched, err := corrupted.GetContextCheckpoint(t.Context(), checkpoint.ID)
	if err != nil || runtimecontext.ValidCheckpoint(fetched) {
		t.Fatalf("audit fixture failed: err=%v valid=%v", err, runtimecontext.ValidCheckpoint(fetched))
	}
	if !sameContextCheckpointRef(fetched, contextCheckpointRef(checkpoint)) {
		t.Fatal("audit fixture metadata not matching")
	}
	middleware, err := NewContextRunMiddleware(corrupted, nil)
	if err != nil {
		t.Fatal(err)
	}
	invoked := false
	_, err = middleware.Run(t.Context(), plugin.RunInvocation{Operation: plugin.RunResume, Kind: kernel.RunKind(ExecutionAgent), RunID: root.ExecutionRefID}, func(ctx context.Context) (kernel.Snapshot, error) {
		invoked = true
		got, ok := CurrentContextCheckpoint(ctx)
		if ok && !runtimecontext.ValidCheckpoint(got) {
			t.Errorf("INVALID DURABLE CHECKPOINT ACCEPTED: content=%q", model.TextValue(got.Window.Entries[0].Message.Content))
		}
		return kernel.Snapshot{}, nil
	})
	if invoked || !errors.Is(err, ErrConflict) {
		t.Errorf("checkpoint integrity gap: dispatched=%t error=%v, should fail closed", invoked, err)
	}
}

// A damaged or inconsistent Store read can drop a Handoff Input while
// preserving its other identity fields. Neither a leftover InputHash nor
// both fields becoming empty may downgrade the child to ordinary ownership.
type auditMissingHandoffInputStore struct {
	Store
	id        string
	clearHash bool
}

func (s auditMissingHandoffInputStore) GetInvocationByExecutionRefID(ctx context.Context, id string) (Invocation, error) {
	got, err := s.Store.GetInvocationByExecutionRefID(ctx, id)
	if err == nil && id == s.id {
		got.Input = nil
		if s.clearHash {
			got.InputHash = ""
		}
	}
	return got, err
}

func TestIndependentAuditMissingHandoffInputMustNotBecomeOrdinaryChild(t *testing.T) {
	store, turn, checkpoint, root := contextRunMiddlewareFixture(t)
	delegation := handoff.Delegation{ID: "audit-missing-input", MemberID: "specialist", RoleID: "specialist", ChildRunID: "audit-specialist-run", Goal: "isolated task", Status: handoff.StatusQueued}
	input, hash, err := marshalInvocationValue(delegation)
	if err != nil {
		t.Fatal(err)
	}
	input, hash, err = sealChildRootInput(input, hash, root)
	if err != nil {
		t.Fatal(err)
	}
	id, err := InvocationID(turn.ID, "audit-parent-item", CapabilityAgent, delegation.ID)
	if err != nil {
		t.Fatal(err)
	}
	child := root
	child.ID, child.ExecutionRefID, child.ParentItemID, child.Input, child.InputHash = id, delegation.ChildRunID, "audit-parent-item", input, hash
	if _, _, err = store.CreateInvocation(t.Context(), child); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name      string
		clearHash bool
	}{
		{name: "input_erased_hash_retained"},
		{name: "input_and_hash_erased", clearHash: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			corrupted := auditMissingHandoffInputStore{Store: store, id: child.ExecutionRefID, clearHash: tc.clearHash}
			received, readErr := corrupted.GetInvocationByExecutionRefID(t.Context(), child.ExecutionRefID)
			if readErr != nil || isHandoffDelegationInvocation(received) || validInvocation(received) {
				t.Fatalf("expected invalid and unrecognizable Handoff: readErr=%v valid=%v", readErr, validInvocation(received))
			}
			middleware, makeErr := NewContextRunMiddleware(corrupted, nil)
			if makeErr != nil {
				t.Fatal(makeErr)
			}
			inherited := withContextWindowBinding(withContextCheckpoint(t.Context(), checkpoint), turn.ID, ContextWindowOwner)
			invoked := false
			_, runErr := middleware.Run(inherited, plugin.RunInvocation{Operation: plugin.RunResume, Kind: kernel.RunKind(ExecutionAgent), RunID: child.ExecutionRefID}, func(ctx context.Context) (kernel.Snapshot, error) {
				invoked = true
				if binding, has := CurrentContextWindowBinding(ctx); has {
					t.Errorf("invalid Handoff exposed Supervisor Context: %+v", binding)
				}
				if cp, has := CurrentContextCheckpoint(ctx); has {
					t.Errorf("invalid Handoff exposed Supervisor Checkpoint: %s", cp.ID)
				}
				return kernel.Snapshot{}, nil
			})
			if invoked || !errors.Is(runErr, ErrConflict) {
				t.Errorf("invalid Handoff did not fail closed: invoked=%v err=%v", invoked, runErr)
			}
		})
	}
}

// Read-side authorization must enforce the Registry's immutable relation
// contract, including fields not needed to walk the ancestry graph.
func TestIndependentAuditMalformedRelationMetadataCannotGrantContext(t *testing.T) {
	store, turn, checkpoint, root := contextRunMiddlewareFixture(t)
	const childID = "unrelated-malformed-relation"
	base := runrelation.Relation{ParentRunID: root.ExecutionRefID, ChildRunID: childID, Kind: runrelation.KindCapability, OwnerNodeID: "owner", CreatedAt: time.Now().UTC()}
	cases := []struct {
		name   string
		mutate func(*runrelation.Relation)
	}{
		{name: "unknown_kind", mutate: func(r *runrelation.Relation) { r.Kind = runrelation.Kind("not_a_kind") }},
		{name: "empty_owner", mutate: func(r *runrelation.Relation) { r.OwnerNodeID = "" }},
		{name: "zero_timestamp", mutate: func(r *runrelation.Relation) { r.CreatedAt = time.Time{} }},
		{name: "padded_kind", mutate: func(r *runrelation.Relation) { r.Kind = " " + r.Kind + " " }},
		{name: "padded_owner", mutate: func(r *runrelation.Relation) { r.OwnerNodeID = " owner " }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			malformed := base
			tc.mutate(&malformed)
			canonical, normalizeErr := runrelation.Prepare(malformed)
			if normalizeErr == nil && canonical.Kind == malformed.Kind && canonical.OwnerNodeID == malformed.OwnerNodeID {
				t.Fatal("audit fixture was not rejected or normalized by the Registry contract")
			}
			middleware, err := NewContextRunMiddleware(store, contextRunRelationFixture{childID: malformed})
			if err != nil {
				t.Fatal(err)
			}
			invoked := false
			_, err = middleware.Run(t.Context(), plugin.RunInvocation{Operation: plugin.RunResume, Kind: kernel.RunKind(ExecutionAgent), RunID: childID}, func(ctx context.Context) (kernel.Snapshot, error) {
				invoked = true
				if b, ok := CurrentContextWindowBinding(ctx); ok {
					t.Errorf("invalid relation authorized Context: %+v, turn=%s", b, turn.ID)
				}
				if cp, ok := CurrentContextCheckpoint(ctx); ok {
					t.Errorf("invalid relation exposed Checkpoint: %s, expected no %s", cp.ID, checkpoint.ID)
				}
				return kernel.Snapshot{}, nil
			})
			if invoked || !errors.Is(err, ErrConflict) {
				t.Errorf("invalid relation did not fail closed: invoked=%t err=%v", invoked, err)
			}
		})
	}
}
