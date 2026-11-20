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

func TestContextBaseFreezesNilCarryAcrossAcceptedCheckpointFailure(t *testing.T) {
	for _, mode := range []string{"existing V1", "existing nil", "no previous checkpoint", "explicit source delta"} {
		t.Run(mode, func(t *testing.T) {
			backing := harness.NewMemoryStore()
			store := &workingStateCheckpointFaultStore{Store: backing}
			provider := &multiContextCaptureModel{}
			runner, _ := newResumeTurnHarness(t, memory.NewStore(), store, provider, newResumeTurnClock(), nil)
			options := resumeTurnSessionOptions()
			common := harness.StartRequest{HostThread: options.HostThread, Actor: options.Actor, Thread: options.Thread, Config: options.Config}
			var original *runtimecontext.WorkingState
			pending := common
			pending.HostTurn = harness.HostRef{Kind: testContextHostKind, ID: "pending"}
			pending.Goal = "pending prompt"
			pending.Context = workingStateHistory("first", "pending")
			var boundary harness.ContextSourceBoundary
			if mode != "no previous checkpoint" {
				first := common
				first.HostTurn, first.Goal, first.Context = harness.HostRef{Kind: testContextHostKind, ID: "first"}, "first prompt", workingStateHistory("first")
				startContextTurn(t, runner, first)
				var err error
				boundary, err = runner.ResolveContextSourceBoundaryForPath(t.Context(), common.HostThread, common.Config, "", pending.Context.SourcePath)
				if err != nil {
					t.Fatal(err)
				}
				if mode == "existing V1" || mode == "explicit source delta" {
					initial := common
					initial.HostTurn, initial.Goal = harness.HostRef{Kind: testContextHostKind, ID: "initial-state"}, "record initial state"
					initial.Context = &harness.ContextSeed{SourceDelta: true, BaseCheckpointID: boundary.CheckpointID, WorkingState: stateFromContextBoundary(boundary, 1)}
					initial.Context.WorkingState.Goal = "ORIGINAL_V1"
					startContextTurn(t, runner, initial)
					original = runtimecontext.CloneWorkingState(initial.Context.WorkingState)
					boundary, err = runner.ResolveContextSourceBoundaryForPath(t.Context(), common.HostThread, common.Config, "", pending.Context.SourcePath)
					if err != nil {
						t.Fatal(err)
					}
				}
				if mode == "explicit source delta" {
					pending.Context = &harness.ContextSeed{
						SourceDelta: true, BaseCheckpointID: boundary.CheckpointID,
						SourcePath: append([]string(nil), pending.Context.SourcePath[1:]...),
						Entries:    runtimecontext.CloneEntries(pending.Context.Entries[1:]),
					}
				}
			} else {
				pending.Context = workingStateHistory("pending")
			}
			scopeID, err := harness.SessionID(common.HostThread)
			if err != nil {
				t.Fatal(err)
			}
			store.target, err = harness.TurnID(scopeID, pending.HostTurn)
			if err != nil {
				t.Fatal(err)
			}
			store.armed = true
			callsBefore := len(provider.requests)
			if _, err := runner.StartAgentTurn(t.Context(), pending); !errors.Is(err, errWorkingStateCheckpointCommit) || len(provider.requests) != callsBefore {
				t.Fatalf("expected Accepted/no-checkpoint failure before generation: %v calls=%d", err, len(provider.requests))
			}
			admitted, err := backing.GetTurn(t.Context(), store.target)
			if err != nil || admitted.Status != harness.TurnAccepted || admitted.ContextCheckpointID != "" {
				t.Fatalf("original admission missing: %+v err=%v", admitted, err)
			}
			sealed, err := backing.GetConfigSnapshot(t.Context(), admitted.ConfigSnapshotID)
			if err != nil || sealed.ContextBase == nil || pending.Context.WorkingState != nil {
				t.Fatalf("nil carry became an explicit update or lost its frozen open plan: %v", err)
			}
			if mode == "no previous checkpoint" {
				if sealed.ContextBase.Source != nil || sealed.ContextBase.ExpectedHeadCheckpointID != "" {
					t.Fatal("first admission did not freeze known no previous checkpoint")
				}
				futureBase := common
				futureBase.HostTurn, futureBase.Goal, futureBase.Context = harness.HostRef{Kind: testContextHostKind, ID: "future-base"}, pending.Goal, pending.Context
				startContextTurn(t, runner, futureBase)
				boundary, err = runner.ResolveContextSourceBoundaryForPath(t.Context(), common.HostThread, common.Config, "", pending.Context.SourcePath)
				if err != nil {
					t.Fatal(err)
				}
			}
			future := common
			future.HostTurn, future.Goal = harness.HostRef{Kind: testContextHostKind, ID: "future-state"}, "record later state"
			version := 1
			if original != nil {
				version = original.Version + 1
			}
			future.Context = &harness.ContextSeed{SourceDelta: true, BaseCheckpointID: boundary.CheckpointID, WorkingState: stateFromContextBoundary(boundary, version)}
			future.Context.WorkingState.Goal = "FUTURE_STATE"
			advanced := startContextTurn(t, runner, future)
			callsBefore = len(provider.requests)
			recovered := startContextTurn(t, runner, pending)
			checkpoint := requireStoredContextCheckpoint(t, backing, recovered.Turn.ContextCheckpointID)
			if !reflect.DeepEqual(checkpoint.Window.WorkingState, original) || len(provider.requests) != callsBefore+1 || recovered.Config.ID != sealed.ID {
				t.Fatalf("same admitted nil seed adopted future state or config: state=%+v calls=%d", checkpoint.Window.WorkingState, len(provider.requests))
			}
			if original != nil && sealed.ContextBase.Source != nil {
				base := requireStoredContextCheckpoint(t, backing, sealed.ContextBase.Source.CheckpointID)
				if checkpoint.Generation != base.Generation {
					t.Fatal("nil carry was converted into an explicit version/generation update")
				}
			}
			for _, message := range provider.requests[len(provider.requests)-1].Messages {
				if strings.Contains(model.TextValue(message.Content), "FUTURE_STATE") {
					t.Fatal("unadmitted future WorkingState reached the original model invocation")
				}
			}
			active, err := backing.GetActiveContextCheckpoint(t.Context(), scopeID)
			if err != nil || active.ID != advanced.Turn.ContextCheckpointID {
				t.Fatalf("retry overwrote future active head rather than committing detached: %+v err=%v", active, err)
			}
			startContextTurn(t, runner, pending)
			if len(provider.requests) != callsBefore+1 {
				t.Fatal("completed original retry generated twice")
			}
		})
	}
}

func TestContextBaseFreezesFullPathStaticResetPrevious(t *testing.T) {
	f := newSourceDeltaHarnessFixture(t, "context-base-static-reset")
	first := f.common
	first.HostTurn, first.Goal, first.Context = harness.HostRef{Kind: testContextHostKind, ID: "first"}, "first prompt", workingStateHistory("first")
	old := startContextTurn(t, f.runner, first)
	next := f.common
	next.HostTurn, next.Goal, next.Context = harness.HostRef{Kind: testContextHostKind, ID: "reset"}, "reset prompt", workingStateHistory("reset")
	next.Config.Instructions = "different static prefix"
	result := startContextTurn(t, f.runner, next)
	bound := result.Config.ContextBase
	checkpoint := requireStoredContextCheckpoint(t, f.store, result.Turn.ContextCheckpointID)
	if bound == nil || bound.Source == nil || bound.SourceDelta || bound.Source.CheckpointID != old.Turn.ContextCheckpointID ||
		checkpoint.ParentCheckpointID != old.Turn.ContextCheckpointID || checkpoint.Generation != old.Turn.ContextRef.Generation+1 {
		t.Fatal("full-path static reset lost its original active Previous or treated it as a same-static source delta")
	}
}

type missingOriginalContextConfigStore struct {
	harness.Store
	missingID string
	selects   int
	writes    int
}

func (s *missingOriginalContextConfigStore) GetConfigSnapshot(ctx context.Context, id string) (harness.ConfigSnapshot, error) {
	if id == s.missingID {
		return harness.ConfigSnapshot{}, harness.ErrNotFound
	}
	return s.Store.GetConfigSnapshot(ctx, id)
}
func (s *missingOriginalContextConfigStore) GetActiveContextCheckpoint(ctx context.Context, scope string) (runtimecontext.Checkpoint, error) {
	s.selects++
	return s.Store.GetActiveContextCheckpoint(ctx, scope)
}
func (s *missingOriginalContextConfigStore) PutConfigSnapshot(ctx context.Context, config harness.ConfigSnapshot) (harness.ConfigSnapshot, bool, error) {
	s.writes++
	return s.Store.PutConfigSnapshot(ctx, config)
}

func TestContextBaseMissingOriginalConfigNeverReadmitsExistingTurn(t *testing.T) {
	backing := harness.NewMemoryStore()
	store := &missingOriginalContextConfigStore{Store: backing}
	provider := &multiContextCaptureModel{}
	runner, _ := newResumeTurnHarness(t, memory.NewStore(), store, provider, newResumeTurnClock(), nil)
	options := resumeTurnSessionOptions()
	request := harness.StartRequest{HostThread: options.HostThread, HostTurn: harness.HostRef{Kind: testContextHostKind, ID: "first"}, Actor: options.Actor, Thread: options.Thread, Config: options.Config, Goal: "first prompt", Context: workingStateHistory("first")}
	first := startContextTurn(t, runner, request)
	store.missingID = first.Config.ID
	selects, writes, calls := store.selects, store.writes, len(provider.requests)
	if _, err := runner.StartAgentTurn(t.Context(), request); !errors.Is(err, harness.ErrConflict) {
		t.Fatalf("missing immutable original config became new admission: %v", err)
	}
	if store.selects != selects || store.writes != writes || len(provider.requests) != calls {
		t.Fatal("missing original config caused new source selection, config persistence or model execution")
	}
}

var errCaptureLegacyContextTurn = errors.New("capture original Turn before admission")

type legacyContextAdmissionStore struct {
	harness.Store
	target   string
	captured *harness.Turn
	armed    bool
}

func (s *legacyContextAdmissionStore) CreateTurn(ctx context.Context, turn harness.Turn) (harness.Turn, bool, error) {
	if s.armed && turn.ID == s.target {
		s.armed = false
		copy := turn
		s.captured = &copy
		return harness.Turn{}, false, errCaptureLegacyContextTurn
	}
	return s.Store.CreateTurn(ctx, turn)
}

// Install a canonical pre-binding admission using the real admission envelope.
// The intercepted new Turn was never persisted, so this does not mutate any
// immutable stored Config or Turn fact.
func installLegacyContextAdmission(t *testing.T, runner *harness.Runner, store *legacyContextAdmissionStore, backing *harness.MemoryStore, request harness.StartRequest) harness.ConfigSnapshot {
	t.Helper()
	scopeID, err := harness.SessionID(request.HostThread)
	if err != nil {
		t.Fatal(err)
	}
	store.target, err = harness.TurnID(scopeID, request.HostTurn)
	if err != nil {
		t.Fatal(err)
	}
	store.armed = true
	if _, err := runner.StartAgentTurn(t.Context(), request); !errors.Is(err, errCaptureLegacyContextTurn) || store.captured == nil {
		t.Fatalf("did not capture an unpersisted original admission: %v", err)
	}
	turn := *store.captured
	config, err := backing.GetConfigSnapshot(t.Context(), turn.ConfigSnapshotID)
	if err != nil || config.ContextBase == nil {
		t.Fatalf("new admission did not persist its derived plan: %v", err)
	}
	config.ContextBase = nil
	legacy, err := harness.SealConfigSnapshot(turn.ID, config, config.CreatedAt)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := backing.PutConfigSnapshot(t.Context(), legacy); err != nil {
		t.Fatal(err)
	}
	turn.ConfigSnapshotID = legacy.ID
	if _, created, err := backing.CreateTurn(t.Context(), turn); err != nil || !created {
		t.Fatalf("could not install a canonical legacy Accepted Turn: created=%v err=%v", created, err)
	}
	return legacy
}

func TestContextBaseLegacyAdmissionCompatibility(t *testing.T) {
	for _, mode := range []string{"full path without later state", "full path with later state", "explicit delta with later state"} {
		t.Run(mode, func(t *testing.T) {
			backing := harness.NewMemoryStore()
			store := &legacyContextAdmissionStore{Store: backing}
			provider := &multiContextCaptureModel{}
			runner, _ := newResumeTurnHarness(t, memory.NewStore(), store, provider, newResumeTurnClock(), nil)
			options := resumeTurnSessionOptions()
			common := harness.StartRequest{HostThread: options.HostThread, Actor: options.Actor, Thread: options.Thread, Config: options.Config}
			first := common
			first.HostTurn, first.Goal, first.Context = harness.HostRef{Kind: testContextHostKind, ID: "first"}, "first prompt", workingStateHistory("first")
			startContextTurn(t, runner, first)
			pending := common
			pending.HostTurn, pending.Goal, pending.Context = harness.HostRef{Kind: testContextHostKind, ID: "legacy-pending"}, "pending prompt", workingStateHistory("first", "pending")
			boundary, err := runner.ResolveContextSourceBoundaryForPath(t.Context(), common.HostThread, common.Config, "", pending.Context.SourcePath)
			if err != nil {
				t.Fatal(err)
			}
			var original *runtimecontext.WorkingState
			if mode == "explicit delta with later state" {
				initial := common
				initial.HostTurn, initial.Goal = harness.HostRef{Kind: testContextHostKind, ID: "initial-state"}, "initial state"
				initial.Context = &harness.ContextSeed{SourceDelta: true, BaseCheckpointID: boundary.CheckpointID, WorkingState: stateFromContextBoundary(boundary, 1)}
				initial.Context.WorkingState.Goal = "ORIGINAL_LEGACY_V1"
				startContextTurn(t, runner, initial)
				original = runtimecontext.CloneWorkingState(initial.Context.WorkingState)
				boundary, err = runner.ResolveContextSourceBoundaryForPath(t.Context(), common.HostThread, common.Config, "", pending.Context.SourcePath)
				if err != nil {
					t.Fatal(err)
				}
				pending.Context = &harness.ContextSeed{SourceDelta: true, BaseCheckpointID: boundary.CheckpointID, SourcePath: append([]string(nil), pending.Context.SourcePath[1:]...), Entries: runtimecontext.CloneEntries(pending.Context.Entries[1:])}
			}
			legacy := installLegacyContextAdmission(t, runner, store, backing, pending)
			admitted, err := backing.GetTurn(t.Context(), store.target)
			if err != nil || admitted.Status != harness.TurnAccepted || admitted.ContextCheckpointID != "" {
				t.Fatalf("expected a legacy Accepted Turn without a checkpoint: %+v err=%v", admitted, err)
			}
			advance := func() {
				t.Helper()
				future := common
				future.HostTurn, future.Goal = harness.HostRef{Kind: testContextHostKind, ID: "future-state"}, "later state"
				version := 1
				if original != nil {
					version = original.Version + 1
				}
				future.Context = &harness.ContextSeed{SourceDelta: true, BaseCheckpointID: boundary.CheckpointID, WorkingState: stateFromContextBoundary(boundary, version)}
				future.Context.WorkingState.Goal = "FUTURE_LEGACY_STATE"
				startContextTurn(t, runner, future)
			}
			if mode != "full path without later state" {
				advance()
			}
			calls := len(provider.requests)
			if mode == "full path with later state" {
				if _, err := runner.StartAgentTurn(t.Context(), pending); !errors.Is(err, harness.ErrConflict) {
					t.Fatalf("uncertain legacy nil carry adopted later state instead of failing closed: %v", err)
				}
				retained, err := backing.GetTurn(t.Context(), store.target)
				if err != nil || retained.Status != harness.TurnAccepted || retained.ContextCheckpointID != "" || retained.ConfigSnapshotID != legacy.ID || len(provider.requests) != calls {
					t.Fatalf("failed legacy retry changed admission or executed: %+v err=%v calls=%d", retained, err, len(provider.requests))
				}
				return
			}
			recovered := startContextTurn(t, runner, pending)
			checkpoint := requireStoredContextCheckpoint(t, backing, recovered.Turn.ContextCheckpointID)
			if recovered.Config.ID != legacy.ID || recovered.Config.ContextBase != nil || !reflect.DeepEqual(checkpoint.Window.WorkingState, original) || len(provider.requests) != calls+1 {
				t.Fatalf("legacy admission changed its hash, explicit delta or original nil state: %+v calls=%d", checkpoint.Window.WorkingState, len(provider.requests))
			}
			if mode == "full path without later state" {
				advance()
				calls = len(provider.requests)
				replayed := startContextTurn(t, runner, pending)
				if replayed.Turn.ContextCheckpointID != checkpoint.ID || replayed.Config.ID != legacy.ID || len(provider.requests) != calls {
					t.Fatal("legacy admission with an existing checkpoint was rebuilt from later state")
				}
			}
		})
	}
}
