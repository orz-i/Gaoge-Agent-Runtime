package harness_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	runtimecontext "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/context"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/runfeed"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

func sessionTestFeed(t *testing.T) *harness.TurnFeed {
	t.Helper()
	raw, err := runfeed.New(memory.NewRunFeedStore(), runfeed.Options{PollInterval: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	feed, err := harness.NewTurnFeed(raw)
	if err != nil {
		t.Fatal(err)
	}
	return feed
}

func TestSessionSubscribeUsesExecutingRunnerReplayAndLiveFeed(t *testing.T) {
	calls := 0
	client := &agentSessionModel{generate: func(request model.Request) (model.Response, error) {
		for _, message := range request.Messages {
			if message.Role == model.RoleTool {
				return model.Response{Content: "approved"}, nil
			}
		}
		return model.Response{ToolCalls: []tools.Call{{ID: "write", ToolKey: "write", Arguments: json.RawMessage(`{}`)}}}, nil
	}}
	options := agentSessionOptions(t, memory.NewStore(), harness.NewMemoryStore(), client)
	options.TurnFeed = sessionTestFeed(t)
	options.LocalTools = []harness.SessionTool{{Registration: agentSessionTool("write", &calls), Policy: harness.ToolPolicySnapshot{
		DefinitionVersion: "v1", ApprovalMode: "always", ApprovalCapability: "per_call", SideEffectLevel: "write",
	}}}
	session, err := harness.OpenAgentSession(options)
	if err != nil {
		t.Fatal(err)
	}
	waiting, err := session.Prompt(t.Context(), "turn", "write with approval")
	if err != nil || waiting.Turn.Status != harness.TurnWaitingInput || calls != 0 {
		t.Fatalf("approval setup: %v status=%s", err, waiting.Turn.Status)
	}
	subscription, err := session.Subscribe(t.Context(), "turn", 0)
	if err != nil {
		t.Fatal(err)
	}
	defer subscription.Close()
	var after int64
	for _, event := range subscription.Replay {
		if event.TurnID != waiting.Turn.ID || event.Seq <= after || event.Terminal {
			t.Fatalf("wrong replay: %+v", event)
		}
		after = event.Seq
	}
	if after == 0 || len(client.requests) != 1 || calls != 0 {
		t.Fatal("subscription lost replay or executed work")
	}
	// Exercise the existing live transport while the original approval remains
	// pending. Terminal delivery is reconciled from retained events below.
	live, err := options.TurnFeed.Publish(t.Context(), waiting.Turn.ID, harness.TurnEventDraft{
		Type: harness.EventItemDelta, ItemID: "host-preview", ItemKind: harness.ItemAgentMessage, Delta: "pending approval",
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	select {
	case event, open := <-subscription.Events:
		if !open || event.Seq != live.Seq || event.TurnID != waiting.Turn.ID || event.Delta != live.Delta {
			t.Fatalf("wrong live event: %+v open=%t", event, open)
		}
		after = event.Seq
	case err := <-subscription.Errors:
		t.Fatalf("live feed failed: %v", err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	subscription.Close()
	root, _ := harness.TopLevelInvocation(waiting)
	run, err := options.Runtime.Load(t.Context(), root.ExecutionRefID)
	if err != nil || run.Checkpoint == nil {
		t.Fatalf("missing original approval: %v", err)
	}
	done, err := session.ResolveApproval(t.Context(), "turn", harness.ResolveApprovalRequest{CheckpointID: run.Checkpoint.ID, Decision: harness.ApprovalApprove})
	if err != nil || done.Turn.Status != harness.TurnCompleted || calls != 1 {
		t.Fatalf("original approval: %v status=%s calls=%d", err, done.Turn.Status, calls)
	}
	options.Session.Transcript = nil
	reopened, err := harness.OpenAgentSession(options)
	if err != nil {
		t.Fatal(err)
	}
	continued, err := reopened.Subscribe(t.Context(), "turn", after)
	if err != nil {
		t.Fatal(err)
	}
	defer continued.Close()
	terminal := false
	for _, event := range continued.Replay {
		if event.TurnID != done.Turn.ID || event.Seq <= after {
			t.Fatalf("reopened cursor repeated events: %+v", event)
		}
		after = event.Seq
		terminal = terminal || event.Terminal && event.Type == harness.EventTurnCompleted
	}
	if !terminal || len(client.requests) != 2 || calls != 1 {
		t.Fatal("reopened feed lost committed completion or repeated effects")
	}
	if _, err := reopened.Subscribe(t.Context(), "turn", -1); !errors.Is(err, harness.ErrInvalidRequest) {
		t.Fatalf("negative cursor accepted: %v", err)
	}
}

func TestSessionSubscribeAuthenticatesOriginalActorAndThread(t *testing.T) {
	client := &agentSessionModel{}
	options := agentSessionOptions(t, memory.NewStore(), harness.NewMemoryStore(), client)
	options.TurnFeed = sessionTestFeed(t)
	owner, err := harness.OpenAgentSession(options)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Prompt(t.Context(), "turn", "private prompt"); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"foreign actor", "foreign thread", "forged first owner reads", "no feed"} {
		t.Run(mode, func(t *testing.T) {
			changed := options
			changed.Session.Transcript = nil
			var spoof *auditTwoSourceOwnerSwapStore
			switch mode {
			case "foreign actor":
				changed.Session.Actor.ActorID = "intruder"
			case "foreign thread":
				changed.Session.Thread.ID = "unrelated"
			case "forged first owner reads":
				changed.Session.Actor = kernel.ActorRef{TenantID: "foreign", ActorID: "intruder"}
				spoof = &auditTwoSourceOwnerSwapStore{Store: options.Store, intruder: changed.Session.Actor}
				changed.Store = spoof
			case "no feed":
				changed.TurnFeed = nil
			}
			session, err := harness.OpenAgentSession(changed)
			if err != nil {
				t.Fatal(err)
			}
			want := harness.ErrConflict
			if mode == "no feed" {
				want = harness.ErrInvalidRequest
			}
			if subscription, err := session.Subscribe(t.Context(), "turn", 0); !errors.Is(err, want) || subscription != nil {
				if subscription != nil {
					subscription.Close()
				}
				t.Fatalf("unauthorized replay exposed: err=%v", err)
			}
			if spoof != nil && spoof.rootReads < 2 {
				t.Fatal("subscription did not recheck the returned feed's frozen owner")
			}
		})
	}
	if len(client.requests) != 1 {
		t.Fatal("read subscription dispatched a model")
	}
}

func TestSessionResumeCannotChangeOriginalRecoveryModeBeforeDispatch(t *testing.T) {
	for _, managed := range []bool{false, true} {
		t.Run(map[bool]string{false: "unmanaged-to-managed", true: "managed-to-unmanaged"}[managed], func(t *testing.T) {
			runs, store, client := memory.NewStore(), harness.NewMemoryStore(), &agentSessionModel{}
			options := agentSessionOptions(t, runs, store, client)
			dependencies := kernel.Dependencies{Store: runs}
			if managed {
				dependencies.ManagedRecoveryHost = store
			}
			var err error
			options.Runtime, err = kernel.New(dependencies)
			if err != nil {
				t.Fatal(err)
			}
			options.Context.ResolveWindow = func(context.Context, model.Request) (runtimecontext.ModelWindow, error) {
				return runtimecontext.ModelWindow{}, errResumeTurnInterrupted
			}
			original, err := harness.OpenAgentSession(options)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := original.Prompt(t.Context(), "turn", "pending work"); !errors.Is(err, errResumeTurnInterrupted) || len(client.requests) != 0 {
				t.Fatalf("preparation interruption: %v", err)
			}
			pending, err := original.Load(t.Context(), "turn")
			if err != nil {
				t.Fatal(err)
			}
			root, _ := harness.TopLevelInvocation(pending)
			before, err := options.Runtime.Load(t.Context(), root.ExecutionRefID)
			if err != nil || before.Run.Status != kernel.RunStatusRunning {
				t.Fatalf("original running Run missing: %v", err)
			}
			dependencies.ManagedRecoveryHost = nil
			if !managed {
				dependencies.ManagedRecoveryHost = store
			}
			options.Runtime, err = kernel.New(dependencies)
			if err != nil {
				t.Fatal(err)
			}
			options.Session.Transcript = nil
			reopened, err := harness.OpenAgentSession(options)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := reopened.Resume(t.Context(), "turn"); !errors.Is(err, harness.ErrInvalidRequest) || len(client.requests) != 0 {
				t.Fatalf("changed native recovery authority dispatched: %v", err)
			}
			after, err := options.Runtime.Load(t.Context(), root.ExecutionRefID)
			if err != nil || after.Run.Revision != before.Run.Revision {
				t.Fatal("rejected recovery mode changed the original Run")
			}
		})
	}
}
