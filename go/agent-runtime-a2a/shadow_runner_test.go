package a2a

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/handoff"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
)

const (
	testShadowActorID    = "actor"
	testShadowTenantID   = "tenant"
	testShadowThreadKind = "conversation"
	testShadowThreadID   = "thread"
	testShadowRemoteURL  = "https://agent.example/a2a"
)

type shadowRemote struct {
	sent     SendRequest
	task     TaskSnapshot
	canceled bool
	sendErr  error
}

func (remote *shadowRemote) SendMessage(_ context.Context, _ Discovery, request SendRequest) (Interaction, error) {
	remote.sent = request
	if remote.sendErr != nil {
		return Interaction{}, remote.sendErr
	}
	return Interaction{Task: cloneTaskSnapshot(&remote.task), Raw: append(json.RawMessage(nil), remote.task.Raw...)}, nil
}

func (remote *shadowRemote) GetTask(context.Context, Discovery, string) (TaskSnapshot, error) {
	return cloneTaskValue(remote.task), nil
}

func (remote *shadowRemote) CancelTask(context.Context, Discovery, string) (TaskSnapshot, error) {
	remote.canceled = true
	remote.task.State = "TASK_STATE_CANCELED"
	remote.task.Terminal = true
	remote.task.Raw = json.RawMessage(`{"id":"remote-1","contextId":"context-1","status":{"state":"TASK_STATE_CANCELED"}}`)
	return cloneTaskValue(remote.task), nil
}

func TestShadowRunnerSatisfiesHandoffWithoutA2AImportsInParent(t *testing.T) {
	t.Parallel()
	runtime := newShadowRuntime(t)
	remote := &shadowRemote{task: workingTask()}
	runner := newShadowRunner(t, runtime, remote)
	var _ handoff.ChildRunner = runner
	coordinator := newShadowCoordinator(t, runner)
	parent := newShadowParent(t, runtime)
	delegation := handoff.Delegation{
		ID: "delegation-1", MemberID: "remote-member", ChildRunID: "a2a-child-1",
		Goal: "finish remotely", Status: handoff.StatusQueued,
	}
	started := startShadowDelegation(t, coordinator, parent, delegation)
	assertShadowRemoteRequest(t, remote, delegation)
	assertShadowChildRunning(t, runner, delegation.ChildRunID)
	remote.task = completedTask()
	assertShadowDelegationCompleted(t, coordinator, started)
}

func newShadowCoordinator(t *testing.T, runner *ShadowRunner) *handoff.Coordinator {
	t.Helper()
	coordinator, err := handoff.New(runner)
	if err != nil {
		t.Fatal(err)
	}
	return coordinator
}

func startShadowDelegation(
	t *testing.T,
	coordinator *handoff.Coordinator,
	parent kernel.Snapshot,
	delegation handoff.Delegation,
) handoff.Delegation {
	t.Helper()
	started, err := coordinator.StartOrLoad(t.Context(), parent, delegation)
	if !errors.Is(err, handoff.ErrChildPending) || started.Status != handoff.StatusRunning {
		t.Fatalf("started=%#v err=%v", started, err)
	}
	return started
}

func assertShadowRemoteRequest(t *testing.T, remote *shadowRemote, delegation handoff.Delegation) {
	t.Helper()
	if remote.sent.MessageID != delegation.ChildRunID+":message" || remote.sent.Text != delegation.Goal {
		t.Fatalf("remote request = %#v", remote.sent)
	}
}

func assertShadowChildRunning(t *testing.T, runner *ShadowRunner, runID string) {
	t.Helper()
	child, err := runner.LoadRun(t.Context(), runID)
	if err != nil || child.Run.Kind != RunKind || child.Run.Status != kernel.RunStatusRunning {
		t.Fatalf("child=%#v err=%v", child.Run, err)
	}
}

func assertShadowDelegationCompleted(t *testing.T, coordinator *handoff.Coordinator, started handoff.Delegation) {
	t.Helper()
	completed, err := coordinator.Refresh(t.Context(), started)
	if err != nil || completed.Status != handoff.StatusCompleted || !json.Valid(completed.Result) {
		t.Fatalf("completed=%#v err=%v", completed, err)
	}
}

func TestShadowRunnerCancelProjectsRemoteTaskLocally(t *testing.T) {
	t.Parallel()
	runtime := newShadowRuntime(t)
	remote := &shadowRemote{task: workingTask()}
	runner := newShadowRunner(t, runtime, remote)
	started, err := runner.StartRun(t.Context(), shadowStartRequest("a2a-child-cancel"))
	if err != nil || started.Run.Status != kernel.RunStatusRunning {
		t.Fatalf("started=%#v err=%v", started.Run, err)
	}
	canceled, err := runner.CancelRun(t.Context(), started.Run.ID)
	if err != nil || canceled.Run.Status != kernel.RunStatusCancelled || !remote.canceled {
		t.Fatalf("canceled=%#v remote=%#v err=%v", canceled.Run, remote, err)
	}
}

func TestShadowRunnerMapsInputRequiredAndResumesRemoteTask(t *testing.T) {
	t.Parallel()
	runtime := newShadowRuntime(t)
	remote := &shadowRemote{task: inputRequiredTask()}
	runner := newShadowRunner(t, runtime, remote)
	started, err := runner.StartRun(t.Context(), shadowStartRequest("a2a-child-input"))
	if err != nil || started.Run.Status != kernel.RunStatusWaitingInput {
		t.Fatalf("started=%#v err=%v", started.Run, err)
	}
	remote.task = completedTask()
	completed, err := runner.ResumeRun(t.Context(), started.Run.ID, "approved")
	if err != nil || completed.Run.Status != kernel.RunStatusCompleted {
		t.Fatalf("completed=%#v err=%v", completed.Run, err)
	}
	if remote.sent.MessageID != started.Run.ID+":message:2" || remote.sent.TaskID != "remote-1" ||
		remote.sent.ContextID != "context-1" || remote.sent.Text != "approved" {
		t.Fatalf("resume request=%#v", remote.sent)
	}
}

func TestShadowRunnerRefreshesAuthRequiredRunAfterRemoteCompletion(t *testing.T) {
	t.Parallel()
	runtime := newShadowRuntime(t)
	remote := &shadowRemote{task: authRequiredTask()}
	runner := newShadowRunner(t, runtime, remote)
	started, err := runner.StartRun(t.Context(), shadowStartRequest("a2a-child-auth"))
	if err != nil || started.Run.Status != kernel.RunStatusWaitingInput {
		t.Fatalf("started=%#v err=%v", started.Run, err)
	}
	remote.task = completedTask()
	completed, err := runner.LoadRun(t.Context(), started.Run.ID)
	if err != nil || completed.Run.Status != kernel.RunStatusCompleted {
		t.Fatalf("completed=%#v err=%v", completed.Run, err)
	}
}

func TestShadowRunnerRetriesResumeWithStableMessageIdentity(t *testing.T) {
	t.Parallel()
	runtime := newShadowRuntime(t)
	remote := &shadowRemote{task: inputRequiredTask()}
	runner := newShadowRunner(t, runtime, remote)
	started, err := runner.StartRun(t.Context(), shadowStartRequest("a2a-child-resume-retry"))
	if err != nil {
		t.Fatal(err)
	}
	remote.sendErr = errors.New("temporary network failure")
	waiting, err := runner.ResumeRun(t.Context(), started.Run.ID, "approved")
	if err == nil || waiting.Run.Status != kernel.RunStatusWaitingInput {
		t.Fatalf("waiting=%#v err=%v", waiting.Run, err)
	}
	messageID := remote.sent.MessageID
	remote.sendErr = nil
	remote.task = completedTask()
	completed, err := runner.ResumeRun(t.Context(), started.Run.ID, "approved")
	if err != nil || completed.Run.Status != kernel.RunStatusCompleted || remote.sent.MessageID != messageID {
		t.Fatalf("completed=%#v messageID=%q err=%v", completed.Run, remote.sent.MessageID, err)
	}
}

func TestShadowRunnerUpdatesOneRemoteWaitKindWithoutInvalidTransition(t *testing.T) {
	t.Parallel()
	runtime := newShadowRuntime(t)
	remote := &shadowRemote{task: inputRequiredTask()}
	runner := newShadowRunner(t, runtime, remote)
	started, err := runner.StartRun(t.Context(), shadowStartRequest("a2a-child-wait-kind"))
	if err != nil {
		t.Fatal(err)
	}
	remote.task = authRequiredTask()
	updated, err := runner.LoadRun(t.Context(), started.Run.ID)
	if err != nil || updated.Run.Status != kernel.RunStatusWaitingInput || updated.Checkpoint == nil ||
		updated.Checkpoint.Kind != "a2a.auth" {
		t.Fatalf("updated=%#v err=%v", updated, err)
	}
}

func TestShadowRunnerDoesNotPersistTransportErrorDetails(t *testing.T) {
	t.Parallel()
	runtime := newShadowRuntime(t)
	remote := &shadowRemote{
		task: workingTask(), sendErr: errors.New("Bearer secret at https://private.example"),
	}
	runner := newShadowRunner(t, runtime, remote)
	failed, err := runner.StartRun(t.Context(), shadowStartRequest("a2a-child-transport-error"))
	if err == nil || failed.Run.Status != kernel.RunStatusFailed || failed.Run.ErrorDetail != ErrRemoteSendFailed.Error() {
		t.Fatalf("failed=%#v err=%v", failed.Run, err)
	}
	raw, marshalErr := json.Marshal(failed)
	if marshalErr != nil || strings.Contains(string(raw), "Bearer secret") || strings.Contains(string(raw), "private.example") {
		t.Fatalf("persisted transport detail: %s, err=%v", raw, marshalErr)
	}
}

func TestShadowRunnerFailsClosedWhenRemoteIdentityWasNeverPersisted(t *testing.T) {
	t.Parallel()
	runtime := newShadowRuntime(t)
	remote := &shadowRemote{task: workingTask()}
	runner := newShadowRunner(t, runtime, remote)
	state := runner.initialState("a2a-lost")
	encoded, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	_, err = runtime.Create(t.Context(), kernel.CreateRequest{
		ID: "a2a-lost", Kind: RunKind,
		Actor:  kernel.ActorRef{TenantID: testShadowTenantID, ActorID: testShadowActorID},
		Thread: kernel.ThreadRef{Kind: testShadowThreadKind, ID: testShadowThreadID},
		Goal:   "recover", State: encoded,
	})
	if err != nil {
		t.Fatal(err)
	}
	failed, err := runner.LoadRun(t.Context(), "a2a-lost")
	if !errors.Is(err, ErrRemoteIdentityLost) || failed.Run.Status != kernel.RunStatusFailed {
		t.Fatalf("failed=%#v err=%v", failed.Run, err)
	}
}

func newShadowRuntime(t *testing.T) *kernel.Runtime {
	t.Helper()
	runtime, err := kernel.New(kernel.Dependencies{Store: memory.NewStore()})
	if err != nil {
		t.Fatal(err)
	}
	return runtime
}

func newShadowRunner(t *testing.T, runtime *kernel.Runtime, remote *shadowRemote) *ShadowRunner {
	t.Helper()
	runner, err := NewShadowRunner(ShadowDependencies{
		Runtime: runtime, Client: remote,
		Discovery: Discovery{Descriptor: RemoteAgentDescriptor{
			Name: "remote-agent", PreferredURL: testShadowRemoteURL,
			ProtocolVersion: ProtocolVersion, ProtocolBinding: "HTTP+JSON",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return runner
}

func newShadowParent(t *testing.T, runtime *kernel.Runtime) kernel.Snapshot {
	t.Helper()
	parent, err := runtime.Create(t.Context(), kernel.CreateRequest{
		ID: "parent-1", Kind: agent.RunKind,
		Actor:  kernel.ActorRef{TenantID: testShadowTenantID, ActorID: testShadowActorID},
		Thread: kernel.ThreadRef{Kind: testShadowThreadKind, ID: testShadowThreadID},
		Goal:   "parent", State: json.RawMessage(`{"state":"parent"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	return parent
}

func shadowStartRequest(id string) agent.StartRequest {
	return agent.StartRequest{
		ID: id, Actor: kernel.ActorRef{TenantID: testShadowTenantID, ActorID: testShadowActorID},
		Thread: kernel.ThreadRef{Kind: testShadowThreadKind, ID: testShadowThreadID}, Goal: "remote goal",
	}
}

func workingTask() TaskSnapshot {
	return TaskSnapshot{
		ID: "remote-1", ContextID: "context-1", State: "TASK_STATE_WORKING",
		Raw: json.RawMessage(`{"id":"remote-1","contextId":"context-1","status":{"state":"TASK_STATE_WORKING"}}`),
	}
}

func completedTask() TaskSnapshot {
	return TaskSnapshot{
		ID: "remote-1", ContextID: "context-1", State: "TASK_STATE_COMPLETED", Terminal: true,
		Raw: json.RawMessage(`{"id":"remote-1","contextId":"context-1","status":{"state":"TASK_STATE_COMPLETED"}}`),
	}
}

func inputRequiredTask() TaskSnapshot {
	return TaskSnapshot{
		ID: "remote-1", ContextID: "context-1", State: "TASK_STATE_INPUT_REQUIRED",
		Raw: json.RawMessage(`{"id":"remote-1","contextId":"context-1","status":{"state":"TASK_STATE_INPUT_REQUIRED"}}`),
	}
}

func authRequiredTask() TaskSnapshot {
	return TaskSnapshot{
		ID: "remote-1", ContextID: "context-1", State: "TASK_STATE_AUTH_REQUIRED",
		Raw: json.RawMessage(`{"id":"remote-1","contextId":"context-1","status":{"state":"TASK_STATE_AUTH_REQUIRED"}}`),
	}
}

func cloneTaskSnapshot(task *TaskSnapshot) *TaskSnapshot {
	if task == nil {
		return nil
	}
	clone := cloneTaskValue(*task)
	return &clone
}

func cloneTaskValue(task TaskSnapshot) TaskSnapshot {
	task.Raw = append(json.RawMessage(nil), task.Raw...)
	return task
}
