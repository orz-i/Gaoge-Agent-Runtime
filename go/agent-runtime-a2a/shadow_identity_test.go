package a2a

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

// A hostile/misrouted remote response is not authority to change the original
// Shadow Run identity, even if its state equals the one already persisted.
type identityReplyRemote struct {
	*shadowRemote
	sendReply   *Interaction
	cancelReply *TaskSnapshot
	lastGetID   string
}

func (remote *identityReplyRemote) SendMessage(ctx context.Context, discovery Discovery, request SendRequest) (Interaction, error) {
	if remote.sendReply == nil {
		return remote.shadowRemote.SendMessage(ctx, discovery, request)
	}
	remote.sent = request
	return *remote.sendReply, nil
}
func (remote *identityReplyRemote) GetTask(ctx context.Context, discovery Discovery, taskID string) (TaskSnapshot, error) {
	remote.lastGetID = taskID
	return remote.shadowRemote.GetTask(ctx, discovery, taskID)
}
func (remote *identityReplyRemote) CancelTask(ctx context.Context, discovery Discovery, taskID string) (TaskSnapshot, error) {
	if remote.cancelReply == nil {
		return remote.shadowRemote.CancelTask(ctx, discovery, taskID)
	}
	remote.cancelCalls++
	return *remote.cancelReply, nil
}

func newIdentityTestShadow(t *testing.T, rt *kernel.Runtime, client RemoteClient) *ShadowRunner {
	t.Helper()
	runner, err := NewShadowRunner(ShadowDependencies{AttestationKey: testA2AShadowKey(), Runtime: rt, Client: client, Discovery: Discovery{
		Descriptor: RemoteAgentDescriptor{Name: "remote-agent", PreferredURL: testShadowRemoteURL,
			ProtocolVersion: ProtocolVersion, ProtocolBinding: "HTTP+JSON"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	return runner
}

func assertUnchangedShadow(t *testing.T, rt *kernel.Runtime, before, observed kernel.Snapshot) {
	t.Helper()
	after, err := rt.Load(t.Context(), before.Run.ID)
	if err != nil || !reflect.DeepEqual(after, before) || !reflect.DeepEqual(observed, before) {
		t.Fatalf("foreign response changed original shadow: before=%+v observed=%+v after=%+v err=%v", before, observed, after, err)
	}
}

func TestShadowGetTaskRejectsForeignTaskAndContextBeforeSameStateFastPath(t *testing.T) {
	for _, tc := range []struct {
		name         string
		corrupt      func(*TaskSnapshot)
		changedState bool
	}{
		{"same-state-other-task", func(task *TaskSnapshot) { task.ID = "foreign-task" }, false},
		{"same-state-other-context", func(task *TaskSnapshot) { task.ContextID = "foreign-context" }, false},
		{"same-state-both-foreign", func(task *TaskSnapshot) { task.ID = "foreign-task"; task.ContextID = "foreign-context" }, false},
		{"changed-state-other-task", func(task *TaskSnapshot) { task.ID = "foreign-task" }, true},
		{"changed-state-other-context", func(task *TaskSnapshot) { task.ContextID = "foreign-context" }, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt := newShadowRuntime(t)
			remote := &identityReplyRemote{shadowRemote: &shadowRemote{task: workingTask()}}
			runner := newIdentityTestShadow(t, rt, remote)
			started, err := runner.StartRun(t.Context(), shadowStartRequest("shadow-get"))
			if err != nil {
				t.Fatal(err)
			}
			before, err := rt.Load(t.Context(), started.Run.ID)
			if err != nil {
				t.Fatal(err)
			}
			if tc.changedState {
				remote.task = completedTask()
			}
			tc.corrupt(&remote.task)
			returned, err := runner.LoadRun(t.Context(), started.Run.ID)
			if !errors.Is(err, ErrRemoteBindingChanged) {
				t.Fatalf("foreign GetTask identity accepted: err=%v status=%s", err, returned.Run.Status)
			}
			if remote.lastGetID != "remote-1" {
				t.Fatalf("GetTask used unfrozen remote ID %q", remote.lastGetID)
			}
			assertUnchangedShadow(t, rt, before, returned)
		})
	}
}

func TestShadowCancelRejectsForeignTaskResponseWithoutSettling(t *testing.T) {
	for _, tc := range []struct {
		name    string
		corrupt func(*TaskSnapshot)
	}{
		{"other-task", func(s *TaskSnapshot) { s.ID = "foreign-task" }},
		{"other-context", func(s *TaskSnapshot) { s.ContextID = "foreign-context" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt := newShadowRuntime(t)
			remote := &identityReplyRemote{shadowRemote: &shadowRemote{task: workingTask()}}
			runner := newIdentityTestShadow(t, rt, remote)
			started, err := runner.StartRun(t.Context(), shadowStartRequest("shadow-cancel"))
			if err != nil {
				t.Fatal(err)
			}
			before, err := rt.Load(t.Context(), started.Run.ID)
			if err != nil {
				t.Fatal(err)
			}
			foreign := completedTask()
			foreign.State = "TASK_STATE_CANCELED"
			foreign.Terminal = true
			tc.corrupt(&foreign)
			remote.cancelReply = &foreign
			returned, err := runner.CancelRun(t.Context(), started.Run.ID)
			if !errors.Is(err, ErrRemoteBindingChanged) || remote.cancelCalls != 1 {
				t.Fatalf("foreign CancelTask accepted: err=%v calls=%d", err, remote.cancelCalls)
			}
			assertUnchangedShadow(t, rt, before, returned)
		})
	}
}

func TestShadowResumeRejectsForeignMessageIdentityWithoutCompleting(t *testing.T) {
	for _, tc := range []struct{ name, taskID, contextID string }{
		{"foreign-context", "remote-1", "foreign-context"},
		{"foreign-task", "foreign-task", "context-1"},
		{"missing-context", "remote-1", ""},
		{"missing-task", "", "context-1"},
		{"valid-bound-identity", "remote-1", "context-1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt := newShadowRuntime(t)
			remote := &identityReplyRemote{shadowRemote: &shadowRemote{task: inputRequiredTask()}}
			runner := newIdentityTestShadow(t, rt, remote)
			started, err := runner.StartRun(t.Context(), shadowStartRequest("shadow-resume-message"))
			if err != nil || started.Run.Status != kernel.RunStatusWaitingInput {
				t.Fatalf("start=%+v err=%v", started.Run, err)
			}
			remote.sendReply = &Interaction{Message: &MessageSnapshot{
				ID: "reply", TaskID: tc.taskID, ContextID: tc.contextID, Role: "agent",
				Parts: []ContentPart{{Kind: ContentPartText, Text: "done"}},
			}, Raw: json.RawMessage(`{"messageId":"reply"}`)}
			returned, err := runner.ResumeRun(t.Context(), started.Run.ID, "approved")
			if tc.name == "valid-bound-identity" {
				if err != nil || returned.Run.Status != kernel.RunStatusCompleted {
					t.Fatalf("valid bound message refused: status=%s err=%v", returned.Run.Status, err)
				}
				return
			}
			if !errors.Is(err, ErrRemoteBindingChanged) {
				t.Fatalf("foreign Message completed shadow: status=%s err=%v", returned.Run.Status, err)
			}
			after, loadErr := rt.Load(t.Context(), started.Run.ID)
			if loadErr != nil || after.Run.Status == kernel.RunStatusCompleted || after.Result != nil {
				t.Fatalf("foreign message settled shadow: %+v err=%v", after.Run, loadErr)
			}
			var state shadowState
			if e := json.Unmarshal(after.State, &state); e != nil {
				t.Fatal(e)
			}
			if state.RemoteTaskID != "remote-1" || state.RemoteContextID != "context-1" || state.PendingMessageID == "" || state.RemoteMessageID != "" {
				t.Fatalf("foreign Message poisoned original remote binding or cleared pending receipt: %+v", state)
			}
			// Only a later verified read of the *original* task may finish it.
			remote.task = completedTask()
			recovered, refreshErr := runner.LoadRun(t.Context(), started.Run.ID)
			if refreshErr != nil || recovered.Run.Status != kernel.RunStatusCompleted || remote.lastGetID != "remote-1" {
				t.Fatalf("original task failed to reconcile: status=%s err=%v queried=%q", recovered.Run.Status, refreshErr, remote.lastGetID)
			}
		})
	}
}

func TestShadowResumeRejectsForeignTaskResponseWithoutPoisoningBinding(t *testing.T) {
	for _, tc := range []struct {
		name    string
		corrupt func(*TaskSnapshot)
	}{
		{"foreign-task", func(s *TaskSnapshot) { s.ID = "foreign-task" }},
		{"foreign-context", func(s *TaskSnapshot) { s.ContextID = "foreign-context" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt := newShadowRuntime(t)
			remote := &identityReplyRemote{shadowRemote: &shadowRemote{task: inputRequiredTask()}}
			runner := newIdentityTestShadow(t, rt, remote)
			started, err := runner.StartRun(t.Context(), shadowStartRequest("shadow-resume-task"))
			if err != nil {
				t.Fatal(err)
			}
			foreign := completedTask()
			tc.corrupt(&foreign)
			remote.sendReply = &Interaction{Task: &foreign, Raw: append(json.RawMessage(nil), foreign.Raw...)}
			returned, err := runner.ResumeRun(t.Context(), started.Run.ID, "approved")
			if !errors.Is(err, ErrRemoteBindingChanged) {
				t.Fatalf("foreign Task accepted: status=%s err=%v", returned.Run.Status, err)
			}
			after, loadErr := rt.Load(t.Context(), started.Run.ID)
			if loadErr != nil || after.Run.Status == kernel.RunStatusCompleted || after.Result != nil {
				t.Fatalf("foreign Task settled original shadow: run=%+v err=%v", after.Run, loadErr)
			}
			var state shadowState
			if e := json.Unmarshal(after.State, &state); e != nil {
				t.Fatal(e)
			}
			if state.RemoteTaskID != "remote-1" || state.RemoteContextID != "context-1" || state.PendingMessageID == "" {
				t.Fatalf("foreign Task changed binding: %+v", state)
			}
		})
	}
}

func TestShadowCancelRejectsForeignGetTaskBeforeDispatchingCancel(t *testing.T) {
	rt := newShadowRuntime(t)
	remote := &identityReplyRemote{shadowRemote: &shadowRemote{task: workingTask()}}
	runner := newIdentityTestShadow(t, rt, remote)
	started, err := runner.StartRun(t.Context(), shadowStartRequest("shadow-cancel-preflight"))
	if err != nil {
		t.Fatal(err)
	}
	before, err := rt.Load(t.Context(), started.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	remote.task.ContextID = "foreign-context"
	observed, err := runner.CancelRun(t.Context(), started.Run.ID)
	if !errors.Is(err, ErrRemoteBindingChanged) || remote.cancelCalls != 0 {
		t.Fatalf("Cancel dispatched despite foreign GetTask response: err=%v calls=%d", err, remote.cancelCalls)
	}
	assertUnchangedShadow(t, rt, before, observed)
}

func TestShadowWaitRefreshRejectsSameStateForeignIdentity(t *testing.T) {
	rt := newShadowRuntime(t)
	remote := &identityReplyRemote{shadowRemote: &shadowRemote{task: inputRequiredTask()}}
	runner := newIdentityTestShadow(t, rt, remote)
	started, err := runner.StartRun(t.Context(), shadowStartRequest("shadow-wait-refresh"))
	if err != nil {
		t.Fatal(err)
	}
	before, err := rt.Load(t.Context(), started.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	remote.task.ID = "foreign-task"
	observed, err := runner.LoadRun(t.Context(), started.Run.ID)
	if !errors.Is(err, ErrRemoteBindingChanged) {
		t.Fatalf("same-state waiting task changed owner without error: %v", err)
	}
	assertUnchangedShadow(t, rt, before, observed)
}

func TestShadowStartAcceptsDirectMessageWithoutPriorTaskBinding(t *testing.T) {
	for _, contextID := range []string{"new-context", ""} {
		t.Run("context="+contextID, func(t *testing.T) {
			rt := newShadowRuntime(t)
			remote := &identityReplyRemote{shadowRemote: &shadowRemote{task: workingTask()}}
			remote.sendReply = &Interaction{Message: &MessageSnapshot{
				ID: "direct-reply", ContextID: contextID, Role: "agent",
				Parts: []ContentPart{{Kind: ContentPartText, Text: "done"}},
			}, Raw: json.RawMessage(`{"messageId":"direct-reply"}`)}
			runner := newIdentityTestShadow(t, rt, remote)
			completed, err := runner.StartRun(t.Context(), shadowStartRequest("shadow-direct-message"))
			if err != nil || completed.Run.Status != kernel.RunStatusCompleted {
				t.Fatalf("first direct Message rejected: status=%s err=%v", completed.Run.Status, err)
			}
			var state shadowState
			if err := json.Unmarshal(completed.State, &state); err != nil {
				t.Fatal(err)
			}
			if state.RemoteContextID != contextID || state.RemoteTaskID != "" || state.RemoteMessageID != "direct-reply" {
				t.Fatalf("first Message binding changed: %+v", state)
			}
		})
	}
}

func TestShadowTaskRejectsForeignNestedEvidence(t *testing.T) {
	for _, tc := range []struct {
		name    string
		corrupt func(*TaskSnapshot)
	}{
		{"status-message-other-task", func(task *TaskSnapshot) {
			task.StatusMessage = &MessageSnapshot{ID: "foreign-msg", TaskID: "other-task", ContextID: "context-1", Parts: []ContentPart{{Kind: ContentPartText, Text: "private answer"}}}
		}},
		{"status-message-other-context", func(task *TaskSnapshot) {
			task.StatusMessage = &MessageSnapshot{ID: "foreign-msg", TaskID: "remote-1", ContextID: "other-context", Parts: []ContentPart{{Kind: ContentPartText, Text: "private answer"}}}
		}},
		{"history-other-task", func(task *TaskSnapshot) {
			task.History = []MessageSnapshot{{ID: "foreign-msg", TaskID: "other-task", ContextID: "context-1"}}
		}},
		{"history-other-context", func(task *TaskSnapshot) {
			task.History = []MessageSnapshot{{ID: "foreign-msg", TaskID: "remote-1", ContextID: "other-context"}}
		}},
		{"artifact-other-task", func(task *TaskSnapshot) {
			task.Artifacts = []ArtifactSnapshot{{ID: "foreign-artifact", TaskID: "other-task", ContextID: "context-1"}}
		}},
		{"artifact-other-context", func(task *TaskSnapshot) {
			task.Artifacts = []ArtifactSnapshot{{ID: "foreign-artifact", TaskID: "remote-1", ContextID: "other-context"}}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt := newShadowRuntime(t)
			remote := &identityReplyRemote{shadowRemote: &shadowRemote{task: workingTask()}}
			runner := newIdentityTestShadow(t, rt, remote)
			started, err := runner.StartRun(t.Context(), shadowStartRequest("shadow-nested-evidence"))
			if err != nil {
				t.Fatal(err)
			}
			before, err := rt.Load(t.Context(), started.Run.ID)
			if err != nil {
				t.Fatal(err)
			}
			remote.task = completedTask()
			tc.corrupt(&remote.task)
			observed, err := runner.LoadRun(t.Context(), started.Run.ID)
			if !errors.Is(err, ErrRemoteBindingChanged) {
				t.Fatalf("foreign nested task evidence accepted: status=%s err=%v", observed.Run.Status, err)
			}
			assertUnchangedShadow(t, rt, before, observed)
		})
	}
}

func TestShadowRejectsAmbiguousMessageAndTaskResult(t *testing.T) {
	rt := newShadowRuntime(t)
	remote := &identityReplyRemote{shadowRemote: &shadowRemote{task: inputRequiredTask()}}
	runner := newIdentityTestShadow(t, rt, remote)
	started, err := runner.StartRun(t.Context(), shadowStartRequest("shadow-ambiguous-result"))
	if err != nil {
		t.Fatal(err)
	}
	correct := completedTask()
	remote.sendReply = &Interaction{Task: &correct, Message: &MessageSnapshot{
		ID: "alien-reply", TaskID: "foreign-task", ContextID: "foreign-context",
	}, Raw: json.RawMessage(`{"id":"remote-1"}`)}
	observed, err := runner.ResumeRun(t.Context(), started.Run.ID, "approved")
	if !errors.Is(err, ErrInvalidResult) {
		t.Fatalf("ambiguous union accepted: status=%s err=%v", observed.Run.Status, err)
	}
	persisted, loadErr := rt.Load(t.Context(), started.Run.ID)
	if loadErr != nil || persisted.Run.Status == kernel.RunStatusCompleted || persisted.Result != nil {
		t.Fatalf("ambiguous remote result completed shadow: status=%s err=%v", persisted.Run.Status, loadErr)
	}
}

func TestShadowTaskAcceptsSameIdentityNestedEvidence(t *testing.T) {
	rt := newShadowRuntime(t)
	remote := &identityReplyRemote{shadowRemote: &shadowRemote{task: workingTask()}}
	runner := newIdentityTestShadow(t, rt, remote)
	started, err := runner.StartRun(t.Context(), shadowStartRequest("shadow-valid-nested"))
	if err != nil {
		t.Fatal(err)
	}
	remote.task = completedTask()
	remote.task.StatusMessage = &MessageSnapshot{ID: "status", TaskID: "remote-1", ContextID: "context-1"}
	remote.task.History = []MessageSnapshot{{ID: "history", TaskID: "remote-1", ContextID: "context-1"}, {ID: "legacy-optional-fields"}}
	remote.task.Artifacts = []ArtifactSnapshot{{ID: "result", TaskID: "remote-1", ContextID: "context-1"}}
	settled, err := runner.LoadRun(t.Context(), started.Run.ID)
	if err != nil || settled.Run.Status != kernel.RunStatusCompleted {
		t.Fatalf("correctly bound task nested evidence rejected: status=%s err=%v", settled.Run.Status, err)
	}
}

func TestShadowGetTaskChecksSameStateNestedEvidence(t *testing.T) {
	rt := newShadowRuntime(t)
	remote := &identityReplyRemote{shadowRemote: &shadowRemote{task: workingTask()}}
	runner := newIdentityTestShadow(t, rt, remote)
	started, err := runner.StartRun(t.Context(), shadowStartRequest("shadow-same-nested"))
	if err != nil {
		t.Fatal(err)
	}
	before, err := rt.Load(t.Context(), started.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	remote.task.StatusMessage = &MessageSnapshot{ID: "foreign-status", TaskID: "other-task", ContextID: "context-1"}
	observed, err := runner.LoadRun(t.Context(), started.Run.ID)
	if !errors.Is(err, ErrRemoteBindingChanged) {
		t.Fatalf("same-state status message mismatch returned %v", err)
	}
	assertUnchangedShadow(t, rt, before, observed)
}
