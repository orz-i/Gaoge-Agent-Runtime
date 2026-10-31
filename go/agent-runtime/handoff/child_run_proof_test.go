package handoff_test

import (
	"context"
	"errors"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/handoff"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

type unverifiedChildRunner struct {
	loadSnapshot  kernel.Snapshot
	loadErr       error
	startSnapshot kernel.Snapshot
	startErr      error
	starts        int
}

func (child *unverifiedChildRunner) LoadRun(context.Context, string) (kernel.Snapshot, error) {
	return child.loadSnapshot, child.loadErr
}

func (child *unverifiedChildRunner) StartRun(context.Context, agent.StartRequest) (kernel.Snapshot, error) {
	child.starts++
	return child.startSnapshot, child.startErr
}

func TestHandoffStartOrLoadRejectsUnverifiedChildSnapshots(t *testing.T) {
	t.Parallel()
	const wantedID = "expected-child"
	for _, tc := range []struct {
		name          string
		fromStart     bool
		id            string
		status        kernel.RunStatus
		expectedStart int
	}{
		{name: "loaded other run", id: "other-child", status: kernel.RunStatusRunning},
		{name: "loaded empty identity", status: kernel.RunStatusRunning},
		{name: "loaded invalid status", id: wantedID, status: "unknown"},
		{name: "started other run", fromStart: true, id: "other-child", status: kernel.RunStatusRunning, expectedStart: 1},
		{name: "started empty identity", fromStart: true, status: kernel.RunStatusRunning, expectedStart: 1},
		{name: "started invalid status", fromStart: true, id: wantedID, status: "unknown", expectedStart: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			child := &unverifiedChildRunner{
				loadSnapshot:  kernel.Snapshot{Run: kernel.Run{ID: tc.id, Kind: agent.RunKind, Status: tc.status}},
				startSnapshot: kernel.Snapshot{Run: kernel.Run{ID: tc.id, Kind: agent.RunKind, Status: tc.status}},
			}
			if tc.fromStart {
				child.loadErr = kernel.ErrNotFound
			}
			coordinator, err := handoff.New(child)
			if err != nil {
				t.Fatal(err)
			}
			delegation := handoff.Delegation{
				ID: "handoff", MemberID: "analyst", ChildRunID: wantedID,
				Goal: "inspect", Status: handoff.StatusQueued,
			}
			parent := verifiedHandoffParent()
			observed, err := coordinator.StartOrLoad(t.Context(), parent, delegation)
			if !errors.Is(err, handoff.ErrChildRunUnverified) || observed.ChildRunID != "" ||
				observed.ID != "" || child.starts != tc.expectedStart {
				t.Fatalf("unverified child was accepted: result=%+v err=%v starts=%d", observed, err, child.starts)
			}
		})
	}
}

func TestHandoffRefreshRejectsUnverifiedChildSnapshots(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		id     string
		status kernel.RunStatus
	}{
		{name: "wrong identity", id: "unrelated", status: kernel.RunStatusCompleted},
		{name: "missing identity", status: kernel.RunStatusRunning},
		{name: "invalid status", id: "expected-child", status: "invalid"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			child := &unverifiedChildRunner{
				loadSnapshot: kernel.Snapshot{Run: kernel.Run{ID: tc.id, Kind: agent.RunKind, Status: tc.status}},
			}
			coordinator, err := handoff.New(child)
			if err != nil {
				t.Fatal(err)
			}
			projected, err := coordinator.Refresh(t.Context(), verifiedHandoffParent(), handoff.Delegation{
				ID: "handoff", MemberID: "analyst", ChildRunID: "expected-child",
				Goal: "inspect", Status: handoff.StatusRunning,
			})
			if !errors.Is(err, handoff.ErrChildRunUnverified) || projected.ID != "" || child.starts != 0 {
				t.Fatalf("invalid refresh projection: result=%+v err=%v", projected, err)
			}
		})
	}
}
