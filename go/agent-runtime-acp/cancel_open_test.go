package acp

import (
	"context"
	"errors"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

// A cancelled original session/new must never be followed by a Prompt,
// even though the remote Session ID was unavailable at cancellation time.
// This is a separate admission window from cancel racing a committed session
// receipt, covered by TestACPCancelBeatsPromptAdmissionWithoutLatePhysicalSend.
func TestACPCancelDuringInitialSessionOpenFencesLatePrompt(t *testing.T) {
	client := &fakeClient{openStarted: make(chan struct{}), openRelease: make(chan struct{})}
	rt, runner := fixture(t, client)
	type outcome struct {
		snap kernel.Snapshot
		err  error
	}
	done := make(chan outcome, 1)
	go func() {
		snap, err := runner.StartRun(context.Background(), startRequest("early-open-cancel"))
		done <- outcome{snap, err}
	}()
	<-client.openStarted
	original, err := rt.Load(t.Context(), "early-open-cancel")
	if err != nil || original.Run.Status != kernel.RunStatusRunning || original.Run.Revision != 2 {
		t.Fatalf("open intent absent: %+v %v", original.Run, err)
	}
	cancelled, err := runner.CancelRemote(t.Context(), original.Run.ID, original.Run.Revision)
	if !errors.Is(err, ErrOutcomeUnknown) || cancelled.Run.Revision != 3 || cancelled.Run.Status != kernel.RunStatusRunning {
		t.Fatalf("cancel lacked durable pre-session fence: %+v %v", cancelled.Run, err)
	}
	var state remoteState
	if err = decodeRemoteState(cancelled.State, &state); err != nil || !state.CancelPending || state.CancelReturned || state.SessionID != "" {
		t.Fatalf("pre-session cancel fabricated a remote receipt: %+v %v", state, err)
	}
	close(client.openRelease)
	returned := <-done
	if !errors.Is(returned.err, ErrOutcomeUnknown) {
		t.Fatalf("late remote session escaped admitted cancellation: %+v %v", returned.snap.Run, returned.err)
	}
	opens, prompts, cancels := client.counts()
	client.mu.Lock()
	abandons := client.abandons
	client.mu.Unlock()
	if opens != 1 || prompts != 0 || cancels != 0 || abandons != 1 {
		t.Fatalf("unadmitted physical work after cancel: opens=%d prompts=%d cancels=%d abandoned=%d", opens, prompts, cancels, abandons)
	}
	if _, err = runner.LoadRun(t.Context(), original.Run.ID); !errors.Is(err, ErrOutcomeUnknown) {
		t.Fatalf("uncertain abandoned session projected terminal: %v", err)
	}
}
