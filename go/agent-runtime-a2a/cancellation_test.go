package a2a

import (
	"context"
	"errors"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/handoff"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

func TestPluginCancellationUsesFrozenBindingAndChecksRevision(t *testing.T) {
	t.Parallel()
	fixture := newPluginCancellationFixture(t)
	_, err := fixture.plugin.Cancel(t.Context(), fixture.started.Run.ID, fixture.started.Run.Revision-1, "stop")
	if !errors.Is(err, kernel.ErrConflict) || fixture.remote.cancelCalls != 0 {
		t.Fatalf("stale cancellation made remote call: calls=%d err=%v", fixture.remote.cancelCalls, err)
	}
	cancelled, err := fixture.plugin.Cancel(t.Context(), fixture.started.Run.ID, fixture.started.Run.Revision, "stop")
	if err != nil || cancelled.Run.Status != kernel.RunStatusCancelled || !fixture.remote.canceled {
		t.Fatalf("remote cancellation = %#v, %v", cancelled.Run, err)
	}
	if len(fixture.revisions) != 2 || fixture.revisions[1] != "revision-1" {
		t.Fatalf("frozen cancellation revisions = %#v", fixture.revisions)
	}
	if _, err = fixture.plugin.Cancel(t.Context(), cancelled.Run.ID, cancelled.Run.Revision, "stop"); err != nil {
		t.Fatal(err)
	}
	if fixture.remote.cancelCalls != 1 {
		t.Fatalf("terminal remote task cancelled again: %d", fixture.remote.cancelCalls)
	}
}

func TestPluginCancellationRetainsRemoteTaskForRetry(t *testing.T) {
	t.Parallel()
	for _, pending := range []bool{false, true} {
		name := "transport_failure"
		if pending {
			name = "pending_acknowledgement"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			fixture := newPluginCancellationFixture(t)
			wantErr := ErrRemoteCancellationPending
			fixture.remote.cancelPending = pending
			if !pending {
				wantErr = errors.New("temporary remote failure")
				fixture.remote.cancelErr = wantErr
			}
			_, err := fixture.plugin.Cancel(t.Context(), fixture.started.Run.ID, fixture.started.Run.Revision, "stop")
			if !errors.Is(err, wantErr) {
				t.Fatalf("cancellation error = %v, want %v", err, wantErr)
			}
			current, err := fixture.runtime.Load(t.Context(), fixture.started.Run.ID)
			if err != nil || current.Run.Status != kernel.RunStatusRunning {
				t.Fatalf("unconfirmed cancellation became terminal: %#v, %v", current.Run, err)
			}
			fixture.remote.cancelErr, fixture.remote.cancelPending = nil, false
			cancelled, err := fixture.plugin.Cancel(t.Context(), current.Run.ID, current.Run.Revision, "stop")
			if err != nil || cancelled.Run.Status != kernel.RunStatusCancelled || fixture.remote.cancelCalls != 2 {
				t.Fatalf("cancellation retry = %#v calls=%d err=%v", cancelled.Run, fixture.remote.cancelCalls, err)
			}
		})
	}
}

func TestPluginCancellationRejectsChangedFrozenBinding(t *testing.T) {
	t.Parallel()
	fixture := newPluginCancellationFixture(t)
	fixture.bindingRevision = "revision-2"
	_, err := fixture.plugin.Cancel(t.Context(), fixture.started.Run.ID, fixture.started.Run.Revision, "stop")
	if !errors.Is(err, ErrBindingMissing) || fixture.remote.cancelCalls != 0 {
		t.Fatalf("changed binding used for cancellation: calls=%d err=%v", fixture.remote.cancelCalls, err)
	}
}

type pluginCancellationFixture struct {
	runtime         *kernel.Runtime
	plugin          *Plugin
	remote          *shadowRemote
	started         kernel.Snapshot
	revisions       []string
	bindingRevision string
}

func newPluginCancellationFixture(t *testing.T) *pluginCancellationFixture {
	t.Helper()
	fixture := &pluginCancellationFixture{runtime: newShadowRuntime(t), remote: &shadowRemote{task: workingTask()}, bindingRevision: "revision-1"}
	var err error
	fixture.plugin, err = NewPlugin(PluginDependencies{Runtime: fixture.runtime, Bindings: BindingResolverFunc(
		func(_ context.Context, targetID, revision string) (Binding, error) {
			fixture.revisions = append(fixture.revisions, revision)
			return Binding{TargetID: targetID, Revision: fixture.bindingRevision, Client: fixture.remote,
				Discovery: Discovery{Descriptor: RemoteAgentDescriptor{Name: "remote-agent", PreferredURL: testShadowRemoteURL,
					ProtocolVersion: ProtocolVersion, ProtocolBinding: "HTTP+JSON"}}}, nil
		})})
	if err != nil {
		t.Fatal(err)
	}
	child, err := fixture.plugin.ResolveChild(t.Context(), handoff.Delegation{MemberID: "a2a:remote-agent", ChildRunID: "cancel-plugin-child"})
	if err != nil {
		t.Fatal(err)
	}
	fixture.started, err = child.StartRun(t.Context(), shadowStartRequest("cancel-plugin-child"))
	if err != nil {
		t.Fatal(err)
	}
	return fixture
}
