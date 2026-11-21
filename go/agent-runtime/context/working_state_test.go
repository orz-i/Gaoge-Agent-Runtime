package context_test

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	runtimectx "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/context"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

func workingStateFor(checkpoint runtimectx.Checkpoint, version int) *runtimectx.WorkingState {
	return &runtimectx.WorkingState{
		Version: version,
		Source: runtimectx.WorkingStateSource{
			CheckpointID: checkpoint.ID, ContentHash: checkpoint.ContentHash,
			CoveredThroughSourceID: checkpoint.CoveredThroughSourceID, CoveredPathHash: checkpoint.CoveredPathHash,
		},
		Goal: "HOST_DERIVED_TASK", Constraints: []string{"Keep the requested scope"},
		Completed: []string{"Read the source"}, Pending: []string{"Verify the result"},
		Decisions: []string{"Use the existing runtime"}, EvidenceRefs: []string{"host-fact:one"},
	}
}

func workingStateCheckpoint(t *testing.T, ids ...string) (*runtimectx.Manager, runtimectx.Checkpoint) {
	t.Helper()
	manager := runtimectx.NewManager(runtimectx.Dependencies{})
	request := openRequest(ids...)
	previous, err := manager.Open(t.Context(), request)
	requireNoError(t, err)
	request.Previous, request.WorkingState = &previous, workingStateFor(previous, 1)
	next, err := manager.Open(t.Context(), request)
	requireNoError(t, err)
	return manager, next
}

func assertOneWorkingStateProjection(t *testing.T, window runtimectx.Window) {
	t.Helper()
	messages := runtimectx.Materialize(window)
	count := 0
	for _, message := range messages {
		if strings.Contains(model.TextValue(message.Content), "HOST_DERIVED_TASK") {
			count++
			if message.Role != model.RoleUser {
				t.Fatal("WorkingState became trusted system content")
			}
		}
	}
	if count != 1 {
		t.Fatalf("expected one independent WorkingState projection, got %d", count)
	}
}

func TestWorkingStateNilPreservesLegacyWindowHashVector(t *testing.T) {
	manager := runtimectx.NewManager(runtimectx.Dependencies{CheckpointWriteProfile: runtimectx.CheckpointWriteLosslessJSON})
	checkpoint, err := manager.Open(t.Context(), openRequest("u1"))
	requireNoError(t, err)
	// SHA-256 of the pre-WorkingState canonical Window JSON for this fixture.
	const originalHash = "f24a47851b9adee648d4f1a0f31e8a858c7b360bad7f3a44d01eaf8ab7eaadfe"
	if checkpoint.ContentHash != originalHash || checkpoint.ContentHashEncoding != runtimectx.CheckpointContentHashEncodingLosslessJSON {
		t.Fatalf("nil WorkingState changed the lossless-safe Window fingerprint or encoding: %#v", checkpoint)
	}
	encoded, err := json.Marshal(checkpoint.Window)
	requireNoError(t, err)
	if strings.Contains(string(encoded), "workingState") {
		t.Fatal("nil WorkingState changed persisted Window JSON")
	}
}

func TestWorkingStateExplicitUpdateIsVersionedAndIsolatedFromHistory(t *testing.T) {
	manager := runtimectx.NewManager(runtimectx.Dependencies{CheckpointWriteProfile: runtimectx.CheckpointWriteLosslessJSON})
	request := openRequest("u1", "a1", "u2")
	previous, err := manager.Open(t.Context(), request)
	requireNoError(t, err)
	state := workingStateFor(previous, 1)
	request.Previous, request.WorkingState = &previous, state
	next, err := manager.Open(t.Context(), request)
	requireNoError(t, err)
	if next.Generation != previous.Generation+1 || next.Revision != 1 || next.ParentCheckpointID != previous.ID ||
		next.CacheIdentity == previous.CacheIdentity || next.ContentHash == previous.ContentHash ||
		next.Trace.Reason != "working_state_updated" ||
		next.ContentHashEncoding != runtimectx.CheckpointContentHashEncodingLosslessJSON || !runtimectx.ValidCheckpoint(next) {
		t.Fatalf("WorkingState update did not create a legal immutable boundary: %+v", next)
	}
	if !reflect.DeepEqual(next.Window.Entries, previous.Window.Entries) || next.Trace.SourceEntryCount != len(request.SourcePath) ||
		len(runtimectx.Materialize(next.Window)) != len(previous.Window.Entries)+2 {
		t.Fatal("WorkingState invented a transcript entry or source identity")
	}
	assertOneWorkingStateProjection(t, next.Window)
	state.Constraints[0] = "caller mutation"
	if next.Window.WorkingState.Constraints[0] == state.Constraints[0] || !runtimectx.ValidCheckpoint(next) {
		t.Fatal("submitted payload remained mutable through the caller")
	}
	copy := runtimectx.CloneCheckpoint(next)
	copy.Window.WorkingState.EvidenceRefs[0] = "foreign evidence"
	if !runtimectx.ValidCheckpoint(next) || runtimectx.ValidCheckpoint(copy) {
		t.Fatal("WorkingState clone leaked mutable fields or escaped checkpoint hashing")
	}
	request.Previous, request.WorkingState = &next, workingStateFor(next, 1)
	if _, err = manager.Open(t.Context(), request); !errors.Is(err, runtimectx.ErrLineageConflict) {
		t.Fatalf("same-version update was accepted: %v", err)
	}
	request.WorkingState.Version = 2
	updated, err := manager.Open(t.Context(), request)
	requireNoError(t, err)
	if updated.Window.WorkingState.Version != 2 || updated.Generation != next.Generation+1 {
		t.Fatal("explicit version advancement was not retained")
	}
}

func TestWorkingStateCarryAppendAndBranchReset(t *testing.T) {
	manager, previous := workingStateCheckpoint(t, "u1", "a1", "u2")
	request := openRequest("u1", "a1", "u2", "a2", "u3")
	request.Previous = &previous
	next, err := manager.Open(t.Context(), request)
	requireNoError(t, err)
	if next.Generation != previous.Generation || !reflect.DeepEqual(next.Window.WorkingState, previous.Window.WorkingState) {
		t.Fatal("ordinary source append rewrote or lost WorkingState")
	}
	assertOneWorkingStateProjection(t, next.Window)
	request.Previous = &next
	repeated, err := manager.Open(t.Context(), request)
	requireNoError(t, err)
	if repeated.ID != next.ID {
		t.Fatal("nil carry duplicated state or changed an unchanged checkpoint")
	}
	delta := openRequest("u4")
	delta.Previous, delta.SourceDelta = &next, true
	appended, err := manager.Open(t.Context(), delta)
	requireNoError(t, err)
	if !reflect.DeepEqual(appended.Window.WorkingState, next.Window.WorkingState) || len(appended.Window.Entries) != len(next.Window.Entries)+1 {
		t.Fatal("source-delta append dropped state or appended its projection to history")
	}
	assertOneWorkingStateProjection(t, appended.Window)
	for _, reset := range []struct {
		name string
		req  runtimectx.OpenRequest
	}{
		{"sibling", openRequest("other", "u2")},
		{"changed instructions", func() runtimectx.OpenRequest {
			r := openRequest("u1", "a1", "u2")
			r.Instructions = "new instructions"
			return r
		}()},
		{"changed static binding", func() runtimectx.OpenRequest {
			r := openRequest("u1", "a1", "u2")
			r.StaticFingerprint = runtimectx.StaticFingerprint("new")
			return r
		}()},
	} {
		t.Run(reset.name, func(t *testing.T) {
			reset.req.Previous = &previous
			cleared, err := manager.Open(t.Context(), reset.req)
			requireNoError(t, err)
			if cleared.Window.WorkingState != nil || cleared.Generation != previous.Generation+1 || cleared.CacheIdentity == previous.CacheIdentity {
				t.Fatal("incompatible branch/static reset reused the prior WorkingState prefix")
			}
			reset.req.WorkingState = workingStateFor(previous, 2)
			if _, err := manager.Open(t.Context(), reset.req); !errors.Is(err, runtimectx.ErrLineageConflict) {
				t.Fatalf("foreign branch/static update was accepted: %v", err)
			}
		})
	}
	request = openRequest("u1", "a1", "u2")
	request.Previous, request.ResetCacheIdentity = &previous, true
	resetCache, err := manager.Open(t.Context(), request)
	requireNoError(t, err)
	if !reflect.DeepEqual(resetCache.Window.WorkingState, previous.Window.WorkingState) {
		t.Fatal("a same-branch cache reset discarded task data")
	}
}

func TestWorkingStateRequiresExactCheckpointSource(t *testing.T) {
	manager, previous := workingStateCheckpoint(t, "u1")
	for name, mutate := range map[string]func(*runtimectx.WorkingState){
		"checkpoint":     func(s *runtimectx.WorkingState) { s.Source.CheckpointID = "ctxc_" + strings.Repeat("f", 32) },
		"content":        func(s *runtimectx.WorkingState) { s.Source.ContentHash = strings.Repeat("f", 64) },
		"covered source": func(s *runtimectx.WorkingState) { s.Source.CoveredThroughSourceID = "another-source" },
		"path":           func(s *runtimectx.WorkingState) { s.Source.CoveredPathHash = strings.Repeat("f", 64) },
	} {
		t.Run(name, func(t *testing.T) {
			request := openRequest("u1")
			request.Previous, request.WorkingState = &previous, workingStateFor(previous, 2)
			mutate(request.WorkingState)
			if _, err := manager.Open(t.Context(), request); !errors.Is(err, runtimectx.ErrLineageConflict) {
				t.Fatalf("unbound provenance was accepted: %v", err)
			}
		})
	}
	request := openRequest("u1")
	request.WorkingState = workingStateFor(previous, 1)
	if _, err := manager.Open(t.Context(), request); !errors.Is(err, runtimectx.ErrLineageConflict) {
		t.Fatalf("first checkpoint accepted unbound task state: %v", err)
	}
}

func TestWorkingStateCaptureAndCompactionKeepAnIndependentProjection(t *testing.T) {
	manager, previous := workingStateCheckpoint(t, "u1", "a1", "u2", "a2", "u3")
	messages := append(runtimectx.Materialize(previous.Window),
		model.Message{Role: model.RoleAssistant, ToolCalls: []tools.Call{{ID: "working-tool", ToolKey: "lookup", Arguments: json.RawMessage(`{}`)}}},
		model.Message{Role: model.RoleTool, ToolCallID: "working-tool", Content: model.TextParts(strings.Repeat("exact tool result ", 200))},
	)
	captured, err := manager.Capture(t.Context(), runtimectx.CaptureRequest{
		Previous: previous, StaticFingerprint: previous.StaticFingerprint, RunID: "working-run", Messages: messages,
	})
	requireNoError(t, err)
	if len(captured.Window.Entries) != len(previous.Window.Entries)+2 || !reflect.DeepEqual(captured.Window.WorkingState, previous.Window.WorkingState) {
		t.Fatal("Capture counted the protected projection as runtime history")
	}
	assertOneWorkingStateProjection(t, captured.Window)
	portable, err := manager.CompactPortable(runtimectx.PortableCompactionRequest{
		Previous: captured, RunID: "working-run", Messages: runtimectx.Materialize(captured.Window),
		Policy: runtimectx.Policy{PreserveRecentTurns: 1, MaxCompactionTokens: 128},
	})
	requireNoError(t, err)
	if portable.RemovedMessages != 4 || !reflect.DeepEqual(portable.Window.WorkingState, captured.Window.WorkingState) {
		t.Fatal("portable compaction counted WorkingState as an ordinary user turn")
	}
	assertOneWorkingStateProjection(t, portable.Window)
	for _, artifact := range portable.Artifacts {
		if strings.Contains(string(artifact.ContentJSON), "HOST_DERIVED_TASK") {
			t.Fatal("WorkingState was archived as transcript evidence")
		}
	}
	pressure, err := manager.CompactToolPressure(captured, "working-run", runtimectx.Materialize(captured.Window))
	requireNoError(t, err)
	if !reflect.DeepEqual(pressure.Window.WorkingState, captured.Window.WorkingState) || len(pressure.Window.Entries) != len(captured.Window.Entries) {
		t.Fatal("Tool pressure compaction lost or duplicated the independent state")
	}
	assertOneWorkingStateProjection(t, pressure.Window)
	portable.Window.WorkingState = nil // A rebuilding caller may omit the field; rollover carries it safely.
	rolled, err := manager.Rollover(t.Context(), runtimectx.RolloverRequest{
		Previous: captured, Window: portable.Window, Artifacts: portable.Artifacts, Reason: "soft_limit",
	})
	requireNoError(t, err)
	assertOneWorkingStateProjection(t, rolled.Window)
	if !runtimectx.ValidCheckpoint(rolled) || len(rolled.ArtifactIDs) == 0 {
		t.Fatal("rollover lost task data or its exact archive lineage")
	}
	cacheReset := openRequest("u1", "a1", "u2", "a2", "u3")
	cacheReset.Previous, cacheReset.ResetCacheIdentity = &rolled, true
	reset, err := manager.Open(t.Context(), cacheReset)
	requireNoError(t, err)
	if !reflect.DeepEqual(reset.ArtifactIDs, rolled.ArtifactIDs) || !reflect.DeepEqual(reset.Window.WorkingState, rolled.Window.WorkingState) {
		t.Fatal("same-branch cache reset lost independent state or its archived evidence")
	}
	rebound, err := manager.BindModelWindow(t.Context(), rolled, "first-window")
	requireNoError(t, err)
	rebound, err = manager.BindModelWindow(t.Context(), rebound, "changed-window")
	requireNoError(t, err)
	assertOneWorkingStateProjection(t, rebound.Window)
	mutated := runtimectx.Materialize(captured.Window)
	mutated[1].Content = model.TextParts("replaced task state")
	if _, err = manager.Capture(t.Context(), runtimectx.CaptureRequest{
		Previous: captured, StaticFingerprint: captured.StaticFingerprint, RunID: "working-run", Messages: mutated,
	}); !errors.Is(err, runtimectx.ErrLineageConflict) {
		t.Fatalf("Capture rewrote the protected state prefix: %v", err)
	}
}

func TestWorkingStateRolloverValidatesUpdatesAndStaticChanges(t *testing.T) {
	manager, previous := workingStateCheckpoint(t, "u1")
	window := runtimectx.CloneWindow(previous.Window)
	window.WorkingState = workingStateFor(previous, 2)
	window.WorkingState.Source.CoveredPathHash = strings.Repeat("f", 64)
	if _, err := manager.Rollover(t.Context(), runtimectx.RolloverRequest{
		Previous: previous, Window: window, Reason: "host_update",
	}); !errors.Is(err, runtimectx.ErrLineageConflict) {
		t.Fatalf("rollover accepted an update from a different source: %v", err)
	}
	window.WorkingState = workingStateFor(previous, 2)
	next, err := manager.Rollover(t.Context(), runtimectx.RolloverRequest{
		Previous: previous, Window: window, Reason: "host_update",
	})
	requireNoError(t, err)
	if next.Window.WorkingState.Version != 2 || next.CacheIdentity == previous.CacheIdentity || next.Generation != previous.Generation+1 {
		t.Fatal("rollover did not freeze an explicit update at a new generation")
	}
	window = runtimectx.CloneWindow(previous.Window)
	window.Instructions = "changed static instructions"
	if _, err := manager.Rollover(t.Context(), runtimectx.RolloverRequest{
		Previous: previous, Window: window, Reason: "static_reset",
	}); !errors.Is(err, runtimectx.ErrLineageConflict) {
		t.Fatalf("rollover accepted inherited state with changed static instructions: %v", err)
	}
	window.WorkingState = nil
	reset, err := manager.Rollover(t.Context(), runtimectx.RolloverRequest{
		Previous: previous, Window: window, Reason: "static_reset",
	})
	requireNoError(t, err)
	if reset.Window.WorkingState != nil || reset.CacheIdentity == previous.CacheIdentity {
		t.Fatal("static reset carried the prior protected state")
	}
}

func TestWorkingStateValidationAndBudgetBounds(t *testing.T) {
	manager := runtimectx.NewManager(runtimectx.Dependencies{})
	previous, err := manager.Open(t.Context(), openRequest("u1"))
	requireNoError(t, err)
	base := workingStateFor(previous, 1)
	for name, mutate := range map[string]func(*runtimectx.WorkingState){
		"zero version": func(s *runtimectx.WorkingState) { s.Version = 0 },
		"large version": func(s *runtimectx.WorkingState) {
			v := int64(runtimectx.MaxWorkingStateVersion)
			s.Version = int(v + 1)
		},
		"bad UTF8":     func(s *runtimectx.WorkingState) { s.Pending = []string{string([]byte{0xff})} },
		"large text":   func(s *runtimectx.WorkingState) { s.Goal = strings.Repeat("x", runtimectx.MaxWorkingStateTextBytes+1) },
		"many entries": func(s *runtimectx.WorkingState) { s.Decisions = make([]string, runtimectx.MaxWorkingStateItems+1) },
		"large payload": func(s *runtimectx.WorkingState) {
			s.Constraints = make([]string, 8)
			for i := range s.Constraints {
				s.Constraints[i] = strings.Repeat("x", runtimectx.MaxWorkingStateTextBytes)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			value := runtimectx.CloneWorkingState(base)
			mutate(value)
			if err := runtimectx.ValidateWorkingState(value); !errors.Is(err, runtimectx.ErrInvalidInput) {
				t.Fatalf("invalid bounded task state accepted: %v", err)
			}
		})
	}
	request := openRequest("u1")
	request.Previous, request.WorkingState = &previous, base
	withState, err := manager.Open(t.Context(), request)
	requireNoError(t, err)
	window := runtimectx.ModelWindow{ContextTokens: 512}
	oldBudget, err := manager.AssessModelRequest(t.Context(), model.Request{RunID: "bounded", Messages: runtimectx.Materialize(previous.Window)}, window, runtimectx.Policy{})
	requireNoError(t, err)
	newBudget, err := manager.AssessModelRequest(t.Context(), model.Request{RunID: "bounded", Messages: runtimectx.Materialize(withState.Window)}, window, runtimectx.Policy{})
	requireNoError(t, err)
	if !runtimectx.WithinHardBudget(oldBudget, runtimectx.Policy{}) || runtimectx.WithinHardBudget(newBudget, runtimectx.Policy{}) || newBudget.SerializedBytes <= oldBudget.SerializedBytes {
		t.Fatal("WorkingState escaped actual model input budget accounting")
	}
}
