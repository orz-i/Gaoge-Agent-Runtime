package main

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

func TestTaskDraftReceiptScopeKnownAndOpaque(t *testing.T) {
	for _, opaque := range []bool{false, true} {
		name := "bounded"
		if opaque {
			name = "opaque"
		}
		t.Run(name, func(t *testing.T) {
			config := scriptedSettings(t)
			state := &taskState{store: harness.NewMemoryStore(), clock: &demoClock{now: time.Now().UTC()}, source: defaultSource(), sampleID: "scope-" + name}
			base := memory.NewStoreWithExecutionClock(state.clock)
			fault := &interruptStore{Store: base}
			var actual kernel.Snapshot
			var originalLease kernel.ExecutionLease
			var originalGrant kernel.ChildExecutionGrant
			observations := 0
			fault.observe = func(ctx context.Context, lease kernel.ExecutionLease, snapshot kernel.Snapshot) error {
				if err := observeDraftOwnerExclusion(ctx, base, state, lease, snapshot); err != nil {
					return err
				}
				observations++
				actual = snapshot
				originalLease = lease
				var err error
				originalGrant, err = base.LookupChildExecution(ctx, lease.RunID)
				return err
			}
			first, err := composeContextMode(fault, state, scriptedModel{}, config, "original-owner", harness.TranscriptSourceFunc(initialTranscript), opaque)
			if err != nil {
				t.Fatal(err)
			}
			initial, startErr := first.Prompt(t.Context(), hostTurnID, taskPrompt)
			if !fault.fired.Load() || fault.hits.Load() != 1 || observations != 1 || actual.Run.Status != kernel.RunStatusRunning || !originalGrant.MatchesExecution(originalLease) {
				t.Fatalf("receipt/fence cut missed: hits=%d observations=%d err=%v", fault.hits.Load(), observations, startErr)
			}
			// The actual third Model and non-idempotent handler have already returned.
			// Check the persisted Tool message against that handler's saved full answer,
			// original CallID and committed-event disposition, not just a call count.
			state.mu.Lock()
			drafts := append([]taskAnswer(nil), state.drafts...)
			executions := append([]toolExecution(nil), state.executions...)
			state.mu.Unlock()
			if len(drafts) != 1 || len(executions) != 3 || executions[2].key != draftKey || !executions[2].committed {
				t.Fatal("missing original physical draft execution")
			}
			var persisted struct {
				Messages []model.Message `json:"messages"`
			}
			if err = json.Unmarshal(actual.State, &persisted); err != nil {
				t.Fatal(err)
			}
			if len(persisted.Messages) == 0 {
				t.Fatal("missing durable Tool message")
			}
			message := persisted.Messages[len(persisted.Messages)-1]
			expectedContent, err := json.Marshal(drafts[0])
			if err != nil {
				t.Fatal(err)
			}
			expected := model.Message{Role: model.RoleTool, Content: model.TextParts(string(expectedContent)), ToolCallID: executions[2].callID}
			if !reflect.DeepEqual(message, expected) {
				t.Fatalf("durable full Tool result/source differs: %+v", message)
			}
			events, err := base.ListEvents(t.Context(), actual.Run.ID, actual.EventHead-1, 1)
			if err != nil || len(events) != 1 || events[0].Type != "tool.completed" || events[0].Message != "committed" {
				t.Fatalf("wrong fault journal: %v", err)
			}
			before := state.counts()
			committed := state.committedExecutions()
			if before.models != 3 || before.drafts != 1 || before.lookups != 1 || before.reads != 1 {
				t.Fatalf("wrong interruption sequence: %+v", before)
			}
			if opaque {
				if !errors.Is(startErr, errInterrupted) || !errors.Is(startErr, agent.ErrRootOutcomeUnknown) || onlyExpectedDraftInterruption(startErr, 0) {
					t.Fatalf("opaque error treated as known: %v", startErr)
				}
				original, err := base.Load(t.Context(), actual.Run.ID)
				if err != nil {
					t.Fatal(err)
				}
				grant, err := base.LookupChildExecution(t.Context(), actual.Run.ID)
				if err != nil || grant != originalGrant {
					t.Fatal("opaque scope retired")
				}
				state.clock.advance(leaseDuration + time.Second)
				reopened, err := composeContextMode(base, state, scriptedModel{}, config, "foreign-owner", nil, true)
				if err != nil {
					t.Fatal(err)
				}
				_, resumeErr := reopened.Resume(t.Context(), hostTurnID)
				if !errors.Is(resumeErr, agent.ErrRootOutcomeUnknown) {
					t.Fatalf("opaque expiry did not remain unknown: %v", resumeErr)
				}
				after, err := base.Load(t.Context(), actual.Run.ID)
				if err != nil || !reflect.DeepEqual(original, after) {
					t.Fatal("opaque retry changed original source")
				}
				grant, err = base.LookupChildExecution(t.Context(), actual.Run.ID)
				if err != nil || grant != originalGrant || state.counts() != before || !reflect.DeepEqual(committed, state.committedExecutions()) {
					t.Fatal("opaque retry changed grant/physical history")
				}
				return
			}
			if !onlyExpectedDraftInterruption(startErr, 0) {
				t.Fatalf("bounded interruption not precise: %v", startErr)
			}
			if _, err = base.LookupChildExecution(t.Context(), actual.Run.ID); !errors.Is(err, kernel.ErrNotFound) {
				t.Fatalf("known callback scope not retired: %v", err)
			}
			reopened, err := compose(base, state, scriptedModel{}, config, "fresh-owner", nil)
			if err != nil {
				t.Fatal(err)
			}
			completed, err := reopened.Resume(t.Context(), hostTurnID)
			if err != nil {
				t.Fatal(err)
			}
			runID, err := rootRunID(completed)
			if err != nil || runID != actual.Run.ID || completed.Turn.ID != initial.Turn.ID {
				t.Fatal("continuation changed original identity")
			}
			if _, err = verifyTask(t.Context(), state, config, completed); err != nil {
				t.Fatal(err)
			}
			after := state.counts()
			if after.models != 4 || after.tools != before.tools || after.drafts != 1 || !reflect.DeepEqual(committed, state.committedExecutions()) {
				t.Fatalf("fourth-model receipt continuation changed: %+v", after)
			}
			repeated, err := reopened.Resume(t.Context(), hostTurnID)
			if err != nil || repeated.Turn.Revision != completed.Turn.Revision || state.counts() != after {
				t.Fatalf("terminal Resume changed evidence: %v", err)
			}
		})
	}
}
func TestTaskRecoveryProfileDescribesActualInFlightObservation(t *testing.T) {
	profile := baselineProfile("openai")
	if profile.OracleVersion != "evidence-draft-recovery-v2" || profile.Recovery != "in-process-known-receipt-continuation" {
		t.Fatalf("stale recovery metadata: %+v", profile)
	}
}
