package agentruntime

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	model "github.com/orz-i/Gaoge/sdk/go/agent-runtime/domain"
)

const (
	testHandoffEvidenceReady = "Evidence ready"
	testHandoffAgentName     = "Researcher"
	testHandoffChildRunID    = "child-a"
)

var errOldHandoffSegmentCancelled = errors.New("old handoff segment cancelled")

type handoffJoinTestUnitOfWork struct{}

func (handoffJoinTestUnitOfWork) Within(ctx context.Context, work func(context.Context) error) error {
	return work(ctx)
}

func TestFinalizeChildResolvesWaitingParentJoin(t *testing.T) {
	parent, join, checkpoint := handoffJoinReadyFixture(t)
	child := model.Run{RunID: testHandoffChildRunID, Actor: parent.Actor, HandoffID: testJoinHandoffA}
	handoff := model.RunHandoff{
		HandoffID: testJoinHandoffA, ParentRunID: parent.RunID, ChildRunID: child.RunID, Actor: parent.Actor,
		AgentManifest: model.ResourceRef{Kind: model.AgentManifestKind, ID: testAgentReviewID, Revision: "1"},
		AgentName:     testHandoffAgentName, Status: model.RunHandoffStatusCompleted, ResultSummary: testHandoffEvidenceReady,
	}
	store := &handoffJoinRuntimeStore{
		run: parent, checkpoint: checkpoint,
		handoffs:          map[string]model.RunHandoff{testJoinHandoffA: handoff},
		handoffCompletion: model.RunHandoffCompletionResult{Handoff: handoff, ResolvedJoins: []model.RunHandoffJoin{join}},
	}
	engine := &Engine{repo: store}
	parentRunID, events, err := engine.finalizeRunHandoff(t.Context(), child, model.TerminalIntent{Outcome: model.TerminalCompleted, Summary: testHandoffEvidenceReady}, nil)
	if err != nil || parentRunID != parent.RunID || store.resumeStatus != model.RunStatusRunning || store.continuationJob == nil {
		t.Fatalf("finalize child parent=%q status=%q job=%#v events=%#v err=%v", parentRunID, store.resumeStatus, store.continuationJob, events, err)
	}
	if !containsRuntimeEventType(events, "handoff.completed") || !containsRuntimeEventType(events, "handoff.join.ready") || !containsRuntimeEventType(events, "run.resumed") {
		t.Fatalf("finalize child events=%#v", events)
	}
}

func (s *handoffJoinRuntimeStore) CompleteRunHandoffWithJoins(_ context.Context, _ model.ActorRef, _ string, _ model.RunHandoffCompletion) (model.RunHandoffCompletionResult, error) {
	return s.handoffCompletion, nil
}

func (s *handoffJoinRuntimeStore) AppendRunEvent(_ context.Context, event *model.Event) (*model.Event, bool, error) {
	if event == nil {
		return nil, false, ErrInvalidInput
	}
	item := *event
	s.appendedRunEvents = append(s.appendedRunEvents, item)
	return &item, true, nil
}

func TestExpiredRunHandoffJoinSuspendsParent(t *testing.T) {
	parent, join, checkpoint := handoffJoinReadyFixture(t)
	deadline := time.Now().UTC().Add(-time.Second)
	join.Status = model.RunHandoffJoinStatusPending
	join.CompletedCount, join.PendingCount = 0, 1
	join.ResultHandoffIDs = nil
	join.TimeoutSeconds = 60
	join.TimeoutPolicy = model.RunHandoffJoinTimeoutLeaveRunning
	join.DeadlineAt = &deadline
	store := &handoffJoinRuntimeStore{
		run: parent, checkpoint: checkpoint, expiredJoin: &join,
		handoffs: map[string]model.RunHandoff{
			testJoinHandoffA: {HandoffID: testJoinHandoffA, Actor: parent.Actor, ChildRunID: testHandoffChildRunID, AgentName: testHandoffAgentName, Status: model.RunHandoffStatusQueued},
		},
	}
	engine := &Engine{repo: store, unitOfWork: handoffJoinTestUnitOfWork{}, generationStreams: newGenerationStreamRegistry(nil, generationStreamOptions{})}
	if err := engine.ExpireRunHandoffJoinsOnce(t.Context(), time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if store.resumeStatus != model.RunStatusSuspended || store.expiredJoin == nil || store.expiredJoin.Status != model.RunHandoffJoinStatusFailed || store.expiredJoin.ErrorCode != "handoff_join_timeout" {
		t.Fatalf("expired join state = join=%#v resume=%q", store.expiredJoin, store.resumeStatus)
	}
	if !containsRuntimeEventType(store.resumeEvents, "handoff.join.failed") || !containsRuntimeEventType(store.resumeEvents, "run.suspended") {
		t.Fatalf("timeout events = %#v", store.resumeEvents)
	}
}

func TestNormalizeRunHandoffJoinTimeoutRejectsUnboundedValues(t *testing.T) {
	if _, _, err := normalizeRunHandoffJoinTimeout(minimumHandoffJoinTimeoutSeconds-1, ""); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("short timeout error = %v", err)
	}
	if _, _, err := normalizeRunHandoffJoinTimeout(maximumHandoffJoinTimeoutSeconds+1, ""); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("long timeout error = %v", err)
	}
	if _, _, err := normalizeRunHandoffJoinTimeout(defaultHandoffJoinTimeoutSeconds, "unknown"); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("unknown timeout policy error = %v", err)
	}
}

func (s *handoffJoinRuntimeStore) ExpireNextRunHandoffJoin(_ context.Context, now time.Time) (*model.RunHandoffJoin, error) {
	if s.expiredJoin == nil || s.expiredJoinTaken {
		return nil, ErrNotFound
	}
	expired, applied := model.ExpireRunHandoffJoin(*s.expiredJoin, now)
	if !applied {
		return nil, ErrNotFound
	}
	s.expiredJoinTaken = true
	s.expiredJoin = &expired
	return &expired, nil
}

func TestPendingHandoffJoinCheckpointCannotBeExplicitlyResumed(t *testing.T) {
	continuation := runContinuation{Type: runContinuationAwaitHandoffJoin}
	if err := validateExplicitResumeContinuation(continuation); !errors.Is(err, ErrRunResumeConflict) {
		t.Fatalf("pending handoff explicit resume error=%v", err)
	}
}

func TestHandoffJoinContextIsInjectedAsUntrustedPromptData(t *testing.T) {
	join := runHandoffJoinContext{
		JoinID: "join-context", Mode: model.RunHandoffJoinModeAll, FailurePolicy: model.RunHandoffJoinFailureCollect,
		Results: []runHandoffJoinResult{{HandoffID: testJoinHandoffA, ChildRunID: testHandoffChildRunID, AgentName: testHandoffAgentName, Status: model.RunHandoffStatusCompleted, Summary: testHandoffEvidenceReady}},
	}
	join.Fingerprint = runHandoffJoinContextFingerprint(join)
	ctx := context.WithValue(t.Context(), runHandoffJoinContextKey{}, &join)
	messages, err := appendRunHandoffJoinContextMessages(ctx, []Message{{Role: valueUser19341906, Content: "Parent goal"}})
	if err != nil || len(messages) != 3 {
		t.Fatalf("context messages=%#v err=%v", messages, err)
	}
	if messages[1].Role != "system" || messages[1].Content != handoffJoinContextPolicy || !strings.Contains(messages[2].Content, testHandoffEvidenceReady) {
		t.Fatalf("injected messages=%#v", messages)
	}
	if strings.Contains(messages[2].Content, join.Fingerprint) {
		t.Fatal("prompt leaked internal handoff context fingerprint")
	}
	tampered := join
	tampered.Results[0].Summary = "tampered"
	if _, err = appendRunHandoffJoinContextMessages(context.WithValue(t.Context(), runHandoffJoinContextKey{}, &tampered), nil); !errors.Is(err, ErrRunSnapshotIncompatible) {
		t.Fatalf("tampered context error=%v", err)
	}
}

func (s *handoffJoinRuntimeStore) GetRunHandoffJoin(_ context.Context, actor model.ActorRef, joinID string) (*model.RunHandoffJoin, error) {
	if s.waitJoin == nil || s.waitJoin.Actor != actor || s.waitJoin.JoinID != joinID {
		return nil, ErrNotFound
	}
	join := *s.waitJoin
	return &join, nil
}

type handoffJoinRuntimeStore struct {
	Store
	run               model.Run
	checkpoint        model.Checkpoint
	handoffs          map[string]model.RunHandoff
	waitJoin          *model.RunHandoffJoin
	waitEvents        []model.Event
	resumeStatus      string
	resumeSuccessor   *model.Checkpoint
	resumeEvents      []model.Event
	continuationJob   *model.ContinuationJob
	waitBundleJoin    *model.RunHandoffJoin
	waitBundleReused  bool
	waitBundleError   error
	resumeApplied     bool
	resumeError       error
	continuationError error
	handoffCompletion model.RunHandoffCompletionResult
	appendedRunEvents []model.Event
	expiredJoin       *model.RunHandoffJoin
	expiredJoinTaken  bool
}

func (s *handoffJoinRuntimeStore) GetRun(_ context.Context, actor model.ActorRef, runID string) (*model.Run, error) {
	if s.run.Actor != actor || s.run.RunID != runID {
		return nil, ErrNotFound
	}
	run := s.run
	return &run, nil
}

func (s *handoffJoinRuntimeStore) CreateRunHandoffJoinWaitBundle(_ context.Context, join *model.RunHandoffJoin, expectedStatus string, expectedLastEventSeq int64, checkpoint *model.Checkpoint, events []model.Event) (*model.RunHandoffJoin, []model.Event, bool, error) {
	if s.waitBundleError != nil {
		return nil, nil, false, s.waitBundleError
	}
	if s.run.Status != expectedStatus || s.run.LastEventSeq != expectedLastEventSeq {
		return nil, nil, false, ErrDuplicate
	}
	created := *join
	if s.waitBundleJoin != nil {
		created = *s.waitBundleJoin
	}
	s.waitJoin = &created
	s.checkpoint = *checkpoint
	s.waitEvents = append([]model.Event(nil), events...)
	s.run.Status = model.RunStatusWaitingHandoff
	s.run.LastEventSeq += int64(len(events))
	return &created, append([]model.Event(nil), events...), s.waitBundleReused, nil
}

func (s *handoffJoinRuntimeStore) GetRunCheckpoint(_ context.Context, actor model.ActorRef, runID, checkpointID string) (*model.Checkpoint, error) {
	if actor != s.run.Actor || runID != s.run.RunID || checkpointID != s.checkpoint.CheckpointID {
		return nil, ErrNotFound
	}
	checkpoint := s.checkpoint
	return &checkpoint, nil
}

func (s *handoffJoinRuntimeStore) ResumeRun(_ context.Context, actor model.ActorRef, runID, checkpointID, _, _ string, nextStatus string, successor *model.Checkpoint, events []model.Event) (*model.Checkpoint, *model.Checkpoint, []model.Event, bool, error) {
	if s.resumeError != nil {
		return nil, nil, nil, false, s.resumeError
	}
	if actor != s.run.Actor || runID != s.run.RunID || checkpointID != s.checkpoint.CheckpointID {
		return nil, nil, nil, false, ErrNotFound
	}
	s.resumeStatus = nextStatus
	s.resumeSuccessor = successor
	s.resumeEvents = append([]model.Event(nil), events...)
	s.run.Status = nextStatus
	applied := s.resumeApplied
	if !applied {
		applied = true
	}
	consumed := s.checkpoint
	consumed.Status = model.CheckpointConsumed
	return &consumed, successor, append([]model.Event(nil), events...), applied, nil
}

func (s *handoffJoinRuntimeStore) CreateContinuationJob(_ context.Context, job *model.ContinuationJob) (*model.ContinuationJob, bool, error) {
	if s.continuationError != nil {
		return nil, false, s.continuationError
	}
	cloned := *job
	s.continuationJob = &cloned
	return &cloned, false, nil
}

func (s *handoffJoinRuntimeStore) GetRunHandoff(_ context.Context, actor model.ActorRef, handoffID string) (*model.RunHandoff, error) {
	item, ok := s.handoffs[handoffID]
	if !ok || item.Actor != actor {
		return nil, ErrNotFound
	}
	cloned := item
	return &cloned, nil
}

func TestCreateRunHandoffJoinTransitionsParentToWaiting(t *testing.T) {
	parent := handoffJoinTestParent()
	store := &handoffJoinRuntimeStore{run: parent}
	engine := &Engine{repo: store, unitOfWork: handoffJoinTestUnitOfWork{}, generationStreams: newGenerationStreamRegistry(nil, generationStreamOptions{})}
	join, reused, err := engine.CreateRunHandoffJoin(t.Context(), CreateRunHandoffJoinInput{
		Actor: parent.Actor, ParentRunID: parent.RunID, ClientJoinID: "join-wait", HandoffIDs: []string{testJoinHandoffA},
	})
	requireNewRunHandoffJoin(t, join, reused, err)
	if join.TimeoutSeconds != defaultHandoffJoinTimeoutSeconds || join.TimeoutPolicy != model.RunHandoffJoinTimeoutCancelPending || join.DeadlineAt == nil {
		t.Fatalf("default timeout contract = %#v", join)
	}
	assertParentWaitingForHandoffJoin(t, store, *join)
	assertRunHandoffWaitContinuation(t, store.checkpoint)
	replayed, reused, err := engine.CreateRunHandoffJoin(t.Context(), CreateRunHandoffJoinInput{
		Actor: parent.Actor, ParentRunID: parent.RunID, ClientJoinID: "join-wait", HandoffIDs: []string{testJoinHandoffA},
	})
	assertPendingRunHandoffJoinReplay(t, replayed, *join, reused, err)
}

func requireNewRunHandoffJoin(t *testing.T, join *model.RunHandoffJoin, reused bool, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
	if reused || join == nil {
		t.Fatalf("new join reused=%v join=%#v", reused, join)
	}
}

func assertParentWaitingForHandoffJoin(t *testing.T, store *handoffJoinRuntimeStore, join model.RunHandoffJoin) {
	t.Helper()
	if store.run.Status != model.RunStatusWaitingHandoff || store.waitJoin == nil || store.checkpoint.CheckpointID != join.ResumeCheckpointID {
		t.Fatalf("wait state run=%#v join=%#v checkpoint=%#v", store.run, store.waitJoin, store.checkpoint)
	}
	if len(store.waitEvents) != 4 || store.waitEvents[len(store.waitEvents)-1].EventType != "run.waiting_handoff" {
		t.Fatalf("wait events=%#v", store.waitEvents)
	}
}

func assertRunHandoffWaitContinuation(t *testing.T, checkpoint model.Checkpoint) {
	t.Helper()
	wait, err := decodeRunContinuation(checkpoint)
	if err != nil || wait.Type != runContinuationAwaitHandoffJoin || wait.NextContinuation == nil {
		t.Fatalf("wait continuation=%#v err=%v", wait, err)
	}
}

func assertPendingRunHandoffJoinReplay(t *testing.T, replayed *model.RunHandoffJoin, expected model.RunHandoffJoin, reused bool, err error) {
	t.Helper()
	if err != nil || !reused || replayed == nil || replayed.JoinID != expected.JoinID {
		t.Fatalf("pending replay join=%#v reused=%v err=%v", replayed, reused, err)
	}
}

func TestReadyRunHandoffJoinReplayDoesNotCancelResumedGeneration(t *testing.T) {
	parent := handoffJoinTestParent()
	normalized, err := normalizeCreateRunHandoffJoinInput(CreateRunHandoffJoinInput{
		Actor: parent.Actor, ParentRunID: parent.RunID, ClientJoinID: "join-replayed-ready", HandoffIDs: []string{testJoinHandoffA},
	})
	if err != nil {
		t.Fatal(err)
	}
	join := newRunHandoffJoinContract(parent, normalized, "checkpoint-ready")
	join.Status = model.RunHandoffJoinStatusReady
	store := &handoffJoinRuntimeStore{run: parent, waitJoin: &join}
	registry := newGenerationStreamRegistry(nil, generationStreamOptions{})
	cancelled := make(chan struct{})
	runCtx, cancel := context.WithCancel(t.Context())
	defer cancel()
	registry.register(runCtx, parent.RunID, parent.Actor, func() { close(cancelled) })
	engine := &Engine{repo: store, unitOfWork: handoffJoinTestUnitOfWork{}, generationStreams: registry}
	replayed, reused, err := engine.CreateRunHandoffJoin(t.Context(), normalized)
	if err != nil || !reused || replayed.Status != model.RunHandoffJoinStatusReady {
		t.Fatalf("ready replay join=%#v reused=%v err=%v", replayed, reused, err)
	}
	select {
	case <-cancelled:
		t.Fatal("ready join replay cancelled the resumed generation")
	default:
	}
}

func TestResolveReadyRunHandoffJoinQueuesParentContinuation(t *testing.T) {
	parent, join, checkpoint := handoffJoinReadyFixture(t)
	store := &handoffJoinRuntimeStore{
		run: parent, checkpoint: checkpoint,
		handoffs: map[string]model.RunHandoff{
			testJoinHandoffA: {HandoffID: testJoinHandoffA, Actor: parent.Actor, ChildRunID: testHandoffChildRunID, AgentName: testHandoffAgentName, Status: model.RunHandoffStatusCompleted, ResultSummary: testHandoffEvidenceReady, ResultOutputIDs: []string{"output-a"}},
		},
	}
	engine := &Engine{repo: store}
	events, queued, err := engine.resolveRunHandoffJoinAtCommit(t.Context(), parent, join)
	assertReadyRunHandoffJoinResolution(t, store, checkpoint, events, queued, err)
}

func assertReadyRunHandoffJoinResolution(t *testing.T, store *handoffJoinRuntimeStore, checkpoint model.Checkpoint, events []model.Event, queued bool, err error) {
	t.Helper()
	if err != nil || !queued || store.resumeStatus != model.RunStatusRunning || store.continuationJob == nil {
		t.Fatalf("resolve ready events=%#v queued=%v status=%q job=%#v err=%v", events, queued, store.resumeStatus, store.continuationJob, err)
	}
	assertReadyRunHandoffSuccessor(t, store, checkpoint)
	if !containsRuntimeEventType(events, "handoff.join.ready") || !containsRuntimeEventType(events, "run.resumed") {
		t.Fatalf("ready events=%#v", events)
	}
}

func assertReadyRunHandoffSuccessor(t *testing.T, store *handoffJoinRuntimeStore, checkpoint model.Checkpoint) {
	t.Helper()
	if store.resumeSuccessor == nil || store.continuationJob == nil || store.resumeSuccessor.ParentCheckpointID != checkpoint.CheckpointID || store.continuationJob.CheckpointID != store.resumeSuccessor.CheckpointID {
		t.Fatalf("successor=%#v job=%#v", store.resumeSuccessor, store.continuationJob)
	}
	continuation, err := decodeRunContinuation(*store.resumeSuccessor)
	if err != nil || continuation.HandoffJoin == nil || len(continuation.HandoffJoin.Results) != 1 || continuation.HandoffJoin.Results[0].Summary != testHandoffEvidenceReady {
		t.Fatalf("frozen handoff context=%#v err=%v", continuation.HandoffJoin, err)
	}
}

func TestResolveFailedRunHandoffJoinSuspendsWithoutContinuation(t *testing.T) {
	parent, join, checkpoint := handoffJoinReadyFixture(t)
	join.Status = model.RunHandoffJoinStatusFailed
	join.ErrorCode = "handoff_join_child_failed"
	store := &handoffJoinRuntimeStore{run: parent, checkpoint: checkpoint, handoffs: map[string]model.RunHandoff{
		testJoinHandoffA: {HandoffID: testJoinHandoffA, Actor: parent.Actor, ChildRunID: testHandoffChildRunID, AgentName: testHandoffAgentName, Status: model.RunHandoffStatusFailed, ErrorCode: "child_failed"},
	}}
	engine := &Engine{repo: store}
	events, queued, err := engine.resolveRunHandoffJoinAtCommit(t.Context(), parent, join)
	if err != nil || queued || store.resumeStatus != model.RunStatusSuspended || store.continuationJob != nil {
		t.Fatalf("resolve failed events=%#v queued=%v status=%q job=%#v err=%v", events, queued, store.resumeStatus, store.continuationJob, err)
	}
	if !containsRuntimeEventType(events, "handoff.join.failed") || !containsRuntimeEventType(events, "run.suspended") {
		t.Fatalf("failed events=%#v", events)
	}
	if store.resumeSuccessor == nil {
		t.Fatal("failed join did not persist an explicit-resume successor")
	}
	if _, err = decodeRunContinuation(*store.resumeSuccessor); err != nil {
		t.Fatalf("failed join successor is not resumable: %v", err)
	}
}

func TestFailTextRunDoesNotTerminateWaitingHandoffParent(t *testing.T) {
	parent := handoffJoinTestParent()
	store := &handoffJoinRuntimeStore{run: parent}
	store.run.Status = model.RunStatusWaitingHandoff
	engine := &Engine{repo: store}
	engine.failTextRun(t.Context(), parent, parent.CurrentStepID, errOldHandoffSegmentCancelled)
	if store.run.Status != model.RunStatusWaitingHandoff {
		t.Fatalf("waiting parent changed status: %#v", store.run)
	}
}

func handoffJoinTestParent() model.Run {
	actor := model.ActorRef{TenantID: "tenant-join", ActorID: "actor-join"}
	return model.Run{
		RunID: "run-parent-join", Actor: actor, Thread: model.ThreadRef{Kind: threadKindConversation, ID: "thread-join"},
		Status: model.RunStatusRunning, CurrentStepID: "step-parent-join", RootRunID: "run-parent-join",
		RunConfigSnapshotJSON: mustRunJSON(effectiveTextRunConfig{SemanticVersion: RuntimeSnapshotVersion, Strategy: TextRunStrategyDirect}),
	}
}

func handoffJoinReadyFixture(t *testing.T) (model.Run, model.RunHandoffJoin, model.Checkpoint) {
	t.Helper()
	parent := handoffJoinTestParent()
	normalized, err := normalizeCreateRunHandoffJoinInput(CreateRunHandoffJoinInput{
		Actor: parent.Actor, ParentRunID: parent.RunID, ClientJoinID: "join-ready", HandoffIDs: []string{testJoinHandoffA},
	})
	if err != nil {
		t.Fatal(err)
	}
	join, checkpoint, _, err := buildRunHandoffJoinWait(parent, normalized)
	if err != nil {
		t.Fatal(err)
	}
	parent.Status = model.RunStatusWaitingHandoff
	join.Status = model.RunHandoffJoinStatusReady
	join.CompletedCount, join.PendingCount = 1, 0
	join.ResultHandoffIDs = []string{testJoinHandoffA}
	return parent, join, *checkpoint
}

func containsRuntimeEventType(events []model.Event, eventType string) bool {
	for _, event := range events {
		if event.EventType == eventType {
			return true
		}
	}
	return false
}

func TestAwaitHandoffContinuationRejectsNestedWait(t *testing.T) {
	parent := handoffJoinTestParent()
	next := runContinuation{SemanticVersion: RuntimeSnapshotVersion, SegmentKey: "nested", Type: runContinuationAwaitHandoffJoin, TargetStatus: model.RunStatusWaitingHandoff, StepID: parent.CurrentStepID, HandoffJoinID: "nested"}
	wait := runContinuation{SemanticVersion: RuntimeSnapshotVersion, SegmentKey: "outer", Type: runContinuationAwaitHandoffJoin, TargetStatus: model.RunStatusWaitingHandoff, StepID: parent.CurrentStepID, HandoffJoinID: "outer", NextContinuation: &next}
	if err := validateRunContinuation(wait); !errors.Is(err, ErrRunSnapshotIncompatible) {
		t.Fatalf("nested wait validation error=%v", err)
	}
}

func TestRunHandoffJoinWaitEventPayloadExcludesHiddenContinuation(t *testing.T) {
	parent := handoffJoinTestParent()
	normalized, err := normalizeCreateRunHandoffJoinInput(CreateRunHandoffJoinInput{Actor: parent.Actor, ParentRunID: parent.RunID, ClientJoinID: "join-payload", HandoffIDs: []string{testJoinHandoffA}})
	if err != nil {
		t.Fatal(err)
	}
	_, _, events, err := buildRunHandoffJoinWait(parent, normalized)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if strings.Contains(event.PayloadJSON, "nextContinuation") || strings.Contains(event.PayloadJSON, "segmentKey") {
			t.Fatalf("wait event leaked continuation state: %s", event.PayloadJSON)
		}
	}
}
