package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

var errInterrupted = errors.New("task-agent: simulated host interruption after committed draft receipt")

// This is a fault-test clock, not permission to override a production Store's
// time. A real recovery must wait for its Store-authoritative lease expiry.
type demoClock struct {
	mu  sync.Mutex
	now time.Time
}

func (clock *demoClock) Now() time.Time {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	return clock.now
}

func (clock *demoClock) advance(duration time.Duration) {
	clock.mu.Lock()
	clock.now = clock.now.Add(duration)
	clock.mu.Unlock()
}

type interruptStore struct {
	*memory.Store
	fired atomic.Bool
}

func (store *interruptStore) ApplyExecution(ctx context.Context, lease kernel.ExecutionLease, revision uint64, mutation kernel.StoreMutation) (kernel.Snapshot, error) {
	snapshot, err := store.Store.ApplyExecution(ctx, lease, revision, mutation)
	if err != nil {
		return snapshot, err
	}
	for _, event := range mutation.Events {
		if event.Type == "tool.completed" && event.Message == "committed" && store.fired.CompareAndSwap(false, true) {
			return snapshot, errInterrupted
		}
	}
	return snapshot, nil
}

type taskReport struct {
	Mode              string     `json:"mode"`
	Model             string     `json:"model"`
	Status            string     `json:"status"`
	TurnID            string     `json:"turnID"`
	RunID             string     `json:"runID"`
	ContextGeneration int        `json:"contextGeneration"`
	ModelCalls        int        `json:"modelCalls"`
	LookupCalls       int        `json:"lookupCalls"`
	ArtifactReads     int        `json:"artifactReads"`
	DraftWrites       int        `json:"draftWrites"`
	Answer            taskAnswer `json:"answer"`
}

type callCounts struct {
	models, lookups, reads, drafts int
}

func (state *taskState) counts() callCounts {
	state.mu.Lock()
	defer state.mu.Unlock()
	return callCounts{len(state.requests), state.lookupCalls, len(state.reads), len(state.drafts)}
}

func runTask(ctx context.Context, config settings, newModel func() (model.Client, error), source sourceFixture) (taskReport, error) {
	state := &taskState{store: harness.NewMemoryStore(), clock: &demoClock{now: time.Now().UTC()}, source: source}
	base := memory.NewStoreWithExecutionClock(state.clock)
	fault := &interruptStore{Store: base}
	client, err := newModel()
	if err != nil {
		return taskReport{}, err
	}
	session, err := compose(fault, state, client, config, "task-owner-before", harness.TranscriptSourceFunc(initialTranscript))
	if err != nil {
		return taskReport{}, err
	}
	_, startErr := session.Prompt(ctx, hostTurnID, taskPrompt)
	if !errors.Is(startErr, errInterrupted) || !fault.fired.Load() {
		return taskReport{}, fmt.Errorf("task did not reach the committed-draft interruption: %w", errors.Join(startErr, errors.New("missing interruption")))
	}
	interrupted, err := session.Load(ctx, hostTurnID)
	if err != nil {
		return taskReport{}, err
	}
	runID, err := rootRunID(interrupted)
	if err != nil {
		return taskReport{}, err
	}
	before := state.counts()
	if interrupted.Turn.Status != harness.TurnRunning || before.drafts != 1 || before.lookups != 1 || before.reads == 0 {
		return taskReport{}, errors.New("interruption occurred before the task evidence and single draft were committed")
	}

	// Discard the old provider, Agent Runner, Harness Runner and Session handle.
	// The new Session deliberately has no transcript source.
	client, err = newModel()
	if err != nil {
		return taskReport{}, err
	}
	reopened, err := compose(base, state, client, config, "task-owner-after", nil)
	if err != nil {
		return taskReport{}, err
	}
	if _, err = reopened.Resume(ctx, hostTurnID); !errors.Is(err, kernel.ErrExecutionConflict) || state.counts() != before {
		return taskReport{}, fmt.Errorf("early resume did not preserve the live lease and call counts: %w", errors.Join(err, errors.New("lease fence assertion")))
	}
	state.clock.advance(leaseDuration + time.Second)
	completed, err := reopened.Resume(ctx, hostTurnID)
	if err != nil {
		return taskReport{}, err
	}
	completedRunID, err := rootRunID(completed)
	if err != nil {
		return taskReport{}, err
	}
	after := state.counts()
	if completed.Turn.ID != interrupted.Turn.ID || completedRunID != runID ||
		after.lookups != before.lookups || after.reads != before.reads || after.drafts != before.drafts {
		return taskReport{}, errors.New("resume changed execution identity or repeated an already committed task tool")
	}
	answer, err := verifyTask(ctx, state, config, completed)
	if err != nil {
		return taskReport{}, err
	}
	repeated, err := reopened.Resume(ctx, hostTurnID)
	if err != nil || repeated.Turn.ID != completed.Turn.ID || state.counts() != after {
		return taskReport{}, fmt.Errorf("terminal resume was not read-only: %w", errors.Join(err, errors.New("terminal assertion")))
	}
	mode := "deterministic_runtime_acceptance"
	if config.mode == "live" {
		mode = "live_model_task_smoke"
	}
	return taskReport{
		Mode: mode, Model: config.routedModel(), Status: "passed", TurnID: completed.Turn.ID, RunID: runID,
		ContextGeneration: completed.Turn.ContextRef.Generation, ModelCalls: after.models,
		LookupCalls: after.lookups, ArtifactReads: after.reads, DraftWrites: after.drafts, Answer: answer,
	}, nil
}

func rootRunID(snapshot harness.Snapshot) (string, error) {
	if len(snapshot.Invocations) != 1 || snapshot.Invocations[0].ExecutionRefID == "" {
		return "", errors.New("expected exactly one direct Agent invocation")
	}
	return snapshot.Invocations[0].ExecutionRefID, nil
}

// verifyTask is a deterministic task oracle, independent of the model's claim
// to success. It checks persisted source bytes, actual reads, effects and answer.
func verifyTask(ctx context.Context, state *taskState, config settings, snapshot harness.Snapshot) (taskAnswer, error) {
	var answer taskAnswer
	if snapshot.Turn.Status != harness.TurnCompleted || snapshot.Output == nil || snapshot.Output.ContentType != "application/json" {
		return answer, errors.New("oracle: task has no completed structured answer")
	}
	if err := json.Unmarshal(snapshot.Output.Content, &answer); err != nil {
		return answer, err
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if answer.Release != "R-17" || answer.DeploymentCode != state.source.code || answer.Window != state.source.window ||
		answer.ArtifactID == "" || answer.DraftID == "" || len(state.drafts) != 1 || state.drafts[0] != answer || state.lookupCalls != 1 {
		return answer, errors.New("oracle: answer, source facts and single saved draft disagree")
	}
	checkpoint, err := state.store.GetContextCheckpoint(ctx, snapshot.Turn.ContextCheckpointID)
	if err != nil {
		return answer, err
	}
	if checkpoint.Generation < 2 || !slices.Contains(checkpoint.ArtifactIDs, answer.ArtifactID) {
		return answer, errors.New("oracle: task did not preserve its archived evidence in Context")
	}
	artifact, err := state.store.GetContextArtifact(ctx, answer.ArtifactID)
	if err != nil {
		return answer, err
	}
	var dossier struct {
		Document string `json:"document"`
	}
	if err = json.Unmarshal([]byte(artifact.Content), &dossier); err != nil || dossier.Document != state.source.document() {
		return answer, errors.New("oracle: exact original dossier was not archived")
	}
	readEvidence := false
	for _, page := range state.reads {
		if page.ArtifactID == answer.ArtifactID && page.ContentHash == artifact.ContentHash && page.Found &&
			page.Offset > 2000 && strings.Contains(page.Content, state.source.record()) {
			readEvidence = true
		}
	}
	sawCompacted := false
	for _, request := range state.requests {
		if request.MaxOutputTokens != config.outputTokens {
			return answer, errors.New("oracle: provider output ceiling was not enforced")
		}
		if content, ok := toolMessage(request.Messages, lookupKey); ok {
			if !artifactPattern.MatchString(content) || strings.Contains(content, state.source.record()) {
				return answer, errors.New("oracle: large source bypassed the bounded model window")
			}
			sawCompacted = true
		}
	}
	if !readEvidence || !sawCompacted {
		return answer, errors.New("oracle: answer lacks a verified deep artifact read")
	}
	return answer, nil
}
