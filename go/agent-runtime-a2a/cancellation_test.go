package a2a

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
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

func TestPluginCancellationPreservesUncertainPhysicalIntentWithoutRetry(t *testing.T) {
	t.Parallel()
	for _, pending := range []bool{false, true} {
		name := "transport_failure"
		if pending {
			name = "pending_acknowledgement"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			fixture := newPluginCancellationFixture(t)
			fixture.remote.cancelPending = pending
			if !pending {
				fixture.remote.cancelErr = errors.New("Bearer private upstream credential")
			}
			_, err := fixture.plugin.Cancel(t.Context(), fixture.started.Run.ID, fixture.started.Run.Revision, "stop")
			wantErr := ErrRemoteOutcomeUnknown
			if pending {
				wantErr = ErrRemoteCancellationPending
			}
			if !errors.Is(err, wantErr) || strings.Contains(err.Error(), "private") {
				t.Fatalf("unverified cancellation became an ACK or leaked provider detail: %v", err)
			}
			current, err := fixture.runtime.Load(t.Context(), fixture.started.Run.ID)
			if err != nil || current.Run.Status != kernel.RunStatusRunning || current.Run.Revision != fixture.started.Run.Revision+2 {
				t.Fatalf("cancel intent not persisted: %#v, %v", current.Run, err)
			}
			state, err := fixture.plugin.certifiedShadow(t.Context(), current)
			if err != nil || state == nil {
				t.Fatalf("cancel intent lost trusted origin: %v", err)
			}
			fixture.remote.cancelErr, fixture.remote.cancelPending = nil, false
			_, err = fixture.plugin.Cancel(t.Context(), current.Run.ID, current.Run.Revision, "stop")
			if !errors.Is(err, ErrRemoteOutcomeUnknown) || fixture.remote.cancelCalls != 1 {
				t.Fatalf("unverified cancellation replayed: calls=%d err=%v", fixture.remote.cancelCalls, err)
			}
			// An independent later Task status may settle the original pending
			// control read-only; no new CancelTask RPC is permitted.
			fixture.remote.task = completedTask()
			fixture.remote.task.State = "TASK_STATE_CANCELED"
			fixture.remote.task.Terminal = true
			fixture.remote.task.Raw = json.RawMessage(`{"id":"remote-1","contextId":"context-1","status":{"state":"TASK_STATE_CANCELED"}}`)
			child, err := fixture.plugin.resolveChild(t.Context(), handoff.Delegation{Remote: true, MemberID: "a2a:remote-agent", MemberRevision: "revision-1", ChildRunID: current.Run.ID})
			if err != nil {
				t.Fatal(err)
			}
			cancelled, err := child.LoadRun(t.Context(), current.Run.ID)
			if err != nil || cancelled.Run.Status != kernel.RunStatusCancelled || fixture.remote.cancelCalls != 1 {
				t.Fatalf("original remote cancel did not reconcile read-only: %+v calls=%d err=%v", cancelled.Run, fixture.remote.cancelCalls, err)
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
	fixture.plugin, err = NewPlugin(PluginDependencies{AttestationKey: testA2AShadowKey(), Runtime: fixture.runtime, Bindings: BindingResolverFunc(
		func(_ context.Context, targetID, revision string) (Binding, error) {
			fixture.revisions = append(fixture.revisions, revision)
			return Binding{TargetID: targetID, Revision: fixture.bindingRevision, Client: fixture.remote,
				Discovery: Discovery{Descriptor: RemoteAgentDescriptor{Name: "remote-agent", PreferredURL: testShadowRemoteURL,
					ProtocolVersion: ProtocolVersion, ProtocolBinding: "HTTP+JSON"}}}, nil
		})})
	if err != nil {
		t.Fatal(err)
	}
	child, err := fixture.plugin.ResolveChild(t.Context(), handoff.Delegation{Remote: true, MemberID: "a2a:remote-agent", ChildRunID: "cancel-plugin-child"})
	if err != nil {
		t.Fatal(err)
	}
	fixture.started, err = child.StartRun(t.Context(), shadowStartRequest("cancel-plugin-child"))
	if err != nil {
		t.Fatal(err)
	}
	return fixture
}
