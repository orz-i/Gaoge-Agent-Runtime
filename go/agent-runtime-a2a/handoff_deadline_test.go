package a2a

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/handoff"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

func TestA2AHandoffParentDeadlineBoundsLocalDispatchNotRemoteCapabilities(t *testing.T) {
	for _, tc := range []struct {
		name        string
		deadline    bool
		expireFirst bool
		expireAfter bool
		cancelFirst bool
	}{
		{name: "no-parent-deadline"},
		{name: "future-parent-deadline", deadline: true},
		{name: "expired-parent-deadline", deadline: true, expireFirst: true},
		{name: "expired-after-original-start", deadline: true, expireAfter: true},
		{name: "caller-cancelled-before-start", cancelFirst: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt := newShadowRuntime(t)
			remote := &shadowRemote{task: workingTask()}
			child := newShadowRunner(t, rt, remote)
			coordinator := newShadowCoordinator(t, child)
			var deadline *time.Time
			if tc.deadline {
				at := time.Now().Add(time.Hour)
				if tc.expireFirst {
					at = time.Now().Add(30 * time.Millisecond)
				} else if tc.expireAfter {
					at = time.Now().Add(time.Second)
				}
				deadline = &at
			}
			parent, err := rt.Create(t.Context(), kernel.CreateRequest{
				ID: "deadline-parent", Kind: "test.parent",
				Actor:      kernel.ActorRef{TenantID: testShadowTenantID, ActorID: testShadowActorID},
				Thread:     kernel.ThreadRef{Kind: testShadowThreadKind, ID: testShadowThreadID},
				DeadlineAt: deadline, Goal: "parent", State: json.RawMessage("{}"),
			})
			if err != nil {
				t.Fatal(err)
			}
			d := handoff.Delegation{Remote: true,
				ID: "deadline-delegation", MemberID: "a2a:remote-member", MemberRevision: "fixture-v1",
				ChildRunID: "deadline-child", Goal: "remote goal", Status: handoff.StatusQueued,
			}
			if tc.expireFirst {
				<-time.After(time.Until(*deadline) + time.Millisecond)
			}
			ctx := t.Context()
			if tc.cancelFirst {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			started, err := coordinator.StartOrLoad(ctx, parent, d)
			if tc.expireFirst || tc.cancelFirst {
				if !errors.Is(err, map[bool]error{true: context.Canceled, false: context.DeadlineExceeded}[tc.cancelFirst]) || remote.sent.MessageID != "" {
					t.Fatalf("expired original parent admitted new remote execution: %+v %v sent=%+v", started, err, remote.sent)
				}
				if _, loadErr := rt.Load(t.Context(), d.ChildRunID); !errors.Is(loadErr, kernel.ErrNotFound) {
					t.Fatalf("expired remote attempt created shadow: %v", loadErr)
				}
				return
			}
			if !errors.Is(err, handoff.ErrChildPending) || started.Status != handoff.StatusRunning ||
				remote.sent.MessageID != d.ChildRunID+":message" {
				t.Fatalf("valid parent lost original remote dispatch: %+v %v sent=%+v", started, err, remote.sent)
			}
			if tc.expireAfter {
				<-time.After(time.Until(*deadline) + time.Millisecond)
			}
			if tc.deadline {
				// Expiry cannot prevent *read-only* recovery of an already
				// admitted external effect or cause a new physical Send.
				remote.task = completedTask()
				completed, refreshErr := coordinator.Refresh(t.Context(), parent, started)
				if refreshErr != nil || completed.Status != handoff.StatusCompleted ||
					remote.sent.MessageID != d.ChildRunID+":message" {
					t.Fatalf("existing original Task lost observation: %+v %v", completed, refreshErr)
				}
			}
		})
	}
}
