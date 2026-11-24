package a2a

import (
	"context"
	"errors"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
)

type auditRejectCancelledStore struct{ kernel.Store }

func (s auditRejectCancelledStore) Load(ctx context.Context, id string) (kernel.Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return kernel.Snapshot{}, err
	}
	return s.Store.Load(ctx, id)
}
func (s auditRejectCancelledStore) Apply(ctx context.Context, id string, rev uint64, m kernel.StoreMutation) (kernel.Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return kernel.Snapshot{}, err
	}
	return s.Store.Apply(ctx, id, rev, m)
}

type auditCancelCtxA2AClient struct {
	*shadowRemote
	cancel context.CancelFunc
}

func (c *auditCancelCtxA2AClient) CancelTask(ctx context.Context, d Discovery, task string) (TaskSnapshot, error) {
	c.cancel()
	return TaskSnapshot{}, ctx.Err()
}
func TestPR151A2ACancelCtxRetainsPhysicalReturn(t *testing.T) {
	rt, err := kernel.New(kernel.Dependencies{Store: auditRejectCancelledStore{Store: memory.NewStore()}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	remote := &auditCancelCtxA2AClient{shadowRemote: &shadowRemote{task: workingTask()}, cancel: cancel}
	runner, err := NewShadowRunner(ShadowDependencies{Runtime: rt, Client: remote, AttestationKey: testA2AShadowKey(), Discovery: Discovery{Descriptor: RemoteAgentDescriptor{Name: "remote", PreferredURL: testShadowRemoteURL, ProtocolVersion: ProtocolVersion, ProtocolBinding: "HTTP+JSON"}}})
	if err != nil {
		t.Fatal(err)
	}
	snap, err := runner.StartRun(t.Context(), shadowStartRequest("audit-a2a-ctx"))
	if err != nil {
		t.Fatal(err)
	}
	_, cancelErr := runner.cancelRunAtRevision(ctx, snap.Run.ID, snap.Run.Revision)
	t.Logf("CancelRun returned err=%v context=%v", cancelErr, ctx.Err())
	pending, err := rt.Load(t.Context(), snap.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	state, err := runner.decodeState(pending)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("original CancelTask callback returned: CancelPending=%t CancelReturned=%t, rev=%d", state.PendingCancel, state.PendingCancelReturned, pending.Run.Revision)
	remote.task = completedTask()
	after, obsErr := runner.LoadRun(t.Context(), snap.Run.ID)
	t.Logf("read-only task recovery status=%s error=%v remote GET count=%d", after.Run.Status, obsErr, 0)
	if !errors.Is(cancelErr, ErrRemoteOutcomeUnknown) || !state.PendingCancel || !state.PendingCancelReturned ||
		obsErr != nil || after.Run.Status != kernel.RunStatusCompleted {
		t.Fatal("A2A original CancelTask callback returned but cannot be reconciled under a cancelled caller context")
	}
}

type auditCancelAfterFirstSend struct {
	*shadowRemote
	cancel context.CancelFunc
}

func (c *auditCancelAfterFirstSend) SendMessage(ctx context.Context, d Discovery, req SendRequest) (Interaction, error) {
	result, err := c.shadowRemote.SendMessage(ctx, d, req)
	c.cancel()
	return result, err
}
func TestPR151A2AFirstTaskIdentityLostOnCallerCancellation(t *testing.T) {
	rt, err := kernel.New(kernel.Dependencies{Store: auditRejectCancelledStore{Store: memory.NewStore()}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	remote := &auditCancelAfterFirstSend{shadowRemote: &shadowRemote{task: workingTask()}, cancel: cancel}
	runner, err := NewShadowRunner(ShadowDependencies{Runtime: rt, Client: remote, AttestationKey: testA2AShadowKey(), Discovery: Discovery{Descriptor: RemoteAgentDescriptor{Name: "remote", PreferredURL: testShadowRemoteURL, ProtocolVersion: ProtocolVersion, ProtocolBinding: "HTTP+JSON"}}})
	if err != nil {
		t.Fatal(err)
	}
	_, startErr := runner.StartRun(ctx, shadowStartRequest("audit-first-ctx"))
	persisted, err := rt.Load(t.Context(), "audit-first-ctx")
	if err != nil {
		t.Fatal(err)
	}
	state, err := runner.decodeState(persisted)
	if err != nil {
		t.Fatal(err)
	}
	_, readErr := runner.LoadRun(t.Context(), persisted.Run.ID)
	t.Logf("SendMessage returned authenticated Task ID and Context ID but persisted task=%q context=%q; start=%v read=%v", state.RemoteTaskID, state.RemoteContextID, startErr, readErr)
	if startErr != nil || readErr != nil || state.RemoteTaskID != "remote-1" ||
		state.RemoteContextID != "context-1" {
		t.Fatal("successful first A2A SendMessage lost its authenticated Task/Context identity")
	}
}

type auditCancelAfterResumeSend struct {
	*uncertainResumeClient
	cancel context.CancelFunc
}

func (c *auditCancelAfterResumeSend) SendMessage(ctx context.Context, d Discovery, req SendRequest) (Interaction, error) {
	result, err := c.uncertainResumeClient.SendMessage(ctx, d, req)
	if req.TaskID != "" {
		c.cancel()
	}
	return result, err
}
func TestPR151A2AResumeReturnProofLostOnCallerCancellation(t *testing.T) {
	rt, err := kernel.New(kernel.Dependencies{Store: auditRejectCancelledStore{Store: memory.NewStore()}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	remote := &auditCancelAfterResumeSend{uncertainResumeClient: newUncertainResumeClient(), cancel: cancel}
	runner := newIdentityTestShadow(t, rt, remote)
	snap, err := runner.StartRun(t.Context(), shadowStartRequest("audit-resume-ctx"))
	if err != nil || snap.Run.Status != kernel.RunStatusWaitingInput {
		t.Fatalf("no initial wait: %+v %v", snap.Run, err)
	}
	_, resumeErr := runner.ResumeRun(ctx, snap.Run.ID, "approved")
	persisted, err := rt.Load(t.Context(), snap.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	state, err := runner.decodeState(persisted)
	if err != nil {
		t.Fatal(err)
	}
	remote.setTask(completedTask())
	_, readErr := runner.LoadRun(t.Context(), snap.Run.ID)
	t.Logf("Resume SendMessage returned, proof PendingMessageID=%q Returned=%t, resumeErr=%v readErr=%v", state.PendingMessageID, state.PendingMessageReturned, resumeErr, readErr)
	if resumeErr != nil || state.PendingMessageID != "" || state.PendingMessageReturned ||
		readErr != nil {
		t.Fatal("A2A resumed SendMessage returned but original Task could not be reconciled")
	}
}
