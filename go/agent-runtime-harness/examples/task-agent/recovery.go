package main

import (
	"context"
	"encoding/json"
	"errors"
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

// This clock belongs only to the in-memory fault fixture. Performance metrics
// use time.Since; production recovery must obey its Store-authoritative clock.
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

type recoveryChecks struct {
	InterruptionObserved      bool `json:"interruptionObserved"`
	LeaseBlocked              bool `json:"leaseBlocked"`
	SameRun                   bool `json:"sameRun"`
	SameTurn                  bool `json:"sameTurn"`
	CommittedToolsNotReplayed bool `json:"committedToolsNotReplayed"`
	NoExtraToolsAfterResume   bool `json:"noExtraToolsAfterResume"`
	TerminalResumeNoOp        bool `json:"terminalResumeNoOp"`
}

type taskReport struct {
	Mode              string                `json:"mode"`
	Model             string                `json:"model"`
	Status            string                `json:"status"`
	Stage             string                `json:"stage"`
	FailureCode       string                `json:"failureCode,omitempty"`
	DurationMs        int64                 `json:"durationMs"`
	TurnID            string                `json:"turnID"`
	RunID             string                `json:"runID"`
	ContextGeneration int                   `json:"contextGeneration"`
	ModelCalls        int                   `json:"modelCalls"`
	ToolCalls         int                   `json:"toolCalls"`
	LookupCalls       int                   `json:"lookupCalls"`
	ArtifactReads     int                   `json:"artifactReads"`
	DraftWrites       int                   `json:"draftWrites"`
	OraclePassed      bool                  `json:"oraclePassed"`
	RecoveryChecks    recoveryChecks        `json:"recoveryChecks"`
	Usage             usageCoverage         `json:"usage"`
	ModelDurationsMs  []int64               `json:"modelDurationsMs"`
	ToolExecutions    []toolExecutionMetric `json:"toolExecutions"`
	// Internal oracle material: baseline events project only safe metrics.
	Answer taskAnswer `json:"answer"`
}

type callCounts struct{ models, lookups, reads, drafts, tools int }

func (state *taskState) counts() callCounts {
	state.mu.Lock()
	defer state.mu.Unlock()
	return callCounts{len(state.requests), state.lookupCalls, len(state.reads), len(state.drafts), len(state.executions)}
}

type taskFailure struct {
	code  string
	cause error
}

func (failure taskFailure) Error() string {
	if failure.cause != nil {
		return failure.cause.Error()
	}
	return failure.code
}
func (failure taskFailure) Unwrap() error { return failure.cause }

func taskFailed(code string, cause error) error { return taskFailure{code: code, cause: cause} }

// Every return retains observed counts and the last completed checks. Errors
// remain available to tests; command/event reporting uses stable codes only.
func runTask(ctx context.Context, config settings, newModel func() (model.Client, error), source sourceFixture) (report taskReport, runErr error) {
	started := time.Now()
	state := &taskState{store: harness.NewMemoryStore(), clock: &demoClock{now: time.Now().UTC()}, source: source, sampleID: config.sampleID}
	if state.sampleID == "" {
		state.sampleID = "session"
	}
	report.Mode, report.Model, report.Stage = "deterministic_runtime_acceptance", config.routedModel(), "configuration"
	if config.mode != "scripted" {
		report.Mode = "live_model_task_smoke"
	}
	defer func() {
		report.DurationMs = time.Since(started).Milliseconds()
		state.collectMetrics(&report)
		if runErr != nil {
			report.Status, report.FailureCode = "failed", failureCode(runErr, "task_execution_failed")
		} else {
			report.Status = "passed"
		}
	}()
	base := memory.NewStoreWithExecutionClock(state.clock)
	fault := &interruptStore{Store: base}
	client, err := newModel()
	if err != nil {
		return report, err
	}
	session, err := compose(fault, state, client, config, "task-owner-before", harness.TranscriptSourceFunc(initialTranscript))
	if err != nil {
		return report, err
	}
	report.Stage = "start"
	initial, startErr := session.Prompt(ctx, hostTurnID, taskPrompt)
	updateReportIdentity(&report, initial)
	if !errors.Is(startErr, errInterrupted) || !fault.fired.Load() {
		return report, taskFailed("task_did_not_reach_interruption", startErr)
	}
	report.RecoveryChecks.InterruptionObserved = true
	report.Stage = "interrupted"
	interrupted, err := session.Load(ctx, hostTurnID)
	if err != nil {
		return report, err
	}
	updateReportIdentity(&report, interrupted)
	runID, err := rootRunID(interrupted)
	if err != nil {
		return report, err
	}
	before := state.counts()
	committedBefore := state.committedExecutions()
	if interrupted.Turn.Status != harness.TurnRunning || before.drafts != 1 || before.lookups != 1 || before.reads == 0 {
		return report, taskFailed("task_evidence_missing_at_interruption", nil)
	}

	// Reconstruct all execution handles. Committed Context supplies recovery;
	// the new Session deliberately has no transcript source.
	client, err = newModel()
	if err != nil {
		return report, err
	}
	reopened, err := compose(base, state, client, config, "task-owner-after", nil)
	if err != nil {
		return report, err
	}
	report.Stage = "lease_check"
	_, err = reopened.Resume(ctx, hostTurnID)
	report.RecoveryChecks.LeaseBlocked = errors.Is(err, kernel.ErrExecutionConflict) && state.counts() == before
	if !report.RecoveryChecks.LeaseBlocked {
		return report, taskFailed("lease_fence_failed", err)
	}
	state.clock.advance(leaseDuration + time.Second)
	report.Stage = "resume"
	completed, err := reopened.Resume(ctx, hostTurnID)
	updateReportIdentity(&report, completed)
	if err != nil {
		return report, err
	}
	completedRunID, err := rootRunID(completed)
	if err != nil {
		return report, err
	}
	after := state.counts()
	checks := &report.RecoveryChecks
	checks.SameRun, checks.SameTurn = completedRunID == runID, completed.Turn.ID == interrupted.Turn.ID
	checks.CommittedToolsNotReplayed = unchangedCommitted(committedBefore, state.committedExecutions())
	checks.NoExtraToolsAfterResume = after.tools == before.tools
	report.Answer, err = verifyTask(ctx, state, config, completed)
	report.OraclePassed = err == nil
	if !checks.SameRun || !checks.SameTurn {
		return report, taskFailed("execution_identity_changed", nil)
	}
	if !checks.CommittedToolsNotReplayed {
		return report, taskFailed("runtime_duplicate_effect", nil)
	}
	if !checks.NoExtraToolsAfterResume {
		return report, taskFailed("extra_tool_after_resume", nil)
	}
	report.Stage = "oracle"
	if err != nil {
		return report, taskFailed("oracle_failed", err)
	}
	report.Stage = "terminal_check"
	repeated, err := reopened.Resume(ctx, hostTurnID)
	checks.TerminalResumeNoOp = err == nil && repeated.Turn.ID == completed.Turn.ID &&
		repeated.Turn.Revision == completed.Turn.Revision && state.counts() == after
	if !checks.TerminalResumeNoOp {
		return report, taskFailed("terminal_resume_not_noop", err)
	}
	report.Stage = "completed"
	return report, nil
}

func updateReportIdentity(report *taskReport, snapshot harness.Snapshot) {
	if snapshot.Turn.ID != "" {
		report.TurnID = snapshot.Turn.ID
		report.ContextGeneration = snapshot.Turn.ContextRef.Generation
	}
	if runID, err := rootRunID(snapshot); err == nil {
		report.RunID = runID
	}
}

func rootRunID(snapshot harness.Snapshot) (string, error) {
	if len(snapshot.Invocations) != 1 || snapshot.Invocations[0].ExecutionRefID == "" {
		return "", taskFailed("invalid_root_invocation", nil)
	}
	return snapshot.Invocations[0].ExecutionRefID, nil
}

// verifyTask checks persisted source bytes, actual reads, effects and answer;
// it does not accept the model's claim that the task succeeded.
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
