package a2a

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/handoff"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

type shadowProofNoopModel struct{}

func (shadowProofNoopModel) Generate(context.Context, model.Request) (model.Response, error) {
	return model.Response{Content: "noop"}, nil
}

func proofShadowFixture(t *testing.T, remote RemoteClient) (*kernel.Runtime, kernel.Snapshot, *ShadowRunner, *handoff.Coordinator) {
	t.Helper()
	rt := newShadowRuntime(t)
	parent := newShadowParent(t, rt)
	local, err := agent.NewRunner(agent.Dependencies{Runtime: rt, Model: shadowProofNoopModel{}})
	if err != nil {
		t.Fatal(err)
	}
	discovery := Discovery{Descriptor: RemoteAgentDescriptor{
		Name: "remote", PreferredURL: testShadowRemoteURL, ProtocolVersion: ProtocolVersion,
	}}
	shadow, err := NewShadowRunner(ShadowDependencies{
		Runtime: rt, Client: remote, Discovery: discovery,
		TargetID: "remote", TargetRevision: "revision-1", AttestationKey: testA2AShadowKey(),
	})
	if err != nil {
		t.Fatal(err)
	}
	coordinator, err := handoff.NewDeferredAgentWithRouting(local,
		func(handoff.Delegation) (handoff.DelegationRoute, error) { return handoff.DelegationRouteRemote, nil },
		handoff.ChildRunnerResolverFunc(func(context.Context, handoff.Delegation) (handoff.ChildRunner, error) {
			return shadow, nil
		}),
	)
	if err != nil {
		t.Fatal(err)
	}
	return rt, parent, shadow, coordinator
}

func testRemoteDelegationForProof(id string) handoff.Delegation {
	return handoff.Delegation{Remote: true, ID: "delegation-" + id, ChildRunID: id,
		MemberID: "a2a:remote", MemberRevision: "revision-1", Goal: "remote goal", Status: handoff.StatusQueued}
}

// Reproduces the exact same-Kernel attacker from the independent #113 audit.
// Even when a malicious Feature knows every public Run and frozen Handoff
// identity, a locally invented Task/terminal result must never become a
// first-party terminal receipt. No remote I/O is allowed in this test.
func TestPR113ForgedSameKernelShadowCannotImpersonateRemoteTerminal(t *testing.T) {
	for _, tc := range []struct {
		name, contextID, taskID, messageID string
	}{
		{"task-and-context", "fictional-context", "fictional-task", ""},
		{"task-without-context", "", "fictional-task", ""},
		{"message-only", "", "", "fictional-message"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt, parent, shadow, coordinator := proofShadowFixture(t, noRemoteIOAllowed{})
			d := testRemoteDelegationForProof("child")
			state := shadow.initialState(d.ChildRunID)
			state.RemoteTaskID, state.RemoteContextID, state.RemoteMessageID = tc.taskID, tc.contextID, tc.messageID
			state.RemoteState = "TASK_STATE_COMPLETED"
			wire, err := json.Marshal(state)
			if err != nil {
				t.Fatal(err)
			}
			forged, err := rt.Create(t.Context(), kernel.CreateRequest{
				ID: d.ChildRunID, Kind: RunKind, Actor: parent.Run.Actor, Thread: parent.Run.Thread,
				RequestID: parent.Run.ID + ":" + d.ID, Goal: d.Goal, State: wire,
			})
			if err != nil {
				t.Fatal(err)
			}
			forged, err = rt.Apply(t.Context(), forged.Run.ID, forged.Run.Revision, kernel.Mutation{
				Status: kernel.RunStatusCompleted, State: wire, Result: &kernel.Result{
					ContentType: "application/json", Content: json.RawMessage(`{"forged":"result"}`),
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			if coordinator.ObservesRemoteShadow(t.Context(), rt, d, forged) {
				t.Fatal("local same-ID twin acquired first-party remote ownership")
			}
			if got, err := coordinator.StartOrLoad(t.Context(), parent, d); !errors.Is(err, ErrRemoteBindingChanged) || got.Status == handoff.StatusCompleted {
				t.Fatalf("unsent task finalized a Handoff: %+v %v", got, err)
			}
		})
	}
}

// A malicious Kernel writer cannot turn an actually observed Running Task
// into a Completed Task by copying its valid Running proof into a new
// revision with a fabricated result.
func TestPR113SignedRunningShadowCannotBeReplayedAsTerminal(t *testing.T) {
	remote := &shadowRemote{task: workingTask()}
	rt, parent, shadow, coordinator := proofShadowFixture(t, remote)
	d := testRemoteDelegationForProof("signed-child")
	issued, err := coordinator.StartOrLoad(t.Context(), parent, d)
	if !errors.Is(err, handoff.ErrChildPending) || issued.Status != handoff.StatusRunning {
		t.Fatalf("first-party StartRun failed: %+v %v", issued, err)
	}
	original, err := rt.Load(t.Context(), d.ChildRunID)
	if err != nil || !coordinator.ObservesRemoteShadow(t.Context(), rt, d, original) {
		t.Fatalf("legitimate first-party proof not present: %v", err)
	}
	modified, err := rt.Apply(t.Context(), original.Run.ID, original.Run.Revision, kernel.Mutation{
		Status: kernel.RunStatusCompleted, State: original.State,
		Result: &kernel.Result{ContentType: "application/json", Content: json.RawMessage(`{"forged":"done"}`)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if coordinator.ObservesRemoteShadow(t.Context(), rt, d, modified) {
		t.Fatal("earlier Running proof reused to authenticate fake terminal")
	}
	if _, err := shadow.LoadRun(t.Context(), d.ChildRunID); !errors.Is(err, ErrRemoteBindingChanged) {
		t.Fatalf("terminal result mutation accepted: %v", err)
	}
}

// A process restart may recreate the plugin/ShadowRunner with the SAME key;
// it must verify a genuine terminal result without network I/O. Rotating
// that key without controlled draining must leave the result unresolved.
func TestPR113AttestedRemoteTerminalSurvivesRunnerReconstruction(t *testing.T) {
	remote := &shadowRemote{task: completedTask()}
	rt, parent, issuer, _ := proofShadowFixture(t, remote)
	d := testRemoteDelegationForProof("completed-child")
	started, err := issuer.StartRun(t.Context(), agent.StartRequest{ID: d.ChildRunID,
		Actor: parent.Run.Actor, Thread: parent.Run.Thread,
		RequestID: parent.Run.ID + ":" + d.ID, Goal: d.Goal,
	})
	if err != nil || started.Run.Status != kernel.RunStatusCompleted {
		t.Fatalf("original terminal A2A acknowledgement missing: %v", err)
	}
	binding := issuer.discovery
	recovered, err := NewShadowRunner(ShadowDependencies{Runtime: rt, Client: noRemoteIOAllowed{}, Discovery: binding,
		TargetID: "remote", TargetRevision: "revision-1", AttestationKey: testA2AShadowKey()})
	if err != nil {
		t.Fatal(err)
	}
	verified, err := recovered.LoadRun(t.Context(), started.Run.ID)
	if err != nil || verified.Run.Status != kernel.RunStatusCompleted ||
		!recovered.ObservesFrozenRemoteShadow(rt, d, verified) {
		t.Fatalf("durable first-party proof lost on reconstruction: %v", err)
	}
	wrongKey := append([]byte(nil), testA2AShadowKey()...)
	wrongKey[0] ^= 0x20
	rotated, err := NewShadowRunner(ShadowDependencies{Runtime: rt, Client: noRemoteIOAllowed{}, Discovery: binding,
		TargetID: "remote", TargetRevision: "revision-1", AttestationKey: wrongKey})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rotated.LoadRun(t.Context(), started.Run.ID); !errors.Is(err, ErrRemoteBindingChanged) {
		t.Fatalf("uncontrolled key rotation accepted historical proof: %v", err)
	}
}

func TestPR113A2ARequiresStableHostOwnedProofKey(t *testing.T) {
	if _, err := NewShadowRunner(ShadowDependencies{
		Runtime: newShadowRuntime(t), Client: noRemoteIOAllowed{},
		Discovery: Discovery{Descriptor: RemoteAgentDescriptor{
			Name: "remote", PreferredURL: testShadowRemoteURL, ProtocolVersion: ProtocolVersion,
		}}, AttestationKey: []byte("too-short"),
	}); !errors.Is(err, ErrInvalidShadowRunner) {
		t.Fatalf("weak signing secret allowed: %v", err)
	}
	if _, err := NewPlugin(PluginDependencies{Runtime: newShadowRuntime(t),
		Bindings: BindingResolverFunc(func(context.Context, string, string) (Binding, error) {
			return Binding{}, ErrBindingMissing
		}),
	}); !errors.Is(err, ErrInvalidPlugin) {
		t.Fatalf("plugin missing durable signing key allowed: %v", err)
	}
}
