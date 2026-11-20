package continuatione2e

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	harnesspostgres "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness-postgres"
	postgresadapter "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-postgres"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	runtimecontext "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/context"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"gorm.io/gorm"
)

// The same original invoice oracle also runs without WorkingState through the
// explicit and convenience constructors. This case adds a real previous Turn
// and Host-authored state without changing the business effect or crash point.
func TestRealPostgresManagedAgentWorkingStateRecoveryAcrossProcessCrash(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	testManagedAgentSessionRecoveryAcrossProcessCrash(t, dsn, true, true)
}

type managedWorkingStateWarmupModel struct{}

func (managedWorkingStateWarmupModel) Generate(context.Context, model.Request) (model.Response, error) {
	return model.Response{Content: "invoice task ready"}, nil
}

func prepareManagedRecoveryWorkingState(t *testing.T, db *gorm.DB, files string) *runtimecontext.WorkingState {
	t.Helper()
	store, err := harnesspostgres.New(db)
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := kernel.New(kernel.Dependencies{Store: postgresadapter.NewKernelStore(db), ManagedRecoveryHost: store})
	if err != nil {
		t.Fatal(err)
	}
	options := managedRecoveryConfiguredSessionOptions(false)
	options.Transcript = harness.TranscriptSourceFunc(func(_ context.Context, _, _ harness.HostRef, prompt string) (*harness.ContextSeed, error) {
		return &harness.ContextSeed{
			SourcePath: []string{"old-user"},
			Entries: []runtimecontext.Entry{{
				ID: "old-user", SourceID: "old-user", TurnID: "old",
				Message: model.Message{Role: model.RoleUser, Content: model.TextParts(prompt)},
			}},
		}, nil
	})
	session, err := harness.OpenAgentSession(harness.AgentSessionOptions{
		Runtime: runtime, Store: store, Model: managedWorkingStateWarmupModel{},
		Tools: managedRecoveryToolRegistrations(files), Session: options,
		Context: harness.AgentSessionContextOptions{
			ManagedRecovery: true,
			ResolveWindow: func(context.Context, model.Request) (runtimecontext.ModelWindow, error) {
				return runtimecontext.ModelWindow{ContextTokens: 12000, ReservedOutputTokens: 512}, nil
			},
		},
		Execution: agent.ExecutionPolicy{
			Model: agent.CallPolicy{Timeout: time.Second, MaxAttempts: 1, MaxConcurrency: 1},
			Tool:  agent.CallPolicy{Timeout: time.Second, MaxAttempts: 1, MaxConcurrency: 1},
		},
		RunExecution: &agent.RunExecution{OwnerID: "working-state-warmup", LeaseDuration: managedRecoveryLeaseDuration},
	})
	if err != nil {
		t.Fatal(err)
	}
	warmup, err := session.Prompt(t.Context(), "old", "Remember the invoice terms.")
	if err != nil || warmup.Turn.Status != harness.TurnCompleted {
		t.Fatalf("WorkingState source Turn did not complete: %+v err=%v", warmup.Turn, err)
	}
	boundary, err := session.ResolveContextSourceBoundaryForPath(t.Context(), "old", "", []string{"old-user", "old-answer", "invoice-user"})
	if err != nil {
		t.Fatal(err)
	}
	previous, err := store.GetContextCheckpoint(t.Context(), boundary.CheckpointID)
	if err != nil || !runtimecontext.ValidCheckpoint(previous) ||
		boundary.ContentHash != previous.ContentHash || boundary.CoveredPathHash != previous.CoveredPathHash ||
		boundary.CoveredThroughSourceID != "old-user" || !runtimecontext.SourceAlignedCheckpoint(previous) {
		t.Fatalf("Host did not resolve the actual source-aligned previous checkpoint: %+v err=%v", boundary, err)
	}
	state := &runtimecontext.WorkingState{
		Version: 1,
		Source: runtimecontext.WorkingStateSource{
			CheckpointID: boundary.CheckpointID, ContentHash: boundary.ContentHash,
			CoveredThroughSourceID: boundary.CoveredThroughSourceID, CoveredPathHash: boundary.CoveredPathHash,
		},
		Goal:         "invoice-working-state-v1: recover shipping and report the total",
		Constraints:  []string{"Keep all amounts in exact cents"},
		Completed:    []string{"The Host selected the invoice task"},
		Pending:      []string{"Record the subtotal once", "Retrieve the original archived shipping evidence"},
		Decisions:    []string{"Use the original invoice evidence instead of inventing missing amounts"},
		EvidenceRefs: []string{"source:old-user"},
	}
	writeManagedRecoveryWorkingState(t, files, state)
	return state
}

func writeManagedRecoveryWorkingState(t *testing.T, files string, state *runtimecontext.WorkingState) {
	t.Helper()
	raw, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(files, "working-state.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
}

func managedRecoveryWorkingStateTranscript(t *testing.T, files string, source harness.TranscriptSource) harness.TranscriptSource {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(files, "working-state.json"))
	if errors.Is(err, os.ErrNotExist) {
		return source
	}
	if err != nil {
		t.Fatal(err)
	}
	var state runtimecontext.WorkingState
	if err = json.Unmarshal(raw, &state); err != nil {
		t.Fatal(err)
	}
	return harness.TranscriptSourceFunc(func(ctx context.Context, thread, turn harness.HostRef, prompt string) (*harness.ContextSeed, error) {
		seed, err := source.Seed(ctx, thread, turn, prompt)
		if err != nil {
			return nil, err
		}
		seed.WorkingState = runtimecontext.CloneWorkingState(&state)
		return seed, nil
	})
}

func assertManagedRecoveryWorkingState(t *testing.T, store harness.Store, checkpoint runtimecontext.Checkpoint, expected *runtimecontext.WorkingState) {
	t.Helper()
	stored, err := store.GetContextCheckpoint(t.Context(), checkpoint.ID)
	if err != nil || !runtimecontext.ValidCheckpoint(stored) || !reflect.DeepEqual(stored.Window.WorkingState, expected) {
		t.Fatalf("committed typed WorkingState changed: checkpoint=%s state=%+v err=%v", checkpoint.ID, stored.Window.WorkingState, err)
	}
	if expected == nil {
		return
	}
	previous, err := store.GetContextCheckpoint(t.Context(), expected.Source.CheckpointID)
	if err != nil || !runtimecontext.ValidCheckpoint(previous) || previous.ContentHash != expected.Source.ContentHash ||
		previous.CoveredThroughSourceID != expected.Source.CoveredThroughSourceID || previous.CoveredPathHash != expected.Source.CoveredPathHash ||
		stored.Generation <= previous.Generation {
		t.Fatalf("WorkingState lost exact source binding or update rollover: source=%+v err=%v", expected.Source, err)
	}
	for _, entry := range stored.Window.Entries {
		if strings.Contains(model.TextValue(entry.Message.Content), expected.Goal) {
			t.Fatal("typed WorkingState was merged into an ordinary transcript Entry")
		}
	}
}

func assertManagedRecoveryWorkingStateProjection(t *testing.T, request model.Request, expected *runtimecontext.WorkingState) {
	t.Helper()
	if strings.Contains(requestMessages(request), "unadmitted replacement working state") {
		t.Fatal("recovery re-read unadmitted Host WorkingState")
	}
	if expected == nil {
		return
	}
	raw, err := json.Marshal(expected)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, message := range request.Messages {
		if strings.Contains(model.TextValue(message.Content), expected.Goal) {
			count++
			if message.Role != model.RoleUser || !strings.Contains(model.TextValue(message.Content), string(raw)) {
				t.Fatal("WorkingState projection lost exact data or gained non-user authority")
			}
		}
	}
	if count != 1 {
		t.Fatalf("WorkingState must be projected once per frozen request: got=%d", count)
	}
}
