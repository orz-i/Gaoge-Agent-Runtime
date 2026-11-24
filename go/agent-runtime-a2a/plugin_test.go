package a2a

import (
	"context"
	"errors"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/handoff"
)

func TestPluginResolvesAndFreezesRemoteBindingRevision(t *testing.T) {
	t.Parallel()
	runtime := newShadowRuntime(t)
	remote := &shadowRemote{task: workingTask()}
	requestedRevisions := make([]string, 0, 2)
	plugin, err := NewPlugin(PluginDependencies{AttestationKey: testA2AShadowKey(),
		Runtime: runtime,
		Bindings: BindingResolverFunc(func(_ context.Context, targetID, revision string) (Binding, error) {
			requestedRevisions = append(requestedRevisions, revision)
			return Binding{
				TargetID: targetID, Revision: "revision-1", Client: remote,
				Discovery: Discovery{Descriptor: RemoteAgentDescriptor{
					Name: "remote-agent", PreferredURL: testShadowRemoteURL,
					ProtocolVersion: ProtocolVersion, ProtocolBinding: "HTTP+JSON",
				}},
			}, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	delegation := handoff.Delegation{Remote: true,
		ID: "delegation", MemberID: "a2a:remote-agent", ChildRunID: "a2a-plugin-child",
		Goal: "remote goal", Status: handoff.StatusQueued,
	}
	runner, err := plugin.ResolveChild(t.Context(), delegation)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = runner.StartRun(t.Context(), shadowStartRequest(delegation.ChildRunID)); err != nil {
		t.Fatal(err)
	}
	if _, err = plugin.ResolveChild(t.Context(), delegation); err != nil {
		t.Fatal(err)
	}
	if len(requestedRevisions) != 2 || requestedRevisions[0] != "" || requestedRevisions[1] != "revision-1" {
		t.Fatalf("requested revisions = %#v", requestedRevisions)
	}
}

func TestPluginRejectsChangedFrozenBinding(t *testing.T) {
	t.Parallel()
	runtime := newShadowRuntime(t)
	remote := &shadowRemote{task: workingTask()}
	activeRevision := "revision-1"
	plugin, err := NewPlugin(PluginDependencies{AttestationKey: testA2AShadowKey(),
		Runtime: runtime,
		Bindings: BindingResolverFunc(func(_ context.Context, targetID, _ string) (Binding, error) {
			return Binding{
				TargetID: targetID, Revision: activeRevision, Client: remote,
				Discovery: Discovery{Descriptor: RemoteAgentDescriptor{
					Name: "remote-agent", PreferredURL: testShadowRemoteURL,
					ProtocolVersion: ProtocolVersion, ProtocolBinding: "HTTP+JSON",
				}},
			}, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	delegation := handoff.Delegation{Remote: true,
		ID: "delegation", MemberID: "a2a:remote-agent", ChildRunID: "a2a-plugin-revision",
		Goal: "remote goal", Status: handoff.StatusQueued,
	}
	runner, err := plugin.ResolveChild(t.Context(), delegation)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = runner.StartRun(t.Context(), shadowStartRequest(delegation.ChildRunID)); err != nil {
		t.Fatal(err)
	}
	activeRevision = "revision-2"
	_, err = plugin.ResolveChild(t.Context(), delegation)
	if !errors.Is(err, ErrBindingMissing) {
		t.Fatalf("expected frozen revision rejection, got %v", err)
	}
}

func TestPluginOnlyClaimsA2ATargets(t *testing.T) {
	t.Parallel()
	_, err := NewPlugin(PluginDependencies{AttestationKey: testA2AShadowKey(),
		Runtime: newShadowRuntime(t),
		Bindings: BindingResolverFunc(func(context.Context, string, string) (Binding, error) {
			return Binding{}, ErrBindingMissing
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := ParseTargetMemberID("local-agent"); ok {
		t.Fatal("non-A2A member was claimed")
	}
}
