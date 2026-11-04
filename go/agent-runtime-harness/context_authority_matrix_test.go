package harness

import (
	"context"
	"errors"
	"math/rand"
	"testing"
	"testing/quick"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	runtimecontext "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/context"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/handoff"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/runrelation"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

// Fresh cross-boundary audit: a corrupt indexed Turn read must never grant
// another session's durable window to a physically different Run.
type matrixWrongTurnStore struct {
	Store
	requestedTurn string
	foreignTurn   Turn
}

func (s matrixWrongTurnStore) GetTurn(ctx context.Context, id string) (Turn, error) {
	if id == s.requestedTurn {
		return s.foreignTurn, nil
	}
	return s.Store.GetTurn(ctx, id)
}
func TestAuditMatrixForeignTurnIdentityNeverAuthorizesDifferentSession(t *testing.T) {
	store, turn, _, root := contextRunMiddlewareFixture(t)
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	manager := runtimecontext.NewManager(runtimecontext.Dependencies{})
	foreignCheckpoint, err := manager.Open(t.Context(), runtimecontext.OpenRequest{
		ScopeID: "foreign-session-with-secret", StaticFingerprint: runtimecontext.StaticFingerprint("foreign-static"),
		SourcePath: []string{"foreign-source"}, Instructions: "private foreign session",
		Entries: []runtimecontext.Entry{{ID: "foreign-entry", SourceID: "foreign-source", TurnID: "foreign-host-turn", Message: model.Message{Role: model.RoleUser, Content: model.TextParts("SECRET FOREIGN SESSION BYTES")}}}})
	if err != nil {
		t.Fatal(err)
	}
	other, fresh, err := store.CreateTurn(t.Context(), Turn{ID: "foreign-turn-unrelated", SessionID: foreignCheckpoint.ScopeID, HostTurn: HostRef{Kind: contextRunHostTurnKind, ID: "foreign-host-turn"}, ConfigSnapshotID: "config-foreign", Status: TurnRunning, Revision: 1, CreatedAt: now, UpdatedAt: now})
	if err != nil || !fresh {
		t.Fatalf("foreign turn: %t %v", fresh, err)
	}
	other, err = store.CommitContextCheckpoint(t.Context(), ContextCheckpointCommit{TurnID: other.ID, ExpectedTurnRevision: other.Revision, Checkpoint: foreignCheckpoint, UpdatedAt: now.Add(time.Second)})
	if err != nil {
		t.Fatal(err)
	}
	wrapped := matrixWrongTurnStore{Store: store, requestedTurn: turn.ID, foreignTurn: other}
	mw, err := NewContextRunMiddleware(wrapped, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, operation := range []plugin.RunOperation{plugin.RunStart, plugin.RunResume, plugin.RunResolveApproval} {
		t.Run(string(operation), func(t *testing.T) {
			called := false
			_, err = mw.Run(t.Context(), plugin.RunInvocation{Operation: operation, Kind: kernel.RunKind(ExecutionAgent), RunID: root.ExecutionRefID}, func(ctx context.Context) (kernel.Snapshot, error) {
				called = true
				if got, ok := CurrentContextCheckpoint(ctx); ok {
					t.Errorf("FOREIGN SESSION CHECKPOINT ACCEPTED: scope=%s content=%q", got.ScopeID, model.TextValue(got.Window.Entries[0].Message.Content))
				}
				if bound, ok := CurrentContextWindowBinding(ctx); ok {
					t.Errorf("FOREIGN TURN BINDING ACCEPTED: %+v", bound)
				}
				return kernel.Snapshot{}, nil
			})
			if called || !errors.Is(err, ErrConflict) {
				t.Errorf("foreign Turn read must abort: dispatch=%t err=%v", called, err)
			}
		})
	}
}

type matrixArtifactReadStore struct {
	Store
	artifactID string
	substitute runtimecontext.Artifact
}

func (s matrixArtifactReadStore) GetContextArtifact(ctx context.Context, id string) (runtimecontext.Artifact, error) {
	if id == s.artifactID {
		return s.substitute, nil
	}
	return s.Store.GetContextArtifact(ctx, id)
}
func TestAuditMatrixArtifactPayloadIdentityAndHashNeverTrusted(t *testing.T) {
	store, turn, checkpoint, root := contextRunMiddlewareFixture(t)
	artifact, err := runtimecontext.NewArtifact(runtimecontext.ArtifactCompaction, turn.SessionID, checkpoint.Generation, checkpoint.CoveredThroughSourceID, "LEGITIMATELY REQUESTED ORIGINAL PAYLOAD", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = store.PutContextArtifact(t.Context(), artifact); err != nil {
		t.Fatal(err)
	}
	other, err := runtimecontext.NewArtifact(runtimecontext.ArtifactCompaction, turn.SessionID, checkpoint.Generation, checkpoint.CoveredThroughSourceID, "DIFFERENT SAME-SCOPE PRIVATE ARTIFACT", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		sub  runtimecontext.Artifact
	}{
		{name: "mismatched_artifact_identity", sub: other},
		{name: "invalid_content_hash", sub: func() runtimecontext.Artifact { a := artifact; a.Content = "CORRUPTED PAYLOAD"; return a }()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			handler := NewContextArtifactToolHandler(matrixArtifactReadStore{Store: store, artifactID: artifact.ID, substitute: tc.sub}, nil)
			request := tools.ExecutionRequest{RunID: root.ExecutionRefID, Call: tools.Call{ID: "matrix-read", ToolKey: ContextArtifactToolKey, Arguments: mustContextArtifactArguments(t, artifact.ID, 0, contextArtifactMaxLimit)}}
			result, readErr := handler.Execute(t.Context(), request)
			if !errors.Is(readErr, ErrConflict) || len(result.Content) > 0 {
				t.Errorf("untrusted Artifact response admitted: expected ID=%s received ID=%s content=%q err=%v", artifact.ID, tc.sub.ID, string(result.Content), readErr)
			}
		})
	}
}

// Excluded Context must stay excluded even when a later trusted component
// bound a checkpoint to the same child context; otherwise an early return
// bypasses the execution-scoped durable ownership proof.
func TestAuditMatrixExcludedContextCannotCarryReboundCheckpoint(t *testing.T) {
	store, turn, checkpoint, root := contextRunMiddlewareFixture(t)
	mw, err := NewContextRunMiddleware(store, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"same_turn", "foreign_turn"} {
		t.Run(mode, func(t *testing.T) {
			id := turn.ID
			if mode == "foreign_turn" {
				id = "unrelated-turn"
			}
			ctx := withContextWindowBinding(withContextCheckpoint(WithoutContextWindow(t.Context()), checkpoint), id, ContextWindowOwner)
			_, err = mw.Run(ctx, plugin.RunInvocation{Operation: plugin.RunResume, Kind: kernel.RunKind(ExecutionAgent), RunID: root.ExecutionRefID}, func(got context.Context) (kernel.Snapshot, error) {
				if cp, ok := CurrentContextCheckpoint(got); ok {
					t.Errorf("explicitly EXCLUDED Context was forwarded: %s", cp.ID)
				}
				if b, ok := CurrentContextWindowBinding(got); ok {
					t.Errorf("explicitly EXCLUDED binding was forwarded: %+v", b)
				}
				return kernel.Snapshot{}, nil
			})
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

// Deterministic generative state-property test. Exercise every combination
// of physical Run class, process Context state and Agent lifecycle operation.
// This oracle is independent of individual patch-specific regression cases.
func TestAuditPropertyContextAuthorityTransitions(t *testing.T) {
	property := func(runClass, callerContext, operation uint8) bool {
		store, turn, checkpoint, root := contextRunMiddlewareFixture(t)
		delegation := handoff.Delegation{ID: "state-specialist", MemberID: "specialist", RoleID: "specialist",
			ChildRunID: "state-specialist-run", Goal: "isolated", Status: handoff.StatusQueued}
		input, hash, err := marshalInvocationValue(delegation)
		if err != nil {
			t.Error(err)
			return false
		}
		input, hash, err = sealChildRootInput(input, hash, root)
		if err != nil {
			t.Error(err)
			return false
		}
		id, err := InvocationID(turn.ID, "state-parent", CapabilityAgent, delegation.ID)
		if err != nil {
			t.Error(err)
			return false
		}
		child := root
		child.ID, child.ExecutionRefID, child.ParentItemID, child.Input, child.InputHash = id, delegation.ChildRunID, "state-parent", input, hash
		if _, _, err = store.CreateInvocation(t.Context(), child); err != nil {
			t.Error(err)
			return false
		}
		relations := contextRunRelationFixture{
			"state-plain-child":           {ParentRunID: root.ExecutionRefID, ChildRunID: "state-plain-child", Kind: runrelation.KindCapability, OwnerNodeID: "plain-child", CreatedAt: time.Now().UTC()},
			"state-specialist-grandchild": {ParentRunID: child.ExecutionRefID, ChildRunID: "state-specialist-grandchild", Kind: runrelation.KindCapability, OwnerNodeID: "spec-child", CreatedAt: time.Now().UTC()},
			"state-invalid-child":         {ParentRunID: root.ExecutionRefID, ChildRunID: "state-invalid-child", Kind: runrelation.Kind("not-a-kind"), OwnerNodeID: "bad", CreatedAt: time.Now().UTC()},
		}
		mw, err := NewContextRunMiddleware(store, relations)
		if err != nil {
			t.Error(err)
			return false
		}
		runIDs := []string{root.ExecutionRefID, "state-plain-child", child.ExecutionRefID, "state-specialist-grandchild", "unrelated-no-owner", "state-invalid-child"}
		ops := []plugin.RunOperation{plugin.RunStart, plugin.RunResume, plugin.RunResolveApproval}
		class := int(runClass) % len(runIDs)
		c := int(callerContext) % 7
		op := ops[int(operation)%len(ops)]
		ctx := t.Context()
		switch c {
		case 1:
			ctx = withContextWindowBinding(withContextCheckpoint(ctx, checkpoint), turn.ID, ContextWindowOwner)
		case 2:
			ctx = withContextWindowBinding(withContextCheckpoint(ctx, checkpoint), turn.ID, ContextWindowReadOnly)
		case 3:
			ctx = withContextWindowBinding(withContextCheckpoint(ctx, checkpoint), "foreign-turn", ContextWindowOwner)
		case 4:
			ctx = withContextCheckpoint(ctx, checkpoint)
		case 5:
			ctx = WithoutContextWindow(withContextWindowBinding(withContextCheckpoint(ctx, checkpoint), turn.ID, ContextWindowOwner))
		case 6:
			altered := runtimecontext.CloneCheckpoint(checkpoint)
			altered.Window.Entries[0].Message.Content = model.TextParts("ALTERED IN PROCESS WITHOUT HASH UPDATE")
			ctx = withContextWindowBinding(withContextCheckpoint(ctx, altered), turn.ID, ContextWindowOwner)
		}
		wantScope := class == 0 || class == 1
		if class == 5 || c == 5 {
			wantScope = false
		}
		wantAccess := ContextWindowOwner
		if class == 1 || (class == 0 && c == 2) {
			wantAccess = ContextWindowReadOnly
		}
		calls := 0
		_, err = mw.Run(ctx, plugin.RunInvocation{Operation: op, Kind: kernel.RunKind(ExecutionAgent), RunID: runIDs[class]}, func(nextCtx context.Context) (kernel.Snapshot, error) {
			calls++
			got, has := CurrentContextCheckpoint(nextCtx)
			binding, hasBinding := CurrentContextWindowBinding(nextCtx)
			if !wantScope {
				if has || hasBinding {
					t.Errorf("state violation run=%d context=%d op=%s: scope=%s bound=%+v", class, c, op, got.ScopeID, binding)
					return kernel.Snapshot{}, errors.New("oracle violation")
				}
			} else {
				if !has || !hasBinding || got.ID != checkpoint.ID || binding.TurnID != turn.ID || binding.Access != wantAccess || !runtimecontext.ValidCheckpoint(got) {
					t.Errorf("wrong authority run=%d context=%d op=%s: checkpoint=%s binding=%+v want=%s", class, c, op, got.ID, binding, wantAccess)
					return kernel.Snapshot{}, errors.New("oracle violation")
				}
			}
			return kernel.Snapshot{}, nil
		})
		if class == 5 && c != 5 {
			if calls != 0 || !errors.Is(err, ErrConflict) {
				t.Errorf("invalid relation allowed dispatch: run=%d context=%d op=%s dispatched=%d err=%v", class, c, op, calls, err)
				return false
			}
		} else if err != nil || calls != 1 {
			t.Errorf("valid state failed: run=%d context=%d op=%s dispatched=%d err=%v", class, c, op, calls, err)
			return false
		}
		return true
	}
	// Complete finite state product and randomized variants with a deterministic seed.
	for runClass := uint8(0); runClass < 6; runClass++ {
		for callerContext := uint8(0); callerContext < 7; callerContext++ {
			for op := uint8(0); op < 3; op++ {
				if !property(runClass, callerContext, op) {
					t.Fatalf("state product failed: %d %d %d", runClass, callerContext, op)
				}
			}
		}
	}
	config := &quick.Config{MaxCount: 500, Rand: rand.New(rand.NewSource(20261104))}
	if err := quick.Check(property, config); err != nil {
		t.Fatal(err)
	}
}

func TestAuditPropertyInvalidRelationFieldsCannotGrantContext(t *testing.T) {
	property := func(mask uint8) bool {
		store, _, _, root := contextRunMiddlewareFixture(t)
		base := runrelation.Relation{ParentRunID: root.ExecutionRefID, ChildRunID: "prop-child", Kind: runrelation.KindCapability, OwnerNodeID: "prop-node", CreatedAt: time.Now().UTC()}
		switch mask % 10 {
		case 0:
			base.ParentRunID = ""
		case 1:
			base.ParentRunID = " prop-parent "
		case 2:
			base.ParentRunID = base.ChildRunID
		case 3:
			base.ChildRunID = "other-child"
		case 4:
			base.Kind = "invalid-kind"
		case 5:
			base.Kind = " capability "
		case 6:
			base.OwnerNodeID = ""
		case 7:
			base.OwnerNodeID = " prop-node "
		case 8:
			base.CreatedAt = time.Time{}
		case 9:
			base.ChildRunID = " prop-child "
		}
		mw, err := NewContextRunMiddleware(store, contextRunRelationFixture{"prop-child": base})
		if err != nil {
			t.Error(err)
			return false
		}
		called := false
		_, err = mw.Run(t.Context(), plugin.RunInvocation{Operation: plugin.RunResume, Kind: kernel.RunKind(ExecutionAgent), RunID: "prop-child"}, func(ctx context.Context) (kernel.Snapshot, error) {
			called = true
			return kernel.Snapshot{}, nil
		})
		if called || !errors.Is(err, ErrConflict) {
			t.Errorf("invalid relation variant=%d returned err=%v called=%v", mask%10, err, called)
			return false
		}
		return true
	}
	for i := uint8(0); i < 10; i++ {
		if !property(i) {
			t.Fatalf("relation variant %d", i)
		}
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 500, Rand: rand.New(rand.NewSource(20261104))}); err != nil {
		t.Fatal(err)
	}
}

// A root Agent's complete Input + hash and deterministic ID are durable
// provenance, not optional metadata once an execution reference exists.
type matrixCorruptRootInvocationStore struct {
	Store
	target string
	mode   string
}

func (s matrixCorruptRootInvocationStore) GetInvocationByExecutionRefID(ctx context.Context, id string) (Invocation, error) {
	val, err := s.Store.GetInvocationByExecutionRefID(ctx, id)
	if err == nil && id == s.target {
		switch s.mode {
		case "erase_input_and_hash":
			val.Input = nil
			val.InputHash = ""
		case "rewrite_invocation_identity":
			val.ID = "different-unrelated-invocation-id"
		case "rewrite_capability_key":
			val.CapabilityKey = CapabilityTeam
		}
	}
	return val, err
}
func TestAuditMatrixRootProvenanceMutationNeverGrantsOwner(t *testing.T) {
	store, turn, checkpoint, root := contextRunMiddlewareFixture(t)
	for _, mode := range []string{"erase_input_and_hash", "rewrite_invocation_identity", "rewrite_capability_key"} {
		t.Run(mode, func(t *testing.T) {
			corrupted := matrixCorruptRootInvocationStore{Store: store, target: root.ExecutionRefID, mode: mode}
			val, err := corrupted.GetInvocationByExecutionRefID(t.Context(), root.ExecutionRefID)
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("fixture mode=%s accepted_by_validInvocation=%v", mode, validInvocation(val))
			mw, err := NewContextRunMiddleware(corrupted, nil)
			if err != nil {
				t.Fatal(err)
			}
			calls := 0
			_, err = mw.Run(t.Context(), plugin.RunInvocation{Operation: plugin.RunResume, Kind: kernel.RunKind(ExecutionAgent), RunID: root.ExecutionRefID}, func(ctx context.Context) (kernel.Snapshot, error) {
				calls++
				if cp, ok := CurrentContextCheckpoint(ctx); ok {
					t.Errorf("unproven Agent Root retained Checkpoint %s; original scope=%s", cp.ID, checkpoint.ScopeID)
				}
				if b, ok := CurrentContextWindowBinding(ctx); ok {
					t.Errorf("unproven Agent Root retained binding %+v original=%s", b, turn.ID)
				}
				return kernel.Snapshot{}, nil
			})
			if calls > 0 || !errors.Is(err, ErrConflict) {
				t.Errorf("invalid root identity should not dispatch: calls=%d err=%v", calls, err)
			}
		})
	}
}

// Budget is a second consumer of the same Run->Invocation->Turn identity.
// A corrupted Invocation TurnID cannot reassign physical model charges to
// another session's ledger without an authority conflict.
type matrixBudgetOwnerSwapStore struct {
	Store
	target     string
	forgedTurn string
}

func (s matrixBudgetOwnerSwapStore) GetInvocationByExecutionRefID(ctx context.Context, id string) (Invocation, error) {
	inv, err := s.Store.GetInvocationByExecutionRefID(ctx, id)
	if err == nil && id == s.target {
		inv.TurnID = s.forgedTurn
	}
	return inv, err
}
func TestAuditMatrixBudgetCannotBillAnotherSessionOnForgedInvocationTurn(t *testing.T) {
	store, _, request := hostedBudgetFixture(t, budget.Limits{MaxLLMCalls: 3})
	now := time.Now().UTC()
	foreignConf, err := SealConfigSnapshot("foreign-budget-turn", ConfigSnapshot{SharedBudget: &budget.Limits{MaxLLMCalls: 3}}, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = store.PutConfigSnapshot(t.Context(), foreignConf); err != nil {
		t.Fatal(err)
	}
	if _, _, err = store.CreateTurn(t.Context(), Turn{ID: foreignConf.TurnID, SessionID: "foreign-session", HostTurn: HostRef{Kind: "test", ID: "foreign"}, ConfigSnapshotID: foreignConf.ID, Status: TurnRunning, Revision: 1, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	swapped := matrixBudgetOwnerSwapStore{Store: store, target: request.RunID, forgedTurn: foreignConf.TurnID}
	shared, err := NewBudgetMiddleware(BudgetMiddlewareDependencies{Store: swapped, Ledgers: store, Clock: hostedBudgetClock{}})
	if err != nil {
		t.Fatal(err)
	}
	ledger, err := shared.bindRun(t.Context(), request.RunID)
	if err == nil && ledger.ID == foreignConf.TurnID {
		t.Errorf("BILLING OWNERSHIP MISBOUND: physical Run=%s is charged to foreign Turn=%s", request.RunID, ledger.ID)
	}
	if err == nil {
		t.Errorf("a forged Invocation TurnID must be rejected before charging: resulting ledger=%s", ledger.ID)
	}
}
