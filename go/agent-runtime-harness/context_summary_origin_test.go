package harness

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	runtimecontext "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/context"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
)

type summaryOriginKernelReadProbe struct {
	*memory.Store
	reads int
}

func (store *summaryOriginKernelReadProbe) Load(ctx context.Context, id string) (kernel.Snapshot, error) {
	store.reads++
	return store.Store.Load(ctx, id)
}

func TestSummaryOriginAuthorizesScopeAndOwnerBeforeNativeRead(t *testing.T) {
	for _, mode := range []string{"foreign Session", "foreign Thread", "foreign Actor"} {
		t.Run(mode, func(t *testing.T) {
			store := NewMemoryStore()
			now := time.Now().UTC()
			actor := kernel.ActorRef{TenantID: "tenant", ActorID: "owner"}
			thread := kernel.ThreadRef{Kind: "conversation", ID: "foreign-thread"}
			hostThread := HostRef{Kind: "conversation", ID: "origin-session"}
			sessionID, err := SessionID(hostThread)
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := store.CreateSession(t.Context(), Session{ID: sessionID, HostThread: hostThread, Actor: actor, Revision: 1, CreatedAt: now, UpdatedAt: now}); err != nil {
				t.Fatal(err)
			}
			hostTurn := HostRef{Kind: "conversation_turn", ID: "origin"}
			turnID, err := TurnID(sessionID, hostTurn)
			if err != nil {
				t.Fatal(err)
			}
			config, err := SealConfigSnapshot(turnID, ConfigSnapshot{WorkingStateSummary: &agent.WorkingStateSummaryPolicy{MaxInputTokens: 4096, MaxSerializedBytes: 16384}, AgentSpec: AgentSpec{MaxModelOutputTokens: 128}}, now)
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := store.PutConfigSnapshot(t.Context(), config); err != nil {
				t.Fatal(err)
			}
			turn := Turn{ID: turnID, SessionID: sessionID, HostTurn: hostTurn, ConfigSnapshotID: config.ID, Status: TurnAccepted, Revision: 1, CreatedAt: now, UpdatedAt: now}
			if _, _, err := store.CreateTurn(t.Context(), turn); err != nil {
				t.Fatal(err)
			}
			root, err := newAgentInvocation(turnID, hostTurn.ID, "original goal", actor, thread, nil, nil, now)
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := store.CreateInvocation(t.Context(), root); err != nil {
				t.Fatal(err)
			}
			probe := &summaryOriginKernelReadProbe{Store: memory.NewStore()}
			runtime, err := kernel.New(kernel.Dependencies{Store: probe})
			if err != nil {
				t.Fatal(err)
			}
			current, owner := turn, kernel.Snapshot{Run: kernel.Run{Actor: actor, Thread: thread}}
			switch mode {
			case "foreign Session":
				current.SessionID = "another-authorized-session"
			case "foreign Thread":
				owner.Run.Thread.ID = "another-authorized-thread"
			case "foreign Actor":
				owner.Run.Actor.ActorID = "another-authorized-actor"
			}
			ports := contextSummaryPorts{store: store, runtime: runtime}
			state := &runtimecontext.WorkingState{Version: 1, Source: runtimecontext.WorkingStateSource{
				CheckpointID: "ctxc_" + strings.Repeat("a", 32), ContentHash: strings.Repeat("b", 64), CoveredThroughSourceID: "source", CoveredPathHash: strings.Repeat("c", 64)},
				Derivation: &runtimecontext.WorkingStateDerivation{Kind: runtimecontext.WorkingStateDerivationSummary, RunID: root.ExecutionRefID,
					InvocationID: "modelinv_" + strings.Repeat("d", 32), RequestHash: strings.Repeat("e", 64), PolicyVersion: agent.WorkingStateSummaryVersion}}
			if err := runtimecontext.ValidateWorkingState(state); err != nil {
				t.Fatal(err)
			}
			if prior := ports.priorSummaryRun(t.Context(), current, owner, state); prior != nil || probe.reads != 0 {
				t.Fatalf("inert foreign provenance caused a native feature read: prior=%v reads=%d", prior != nil, probe.reads)
			}
		})
	}
}
