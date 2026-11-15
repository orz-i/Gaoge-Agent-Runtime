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
)

type unverifiedFirstSendClient struct {
	kind                 string
	sends, gets, cancels int
}

func (remote *unverifiedFirstSendClient) SendMessage(_ context.Context, _ Discovery, _ SendRequest) (Interaction, error) {
	remote.sends++
	switch remote.kind {
	case "lost_response":
		return Interaction{}, errors.New("transport reply lost after accepting remote task")
	case "malformed_response":
		return Interaction{}, nil
	case "missing_task_id":
		return Interaction{Task: &TaskSnapshot{ID: "", ContextID: "context", State: "TASK_STATE_WORKING"}}, nil
	case "missing_context_id":
		return Interaction{Task: &TaskSnapshot{ID: "remote", ContextID: "", State: "TASK_STATE_WORKING"}}, nil
	case "unknown_state":
		return Interaction{Task: &TaskSnapshot{ID: "remote", ContextID: "context", State: "TASK_STATE_NEW"}}, nil
	default:
		panic("invalid test case")
	}
}
func (remote *unverifiedFirstSendClient) GetTask(context.Context, Discovery, string) (TaskSnapshot, error) {
	remote.gets++
	return completedTask(), nil
}
func (remote *unverifiedFirstSendClient) CancelTask(context.Context, Discovery, string) (TaskSnapshot, error) {
	remote.cancels++
	return completedTask(), nil
}

// A network error, malformed initial reply, or invalid Task envelope may
// follow a *successful* physical remote submission. Never sign Failed or
// Cancelled as if the provider had reported that terminal state.
func TestPR113UnknownFirstSendRemainsUnresolvedAcrossHandoffAndRestart(t *testing.T) {
	for _, kind := range []string{"lost_response", "malformed_response", "missing_task_id", "missing_context_id", "unknown_state"} {
		t.Run(kind, func(t *testing.T) {
			rt := newShadowRuntime(t)
			parent := newShadowParent(t, rt)
			remote := &unverifiedFirstSendClient{kind: kind}
			newChild := func() *ShadowRunner {
				t.Helper()
				shadow, err := NewShadowRunner(ShadowDependencies{
					Runtime: rt, Client: remote, AttestationKey: testA2AShadowKey(),
					Discovery: Discovery{Descriptor: RemoteAgentDescriptor{
						Name: "remote-agent", PreferredURL: testShadowRemoteURL, ProtocolVersion: ProtocolVersion,
					}},
				})
				if err != nil {
					t.Fatal(err)
				}
				return shadow
			}
			d := handoff.Delegation{ID: "delegation", MemberID: "remote", ChildRunID: "unresolved-child", Goal: "remote goal", Status: handoff.StatusQueued}
			coordinator, err := handoff.New(newChild())
			if err != nil {
				t.Fatal(err)
			}
			initial, startErr := coordinator.StartOrLoad(t.Context(), parent, d)
			if !errors.Is(startErr, ErrRemoteOutcomeUnknown) || initial.Status != handoff.StatusRunning {
				t.Fatalf("unknown external result was incorrectly settled: %+v err=%v", initial, startErr)
			}
			snapshot, err := rt.Load(t.Context(), d.ChildRunID)
			if err != nil || snapshot.Run.Status != kernel.RunStatusRunning || snapshot.Run.Revision != 1 {
				t.Fatalf("initial submitted Run did not preserve its intent: %+v err=%v", snapshot.Run, err)
			}
			state, err := newChild().decodeState(snapshot)
			if err != nil || state.MessageID != d.ChildRunID+":message" || state.RemoteTaskID != "" {
				t.Fatalf("original message proof changed: %+v err=%v", state, err)
			}
			// A restarted Handoff must not retry SendMessage or accept a
			// fabricated remote Task ID by inspecting public Kernel fields.
			reopened, err := handoff.New(newChild())
			if err != nil {
				t.Fatal(err)
			}
			_, retryErr := reopened.StartOrLoad(t.Context(), parent, d)
			if !errors.Is(retryErr, ErrRemoteOutcomeUnknown) || remote.sends != 1 || remote.gets != 0 || remote.cancels != 0 {
				t.Fatalf("replayed physical send or misread remote response: sent=%d get=%d cancel=%d err=%v", remote.sends, remote.gets, remote.cancels, retryErr)
			}
			if updated, loadErr := rt.Load(t.Context(), d.ChildRunID); loadErr != nil || updated.Run.Status != kernel.RunStatusRunning {
				t.Fatalf("unverified reply terminalized after retry: %+v %v", updated.Run, loadErr)
			}
		})
	}
}

// Public A2A Cancel and Resume cannot borrow terminal status or wait state
// from another local Kernel writer. Terminal fast paths MUST authenticate
// the same first-party origin as read-only Handoff observation.
func TestPR113PublicA2AControlsRejectUnsignedSameKernelTwins(t *testing.T) {
	for _, targetStatus := range []kernel.RunStatus{kernel.RunStatusCancelled, kernel.RunStatusCompleted, kernel.RunStatusWaitingInput} {
		t.Run(string(targetStatus), func(t *testing.T) {
			rt := newShadowRuntime(t)
			remote := &unverifiedFirstSendClient{kind: "lost_response"}
			plugin, err := NewPlugin(PluginDependencies{Runtime: rt, AttestationKey: testA2AShadowKey(),
				Bindings: BindingResolverFunc(func(_ context.Context, target, revision string) (Binding, error) {
					return Binding{TargetID: target, Revision: revision, Client: remote,
						Discovery: Discovery{Descriptor: RemoteAgentDescriptor{
							Name: "remote-agent", PreferredURL: testShadowRemoteURL, ProtocolVersion: ProtocolVersion,
						}}}, nil
				}),
			})
			if err != nil {
				t.Fatal(err)
			}
			state := shadowState{RemoteName: "remote-agent", RemoteURL: testShadowRemoteURL, ProtocolVersion: ProtocolVersion,
				MessageID: "forged:message", TargetID: "remote", TargetRevision: "revision-1", RemoteTaskID: "remote-task", RemoteContextID: "remote-context"}
			wire, _ := json.Marshal(state)
			current, err := rt.Create(t.Context(), kernel.CreateRequest{ID: "forged", Kind: RunKind,
				Actor: kernel.ActorRef{TenantID: "tenant", ActorID: "actor"}, Thread: kernel.ThreadRef{Kind: "conversation", ID: "thread"},
				Goal: "remote goal", State: wire})
			if err != nil {
				t.Fatal(err)
			}
			var checkpoint *kernel.Checkpoint
			if targetStatus == kernel.RunStatusWaitingInput {
				checkpoint = &kernel.Checkpoint{ID: "forged:wait", Kind: "a2a.input", Status: kernel.CheckpointPending,
					Payload: json.RawMessage(`{}`), CreatedAt: rt.Now()}
			}
			current, err = rt.Apply(t.Context(), current.Run.ID, current.Run.Revision, kernel.Mutation{Status: targetStatus, State: wire, Checkpoint: checkpoint,
				Result: func() *kernel.Result {
					if targetStatus == kernel.RunStatusCompleted {
						return &kernel.Result{ContentType: "application/json", Content: json.RawMessage(`{}`)}
					}
					return nil
				}()})
			if err != nil {
				t.Fatal(err)
			}
			if targetStatus == kernel.RunStatusWaitingInput {
				_, err = plugin.ResumeRun(t.Context(), current.Run.ID, current.Run.Revision, "approve")
			} else {
				_, err = plugin.Cancel(t.Context(), current.Run.ID, current.Run.Revision, "stop")
			}
			if !errors.Is(err, ErrRemoteBindingChanged) || remote.sends != 0 || remote.gets != 0 || remote.cancels != 0 {
				t.Fatalf("unsigned Kernel Shadow authorized public control: status=%s err=%v I/O=%d/%d/%d", targetStatus, err, remote.sends, remote.gets, remote.cancels)
			}
		})
	}
}

func TestPR113CancelledFirstPartyShadowRemainsReadableWithoutRemoteCleanup(t *testing.T) {
	rt := newShadowRuntime(t)
	remote := &shadowRemote{task: completedTask()}
	plugin, err := NewPlugin(PluginDependencies{Runtime: rt, AttestationKey: testA2AShadowKey(),
		Bindings: BindingResolverFunc(func(_ context.Context, target, rev string) (Binding, error) {
			return Binding{TargetID: target, Revision: rev, Client: remote,
				Discovery: Discovery{Descriptor: RemoteAgentDescriptor{Name: "remote-agent", PreferredURL: testShadowRemoteURL, ProtocolVersion: ProtocolVersion}}}, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	d := handoff.Delegation{ID: "delegate", MemberID: "a2a:remote", MemberRevision: "revision-1", ChildRunID: "completed-shadow", Goal: "remote goal", Status: handoff.StatusQueued}
	child, err := plugin.ResolveChild(t.Context(), d)
	if err != nil {
		t.Fatal(err)
	}
	finished, err := child.StartRun(t.Context(), agent.StartRequest{ID: d.ChildRunID, Actor: kernel.ActorRef{TenantID: "tenant", ActorID: "actor"}, Thread: kernel.ThreadRef{Kind: "conversation", ID: "thread"}, Goal: d.Goal})
	if err != nil || finished.Run.Status != kernel.RunStatusCompleted {
		t.Fatalf("initial completed remote message not committed: %+v %v", finished.Run, err)
	}
	_, err = plugin.Cancel(t.Context(), finished.Run.ID, finished.Run.Revision, "cancel already completed")
	if err != nil || remote.cancelCalls != 0 {
		t.Fatalf("legitimate terminal did not survive proof validation: %v", err)
	}
}

func TestPR113UnknownSendNeverLeaksUpstreamCredentials(t *testing.T) {
	rt := newShadowRuntime(t)
	remote := &shadowRemote{task: workingTask(), sendErr: errors.New("Bearer private credential")}
	runner := newShadowRunner(t, rt, remote)
	state, err := runner.StartRun(t.Context(), shadowStartRequest("transport-secret"))
	if !errors.Is(err, ErrRemoteOutcomeUnknown) || strings.Contains(err.Error(), "private") || state.Run.Status != kernel.RunStatusRunning {
		t.Fatalf("unverified response leaked secret or synthesized failure: %+v %v", state.Run, err)
	}
}
