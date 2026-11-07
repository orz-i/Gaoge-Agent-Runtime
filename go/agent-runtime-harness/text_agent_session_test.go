package harness_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	runtimecontext "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/context"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

type textSessionModel struct {
	requests []model.Request
}

func (client *textSessionModel) Generate(_ context.Context, request model.Request) (model.Response, error) {
	client.requests = append(client.requests, model.CloneRequest(request))
	users := 0
	for _, message := range request.Messages {
		if message.Role == model.RoleUser {
			users++
		}
	}
	return model.Response{Content: fmt.Sprintf("saw-%d-users", users)}, nil
}

func textSessionOptions(rt *kernel.Runtime, store harness.Store, client model.Client, transcript harness.TranscriptSource) harness.TextAgentSessionOptions {
	const thread = "tenant-a/chat-1"
	return harness.TextAgentSessionOptions{
		Runtime: rt, Store: store, Model: client,
		Session: harness.SessionOptions{
			HostThread: harness.HostRef{Kind: "conversation", ID: thread},
			Actor:      kernel.ActorRef{TenantID: "tenant-a", ActorID: "user-a"},
			Thread:     kernel.ThreadRef{Kind: "conversation", ID: thread},
			Config:     harness.ConfigSnapshot{Model: "local"}, Transcript: transcript,
		},
	}
}

func TestTextAgentSessionComposesOneLoopWithImmutableHistoryAndRecovery(t *testing.T) {
	kernelStore, harnessStore := memory.NewStore(), harness.NewMemoryStore()
	var history []runtimecontext.Entry
	var historyReads int
	transcript := harness.TranscriptHistoryFunc(func(_ context.Context, thread, turn harness.HostRef) ([]runtimecontext.Entry, error) {
		historyReads++
		if thread.ID != "tenant-a/chat-1" || turn.Kind != "conversation_turn" {
			t.Fatalf("unexpected host IDs: thread=%+v turn=%+v", thread, turn)
		}
		return runtimecontext.CloneEntries(history), nil
	})
	client := &textSessionModel{}
	newSession := func(own bool) *harness.SessionClient {
		rt, err := kernel.New(kernel.Dependencies{Store: kernelStore})
		if err != nil {
			t.Fatal(err)
		}
		options := textSessionOptions(rt, harnessStore, client, transcript)
		// Reopening with the canonical default Policy must not be mistaken
		// for an unsupported managed/compacting Context configuration.
		options.Session.Config.ContextPolicy = runtimecontext.NormalizePolicy(runtimecontext.Policy{})
		if own {
			options.RunExecution = &agent.RunExecution{OwnerID: "worker-a", LeaseDuration: 5 * time.Minute}
		}
		session, err := harness.OpenTextAgentSession(options)
		if err != nil {
			t.Fatal(err)
		}
		return session
	}
	first := newSession(true)
	one, err := first.Prompt(t.Context(), "turn-1", "hello")
	if err != nil || one.Turn.Status != harness.TurnCompleted || len(client.requests) != 1 {
		t.Fatalf("first turn: status=%s err=%v requests=%d", one.Turn.Status, err, len(client.requests))
	}
	if model.TextValue(client.requests[0].Messages[0].Content) != "hello" {
		t.Fatalf("first prompt not presented to model: %+v", client.requests[0].Messages)
	}
	if repeated, err := first.Prompt(t.Context(), "turn-1", "hello"); err != nil || repeated.Turn.ID != one.Turn.ID || len(client.requests) != 1 {
		t.Fatalf("idempotent immediate replay: %+v err=%v calls=%d", repeated.Turn, err, len(client.requests))
	}
	// The Host commits the completed answer to its own transcript; the SDK
	// neither saves it as a second transcript nor invents a message cursor.
	history = []runtimecontext.Entry{
		{ID: "turn-1/user", SourceID: "turn-1/user", TurnID: "turn-1", Message: model.Message{Role: model.RoleUser, Content: model.TextParts("hello")}},
		{ID: "turn-1/assistant", SourceID: "turn-1/assistant", TurnID: "turn-1", Message: model.Message{Role: model.RoleAssistant, Content: model.TextParts("saw-1-users")}},
	}
	// Construct a new Agent/Harness over the same Stores to prove reopening
	// does not depend on a process-local message history or duplicate loop.
	second := newSession(true)
	two, err := second.Prompt(t.Context(), "turn-2", "how many users?")
	if err != nil || two.Turn.Status != harness.TurnCompleted || len(client.requests) != 2 {
		t.Fatalf("second turn: status=%s err=%v requests=%d", two.Turn.Status, err, len(client.requests))
	}
	messages := client.requests[1].Messages
	if len(messages) != 3 || model.TextValue(messages[0].Content) != "hello" ||
		model.TextValue(messages[1].Content) != "saw-1-users" ||
		model.TextValue(messages[2].Content) != "how many users?" {
		t.Fatalf("context was not preserved across new composition: %+v", messages)
	}
	original, err := second.Load(t.Context(), "turn-1")
	if err != nil || original.Turn.ID != one.Turn.ID || original.Output == nil ||
		string(original.Output.Content) != string(one.Output.Content) {
		t.Fatalf("historical load differs: %+v err=%v", original.Turn, err)
	}
	terminal, err := second.Resume(t.Context(), "turn-2")
	if err != nil || terminal.Turn.Status != harness.TurnCompleted || len(client.requests) != 2 {
		t.Fatalf("terminal resume repeated the model call: %+v err=%v", terminal.Turn, err)
	}
	if _, err = second.Prompt(t.Context(), "turn-1", "hello"); !errors.Is(err, harness.ErrInvalidRequest) {
		t.Fatalf("history with duplicate current SourceID was accepted: %v", err)
	}
	// A changed ancestry without an ID collision instead reaches the frozen
	// Turn-admission hash and must still reject without dispatching a model.
	history = append(history, runtimecontext.Entry{
		ID: "unrelated/assistant", SourceID: "unrelated/assistant", TurnID: "unrelated",
		Message: model.Message{Role: model.RoleAssistant, Content: model.TextParts("later")},
	})
	if _, err = second.Prompt(t.Context(), "turn-2", "how many users?"); !errors.Is(err, harness.ErrConflict) {
		t.Fatalf("changed original Turn history was accepted: %v", err)
	}
	if len(client.requests) != 2 || historyReads != 5 {
		t.Fatalf("unexpected model dispatch/transcript read: model=%d history=%d", len(client.requests), historyReads)
	}
	// A reopened read-only session needs no Host transcript, but its frozen
	// Actor cannot be changed to read another tenant's history.
	readOpts := textSessionOptions(mustTextRuntime(t, kernelStore), harnessStore, client, nil)
	reader, err := harness.OpenTextAgentSession(readOpts)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = reader.Load(t.Context(), "turn-1"); err != nil {
		t.Fatalf("reopened read without transcript: %v", err)
	}
	readOpts.Session.Actor.ActorID = "foreign-user"
	foreign, err := harness.OpenTextAgentSession(readOpts)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = foreign.Load(t.Context(), "turn-1"); !errors.Is(err, harness.ErrConflict) {
		t.Fatalf("foreign owner read: %v", err)
	}
	// Authorization must run before exposing the private Host transcript to
	// a foreign caller and before any physical model effect.
	foreignOptions := readOpts
	foreignOptions.Session.Transcript = harness.TranscriptHistoryFunc(func(context.Context, harness.HostRef, harness.HostRef) ([]runtimecontext.Entry, error) {
		t.Fatal("foreign Prompt read private transcript")
		return nil, nil
	})
	foreign, err = harness.OpenTextAgentSession(foreignOptions)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = foreign.Prompt(t.Context(), "new-turn", "foreign"); !errors.Is(err, harness.ErrConflict) || len(client.requests) != 2 {
		t.Fatalf("foreign Prompt reached transcript or model: err=%v calls=%d", err, len(client.requests))
	}
}

func mustTextRuntime(t *testing.T, store kernel.Store) *kernel.Runtime {
	t.Helper()
	rt, err := kernel.New(kernel.Dependencies{Store: store})
	if err != nil {
		t.Fatal(err)
	}
	return rt
}

func TestTextAgentSessionRejectsUncomposedFeatures(t *testing.T) {
	store := harness.NewMemoryStore()
	client := &textSessionModel{}
	base := textSessionOptions(mustTextRuntime(t, memory.NewStore()), store, client, nil)
	cases := []struct {
		name   string
		mutate func(*harness.TextAgentSessionOptions)
	}{
		{"missing runtime", func(o *harness.TextAgentSessionOptions) { o.Runtime = nil }},
		{"missing store", func(o *harness.TextAgentSessionOptions) { o.Store = nil }},
		{"missing model", func(o *harness.TextAgentSessionOptions) { o.Model = nil }},
		{"tool without registry", func(o *harness.TextAgentSessionOptions) { o.Session.Config.ToolKeys = []string{"some-tool"} }},
		{"policy without registry", func(o *harness.TextAgentSessionOptions) {
			o.Session.Config.ToolPolicies = []harness.ToolPolicySnapshot{{Key: "some-tool"}}
		}},
		{"command without executor", func(o *harness.TextAgentSessionOptions) {
			o.Session.Config.Commands = []harness.CommandDescriptor{{ID: "command"}}
		}},
		{"role without composer", func(o *harness.TextAgentSessionOptions) {
			o.Session.Config.Roles = []harness.RoleSnapshot{{ID: "role"}}
		}},
		{"skills without loader", func(o *harness.TextAgentSessionOptions) {
			o.Session.Config.Skills = []harness.SkillSnapshot{{ID: "skill"}}
		}},
		{"memory policy without manager", func(o *harness.TextAgentSessionOptions) {
			o.Session.Config.MemoryPolicy = "custom"
		}},
		{"bounded context without model window", func(o *harness.TextAgentSessionOptions) {
			o.Session.Config.ContextPolicy.MaxInputTokens = 512
		}},
		{"shared budget without coordinator", func(o *harness.TextAgentSessionOptions) {
			o.Session.Config.SharedBudget = &budget.Limits{}
		}},
		{"delegation without runner", func(o *harness.TextAgentSessionOptions) { o.Session.Config.DelegationPolicy.MaxDepth = 1 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opts := base
			opts.Session = base.Session
			tc.mutate(&opts)
			if _, err := harness.OpenTextAgentSession(opts); !errors.Is(err, harness.ErrInvalidRequest) {
				t.Fatalf("expected fail-closed, got %v", err)
			}
		})
	}
}
