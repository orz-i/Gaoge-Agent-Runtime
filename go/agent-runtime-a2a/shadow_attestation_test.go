package a2a

import (
	"context"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/handoff"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
)

// A read-only shadow attestation must never make even a GET or remote send.
type noRemoteIOAllowed struct{}

func (noRemoteIOAllowed) SendMessage(context.Context, Discovery, SendRequest) (Interaction, error) {
	panic("shadow attestation dispatched a remote message")
}
func (noRemoteIOAllowed) GetTask(context.Context, Discovery, string) (TaskSnapshot, error) {
	panic("shadow attestation performed a remote GET")
}
func (noRemoteIOAllowed) CancelTask(context.Context, Discovery, string) (TaskSnapshot, error) {
	panic("shadow attestation cancelled a remote task")
}

func TestShadowRunnerAttestationRequiresExactFrozenBinding(t *testing.T) {
	original, err := kernel.New(kernel.Dependencies{Store: memory.NewStore()})
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := kernel.New(kernel.Dependencies{Store: memory.NewStore()})
	if err != nil {
		t.Fatal(err)
	}
	discovery := Discovery{Descriptor: RemoteAgentDescriptor{
		Name: "remote", PreferredURL: "https://example.invalid/a2a", ProtocolVersion: ProtocolVersion,
	}}
	child, err := NewShadowRunner(ShadowDependencies{
		Runtime: original, Client: noRemoteIOAllowed{}, Discovery: discovery,
		TargetID: "remote", TargetRevision: "revision-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	state := []byte("{\"remoteName\":\"remote\",\"targetID\":\"remote\",\"targetRevision\":\"revision-1\",\"remoteURL\":\"https://example.invalid/a2a\",\"protocolVersion\":\"1.0\",\"messageID\":\"child:message\",\"remoteTaskID\":\"remote-task\"}")
	snapshot, err := original.Create(t.Context(), kernel.CreateRequest{
		ID: "child", Kind: RunKind, Goal: "finish remotely",
		Actor:  kernel.ActorRef{TenantID: "tenant", ActorID: "42"},
		Thread: kernel.ThreadRef{Kind: "conversation", ID: "one"},
		State:  state,
	})
	if err != nil {
		t.Fatal(err)
	}
	frozen := handoff.Delegation{
		ID: "delegate", MemberID: "a2a:remote", MemberRevision: "revision-1",
		ChildRunID: "child", Goal: "finish remotely", Status: handoff.StatusRunning,
	}
	if !child.ObservesFrozenRemoteShadow(original, frozen, snapshot) {
		t.Fatal("matching immutable first-party shadow was rejected")
	}
	for _, caseName := range []string{"foreign kernel", "revision", "member", "child", "goal", "remote URL"} {
		d, view, runtime := frozen, snapshot, original
		switch caseName {
		case "foreign kernel":
			runtime = foreign
		case "revision":
			d.MemberRevision = "other"
		case "member":
			d.MemberID = "a2a:foreign"
		case "child":
			d.ChildRunID = "foreign-run"
		case "goal":
			d.Goal = "different"
		case "remote URL":
			view.State = []byte("{\"remoteName\":\"remote\",\"targetID\":\"remote\",\"targetRevision\":\"revision-1\",\"remoteURL\":\"https://evil.invalid/a2a\",\"protocolVersion\":\"1.0\",\"messageID\":\"child:message\",\"remoteTaskID\":\"remote-task\"}")
		}
		if child.ObservesFrozenRemoteShadow(runtime, d, view) {
			t.Fatalf("%s changed the frozen identity but was accepted", caseName)
		}
	}
}
