package harness_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	runtimecontext "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/context"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

func stateFromContextBoundary(boundary harness.ContextSourceBoundary, version int) *runtimecontext.WorkingState {
	return &runtimecontext.WorkingState{Version: version, Source: runtimecontext.WorkingStateSource{
		CheckpointID: boundary.CheckpointID, ContentHash: boundary.ContentHash,
		CoveredThroughSourceID: boundary.CoveredThroughSourceID, CoveredPathHash: boundary.CoveredPathHash,
	}, Goal: "finish the verified task", Pending: []string{"verify original evidence"}, EvidenceRefs: []string{"inert:source-reference"}}
}

func workingStateHistory(names ...string) *harness.ContextSeed {
	entries := make([]runtimecontext.Entry, 0, len(names)*2)
	for i, name := range names {
		entries = append(entries, contextEntry(name+"/user", name, model.RoleUser, name+" prompt", i == len(names)-1))
		if i != len(names)-1 {
			entries = append(entries, contextEntry(name+"/assistant", name, model.RoleAssistant, "answer", false))
		}
	}
	return contextSeed("", entries...)
}

func TestSessionWorkingStateUpdateCarryVersionAndFrozenReplay(t *testing.T) {
	store, runs := harness.NewMemoryStore(), memory.NewStore()
	client := &agentSessionModel{}
	options := agentSessionOptions(t, runs, store, client)
	seeds := map[string]*harness.ContextSeed{"first": workingStateHistory("first")}
	options.Session.Transcript = harness.TranscriptSourceFunc(func(_ context.Context, _, turn harness.HostRef, _ string) (*harness.ContextSeed, error) {
		return seeds[turn.ID], nil
	})
	session, err := harness.OpenAgentSession(options)
	if err != nil {
		t.Fatal(err)
	}
	first, err := session.Prompt(t.Context(), "first", "first prompt")
	if err != nil {
		t.Fatal(err)
	}
	secondSeed := workingStateHistory("first", "second")
	boundary, err := session.ResolveContextSourceBoundaryForPath(t.Context(), "first", "", secondSeed.SourcePath)
	if err != nil || boundary.CheckpointID != first.Turn.ContextCheckpointID || boundary.ContentHash == "" || boundary.CoveredPathHash == "" {
		t.Fatalf("missing exact selected source: %+v err=%v", boundary, err)
	}
	secondSeed.WorkingState = stateFromContextBoundary(boundary, 1)
	submitted := runtimecontext.CloneWorkingState(secondSeed.WorkingState)
	seeds["second"] = secondSeed
	second, err := session.Prompt(t.Context(), "second", "second prompt")
	if err != nil || second.Turn.ContextRef.Generation != first.Turn.ContextRef.Generation+1 || len(client.requests) != 2 {
		t.Fatalf("state update: status=%s generation=%d err=%v requests=%d", second.Turn.Status, second.Turn.ContextRef.Generation, err, len(client.requests))
	}
	checkpoint := requireStoredContextCheckpoint(t, store, second.Turn.ContextCheckpointID)
	if !reflect.DeepEqual(checkpoint.Window.WorkingState, submitted) {
		t.Fatal("update was not sealed with its exact source and body")
	}
	visible := false
	for _, message := range client.requests[1].Messages {
		if strings.Contains(model.TextValue(message.Content), "<host_working_state>") {
			visible = message.Role == model.RoleUser && strings.Contains(model.TextValue(message.Content), submitted.Goal)
		}
	}
	if !visible {
		t.Fatal("WorkingState was not supplied as untrusted model-visible task data")
	}
	if _, err = session.Prompt(t.Context(), "second", "second prompt"); err != nil || len(client.requests) != 2 {
		t.Fatalf("same frozen submission revalidated its own version or generated: %v calls=%d", err, len(client.requests))
	}
	secondSeed.WorkingState.Pending[0] = "caller mutated the input"
	stored := requireStoredContextCheckpoint(t, store, second.Turn.ContextCheckpointID)
	if !reflect.DeepEqual(stored.Window.WorkingState, submitted) {
		t.Fatal("caller mutation changed durable WorkingState")
	}
	secondSeed.WorkingState = runtimecontext.CloneWorkingState(submitted)
	seeds["third"] = workingStateHistory("first", "second", "third")
	third, err := session.Prompt(t.Context(), "third", "third prompt")
	if err != nil {
		t.Fatal(err)
	}
	carried := requireStoredContextCheckpoint(t, store, third.Turn.ContextCheckpointID)
	if !reflect.DeepEqual(carried.Window.WorkingState, submitted) || carried.Generation != checkpoint.Generation {
		t.Fatalf("nil update lost or rewrote prior state: %+v", carried.Window.WorkingState)
	}
	fourthSeed := workingStateHistory("first", "second", "third", "fourth")
	boundary, err = session.ResolveContextSourceBoundaryForPath(t.Context(), "first", "", fourthSeed.SourcePath)
	if err != nil {
		t.Fatal(err)
	}
	fourthSeed.WorkingState = stateFromContextBoundary(boundary, 1)
	seeds["fourth"] = fourthSeed
	if _, err = session.Prompt(t.Context(), "fourth", "fourth prompt"); !errors.Is(err, runtimecontext.ErrLineageConflict) || len(client.requests) != 3 {
		t.Fatalf("non-incrementing version was admitted: %v calls=%d", err, len(client.requests))
	}
	assertWorkingStateTurnAbsent(t, store, options.Session.HostThread, "fourth")
	fourthSeed.WorkingState.Version = 2
	fourth, err := session.Prompt(t.Context(), "fourth", "fourth prompt")
	if err != nil || fourth.Turn.ContextRef.Generation != carried.Generation+1 || len(client.requests) != 4 {
		t.Fatalf("corrected version could not use the unpoisoned Turn ID: %v calls=%d", err, len(client.requests))
	}
	// A historical Turn replay is bound to its original update, not today's
	// active WorkingState version. Changed same-ID bodies still conflict.
	if _, err = session.Prompt(t.Context(), "second", "second prompt"); err != nil || len(client.requests) != 4 {
		t.Fatal(err)
	}
	secondSeed.WorkingState.Goal = "different original update"
	if _, err = session.Prompt(t.Context(), "second", "second prompt"); !errors.Is(err, harness.ErrConflict) || len(client.requests) != 4 {
		t.Fatalf("changed WorkingState bypassed admission identity: %v calls=%d", err, len(client.requests))
	}
}

func assertWorkingStateTurnAbsent(t *testing.T, store harness.Store, thread harness.HostRef, turn string) {
	t.Helper()
	sessionID, err := harness.SessionID(thread)
	if err != nil {
		t.Fatal(err)
	}
	id, err := harness.TurnID(sessionID, harness.HostRef{Kind: testContextHostKind, ID: turn})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.GetTurn(t.Context(), id); !errors.Is(err, harness.ErrNotFound) {
		t.Fatalf("rejected WorkingState persisted a Turn: %v", err)
	}
}

func TestHarnessWorkingStateRejectsBadSourceBeforeTurnAdmission(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*harness.StartRequest)
	}{
		{"content hash", func(r *harness.StartRequest) { r.Context.WorkingState.Source.ContentHash = strings.Repeat("f", 64) }},
		{"covered source", func(r *harness.StartRequest) { r.Context.WorkingState.Source.CoveredThroughSourceID = "foreign-source" }},
		{"covered path", func(r *harness.StartRequest) { r.Context.WorkingState.Source.CoveredPathHash = strings.Repeat("f", 64) }},
		{"checkpoint", func(r *harness.StartRequest) {
			r.Context.WorkingState.Source.CheckpointID = "ctxc_" + strings.Repeat("f", 32)
		}},
		{"branch", func(r *harness.StartRequest) {
			r.Context.SourcePath[0] = "another-root"
			r.Context.Entries[0].SourceID = "another-root"
		}},
		{"static instructions", func(r *harness.StartRequest) { r.Config.Instructions = "new static instructions" }},
		{"first checkpoint", func(r *harness.StartRequest) { r.HostThread.ID += "-new"; r.Thread.ID += "-new" }},
		{"invalid version", func(r *harness.StartRequest) { r.Context.WorkingState.Version = 0 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newSourceDeltaHarnessFixture(t, "working-source-"+tc.name)
			first := f.common
			first.HostTurn = harness.HostRef{Kind: testContextHostKind, ID: "first"}
			first.Goal = "first prompt"
			first.Context = workingStateHistory("first")
			started := startContextTurn(t, f.runner, first)
			next := f.common
			next.HostTurn = harness.HostRef{Kind: testContextHostKind, ID: "next"}
			next.Goal = "next prompt"
			next.Context = workingStateHistory("first", "next")
			boundary, err := f.runner.ResolveContextSourceBoundaryForPath(t.Context(), next.HostThread, next.Config, "", next.Context.SourcePath)
			if err != nil || boundary.CheckpointID != started.Turn.ContextCheckpointID {
				t.Fatal(err)
			}
			next.Context.WorkingState = stateFromContextBoundary(boundary, 1)
			tc.change(&next)
			if _, err = f.runner.StartAgentTurn(t.Context(), next); err == nil || len(f.provider.requests) != 1 {
				t.Fatalf("invalid source reached execution: %v calls=%d", err, len(f.provider.requests))
			}
			assertWorkingStateTurnAbsent(t, f.store, next.HostThread, "next")
		})
	}
}

func TestHarnessWorkingStateSourceDeltaUsesExactExplicitBase(t *testing.T) {
	f := newSourceDeltaHarnessFixture(t, "working-explicit-delta")
	first := f.common
	first.HostTurn = harness.HostRef{Kind: testContextHostKind, ID: "first"}
	first.Goal = "first prompt"
	first.Context = workingStateHistory("first")
	startContextTurn(t, f.runner, first)
	boundary, err := f.runner.ResolveContextSourceBoundary(t.Context(), first.HostThread, first.Config, "")
	if err != nil {
		t.Fatal(err)
	}
	next := f.common
	next.HostTurn = harness.HostRef{Kind: testContextHostKind, ID: "next"}
	next.Goal = "next prompt"
	next.Context = &harness.ContextSeed{SourceDelta: true, BaseCheckpointID: boundary.CheckpointID, SourcePath: []string{"first/assistant", "next/user"}, Entries: []runtimecontext.Entry{
		contextEntry("first/assistant", "first", model.RoleAssistant, "answer", false), contextEntry("next/user", "next", model.RoleUser, "next prompt", true),
	}, WorkingState: stateFromContextBoundary(boundary, 1)}
	done := startContextTurn(t, f.runner, next)
	cp := requireStoredContextCheckpoint(t, f.store, done.Turn.ContextCheckpointID)
	if cp.Window.WorkingState == nil || cp.Window.WorkingState.Source.CheckpointID != boundary.CheckpointID || cp.Generation != 2 {
		t.Fatalf("explicit base did not bind WorkingState: %+v", cp)
	}
}

type workingStateBoundaryStore struct {
	harness.Store
	foreign           *runtimecontext.Checkpoint
	queries           int
	corruptAfterQuery bool
	corruptOwner      bool
}

func (s *workingStateBoundaryStore) FindContextCheckpointForPath(ctx context.Context, q harness.ContextCheckpointPathQuery) (runtimecontext.Checkpoint, error) {
	s.queries++
	if s.corruptAfterQuery {
		s.corruptOwner = true
	}
	if s.foreign != nil {
		return runtimecontext.CloneCheckpoint(*s.foreign), nil
	}
	return s.Store.FindContextCheckpointForPath(ctx, q)
}
func (s *workingStateBoundaryStore) GetInvocation(ctx context.Context, id string) (harness.Invocation, error) {
	v, err := s.Store.GetInvocation(ctx, id)
	if err == nil && s.corruptOwner {
		v.InputHash = strings.Repeat("f", 64)
	}
	return v, err
}

func TestSessionWorkingStateSourceHelperChecksOwnerAndStoreScope(t *testing.T) {
	for _, mode := range []string{"foreign actor", "foreign thread", "foreign scope", "foreign static", "changed owner after read"} {
		t.Run(mode, func(t *testing.T) {
			backing := harness.NewMemoryStore()
			store := &workingStateBoundaryStore{Store: backing}
			client := &agentSessionModel{}
			options := agentSessionOptions(t, memory.NewStore(), store, client)
			session, err := harness.OpenAgentSession(options)
			if err != nil {
				t.Fatal(err)
			}
			first, err := session.Prompt(t.Context(), "first", "first prompt")
			if err != nil {
				t.Fatal(err)
			}
			own := requireStoredContextCheckpoint(t, backing, first.Turn.ContextCheckpointID)
			if mode == "foreign actor" {
				options.Session.Actor.ActorID = "different-owner"
			}
			if mode == "foreign thread" {
				options.Session.Thread.ID = "different-thread"
			}
			if mode == "foreign scope" || mode == "foreign static" {
				scope, fingerprint := own.ScopeID, own.StaticFingerprint
				if mode == "foreign scope" {
					scope = "different-scope"
				} else {
					fingerprint = "different-static"
				}
				foreign, e := runtimecontext.NewManager(runtimecontext.Dependencies{}).Open(t.Context(), runtimecontext.OpenRequest{ScopeID: scope, StaticFingerprint: fingerprint, Instructions: own.Window.Instructions, SourcePath: []string{"first/user"}, Entries: runtimecontext.CloneEntries(own.Window.Entries)})
				if e != nil {
					t.Fatal(e)
				}
				store.foreign = &foreign
			}
			store.corruptAfterQuery = mode == "changed owner after read"
			reopened, err := harness.OpenAgentSession(options)
			if err != nil {
				t.Fatal(err)
			}
			before := store.queries
			if _, err = reopened.ResolveContextSourceBoundaryForPath(t.Context(), "first", "", []string{"first/user", "next/user"}); !errors.Is(err, harness.ErrConflict) {
				t.Fatalf("source helper accepted %s: %v", mode, err)
			}
			if (mode == "foreign actor" || mode == "foreign thread") && store.queries != before {
				t.Fatal("foreign handle queried private Context before frozen-owner validation")
			}
			if len(client.requests) != 1 {
				t.Fatal("source helper dispatched a model")
			}
		})
	}
}

func TestHarnessWorkingStateBranchRoundTripDoesNotLeakActiveState(t *testing.T) {
	f := newSourceDeltaHarnessFixture(t, "working-branch-roundtrip")
	root := f.common
	root.HostTurn, root.Goal, root.Context = harness.HostRef{Kind: testContextHostKind, ID: "root"}, "root prompt", workingStateHistory("root")
	startContextTurn(t, f.runner, root)
	a := f.common
	a.HostTurn, a.Goal, a.Context = harness.HostRef{Kind: testContextHostKind, ID: "a"}, "a prompt", workingStateHistory("root", "a")
	boundary, err := f.runner.ResolveContextSourceBoundaryForPath(t.Context(), a.HostThread, a.Config, "", a.Context.SourcePath)
	if err != nil {
		t.Fatal(err)
	}
	a.Context.WorkingState = stateFromContextBoundary(boundary, 1)
	a.Context.WorkingState.Goal = "branch A private task"
	startedA := startContextTurn(t, f.runner, a)
	stateA := requireStoredContextCheckpoint(t, f.store, startedA.Turn.ContextCheckpointID)
	b := f.common
	b.HostTurn, b.Goal, b.Context = harness.HostRef{Kind: testContextHostKind, ID: "b"}, "b prompt", workingStateHistory("root", "b")
	startedB := startContextTurn(t, f.runner, b)
	stateB := requireStoredContextCheckpoint(t, f.store, startedB.Turn.ContextCheckpointID)
	if stateB.Window.WorkingState != nil {
		t.Fatal("new branch inherited the unrelated active branch's WorkingState")
	}
	for _, message := range f.provider.requests[2].Messages {
		if strings.Contains(model.TextValue(message.Content), "branch A private task") {
			t.Fatal("branch A task data leaked to B")
		}
	}
	a2 := f.common
	a2.HostTurn, a2.Goal, a2.Context = harness.HostRef{Kind: testContextHostKind, ID: "a2"}, "a2 prompt", workingStateHistory("root", "a", "a2")
	returnedA := startContextTurn(t, f.runner, a2)
	stateA2 := requireStoredContextCheckpoint(t, f.store, returnedA.Turn.ContextCheckpointID)
	if !reflect.DeepEqual(stateA2.Window.WorkingState, stateA.Window.WorkingState) || stateA2.CacheIdentity != stateA.CacheIdentity {
		t.Fatal("return to branch A did not reuse A's own state and cache lineage")
	}
}

var errWorkingStateCheckpointCommit = errors.New("injected before first WorkingState checkpoint commit")

type workingStateCheckpointFaultStore struct {
	harness.Store
	target string
	armed  bool
}

func (s *workingStateCheckpointFaultStore) CommitContextCheckpoint(ctx context.Context, request harness.ContextCheckpointCommit) (harness.Turn, error) {
	if s.armed && request.TurnID == s.target {
		s.armed = false
		return harness.Turn{}, errWorkingStateCheckpointCommit
	}
	return s.Store.CommitContextCheckpoint(ctx, request)
}

func TestHarnessWorkingStateAcceptedTurnRecoversOriginalBaseAfterHeadAdvance(t *testing.T) {
	backing := harness.NewMemoryStore()
	store := &workingStateCheckpointFaultStore{Store: backing}
	provider := &multiContextCaptureModel{}
	runner, _ := newResumeTurnHarness(t, memory.NewStore(), store, provider, newResumeTurnClock(), nil)
	options := resumeTurnSessionOptions()
	common := harness.StartRequest{HostThread: options.HostThread, Actor: options.Actor, Thread: options.Thread, Config: options.Config}
	first := common
	first.HostTurn, first.Goal, first.Context = harness.HostRef{Kind: testContextHostKind, ID: "first"}, "first prompt", workingStateHistory("first")
	startContextTurn(t, runner, first)
	second := common
	second.HostTurn, second.Goal, second.Context = harness.HostRef{Kind: testContextHostKind, ID: "second"}, "second prompt", workingStateHistory("first", "second")
	boundary, err := runner.ResolveContextSourceBoundaryForPath(t.Context(), common.HostThread, common.Config, "", second.Context.SourcePath)
	if err != nil {
		t.Fatal(err)
	}
	second.Context.WorkingState = stateFromContextBoundary(boundary, 1)
	sessionID, err := harness.SessionID(common.HostThread)
	if err != nil {
		t.Fatal(err)
	}
	store.target, err = harness.TurnID(sessionID, second.HostTurn)
	if err != nil {
		t.Fatal(err)
	}
	store.armed = true
	if _, err = runner.StartAgentTurn(t.Context(), second); !errors.Is(err, errWorkingStateCheckpointCommit) || len(provider.requests) != 1 {
		t.Fatalf("fixture did not stop after admission and before Context commit: %v calls=%d", err, len(provider.requests))
	}
	admitted, err := backing.GetTurn(t.Context(), store.target)
	if err != nil || admitted.Status != harness.TurnAccepted || admitted.ContextCheckpointID != "" {
		t.Fatalf("expected original accepted Turn without a checkpoint: %+v err=%v", admitted, err)
	}
	// A separate, valid Host state-only update advances the same source
	// boundary. Re-running new-admission selection would now choose V2,
	// whereas the already admitted V1 remains bound to its original source.
	other := common
	other.HostTurn, other.Goal = harness.HostRef{Kind: testContextHostKind, ID: "state-only"}, "update task state"
	other.Context = &harness.ContextSeed{SourceDelta: true, BaseCheckpointID: boundary.CheckpointID, WorkingState: stateFromContextBoundary(boundary, 2)}
	other.Context.WorkingState.Goal = "separate later task state"
	advanced := startContextTurn(t, runner, other)
	current, err := runner.ResolveContextSourceBoundaryForPath(t.Context(), common.HostThread, common.Config, "", second.Context.SourcePath)
	if err != nil || current.CheckpointID != advanced.Turn.ContextCheckpointID {
		t.Fatalf("fixture did not advance selected base: %+v err=%v", current, err)
	}
	recovered := startContextTurn(t, runner, second)
	state := requireStoredContextCheckpoint(t, backing, recovered.Turn.ContextCheckpointID)
	if state.Window.WorkingState == nil || state.Window.WorkingState.Version != 1 || state.Window.WorkingState.Source.CheckpointID != boundary.CheckpointID || len(provider.requests) != 3 {
		t.Fatalf("accepted retry switched to the newer active source: %+v calls=%d", state.Window.WorkingState, len(provider.requests))
	}
	later := requireStoredContextCheckpoint(t, backing, advanced.Turn.ContextCheckpointID)
	if later.Window.WorkingState.Version != 2 || later.Window.WorkingState.Goal != "separate later task state" {
		t.Fatal("historical recovery modified the other Turn's immutable state")
	}
	active, err := backing.GetActiveContextCheckpoint(t.Context(), sessionID)
	if err != nil || active.ID != later.ID {
		t.Fatalf("older accepted recovery replaced the newer active head instead of committing detached: %+v err=%v", active, err)
	}
	second.Context.WorkingState.Goal = "changed admitted state"
	if _, err = runner.StartAgentTurn(t.Context(), second); !errors.Is(err, harness.ErrConflict) || len(provider.requests) != 3 {
		t.Fatalf("changed accepted update bypassed original admission: %v calls=%d", err, len(provider.requests))
	}
}
