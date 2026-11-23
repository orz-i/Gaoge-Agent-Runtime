package harness_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/handoff"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/runrelation"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

type fixtureRemote struct {
	rt     *kernel.Runtime
	starts int
}

func (r *fixtureRemote) StartRun(ctx context.Context, req agent.StartRequest) (kernel.Snapshot, error) {
	r.starts++
	if !agent.SupportsRemoteGoalOnly(req) {
		return kernel.Snapshot{}, errors.New("native agent capability leaked into remote")
	}
	s, err := r.rt.Create(ctx, kernel.CreateRequest{ID: req.ID, Kind: handoff.RemoteAgentRunKind,
		Actor: req.Actor, Thread: req.Thread, RequestID: req.RequestID, Goal: req.Goal, State: json.RawMessage(`{"transport":"acp"}`)})
	if err != nil {
		return s, err
	}
	result := kernel.Result{ContentType: "application/json", Content: json.RawMessage(`{"text":"remote evidence"}`)}
	return r.rt.Apply(ctx, req.ID, s.Run.Revision, kernel.Mutation{Status: kernel.RunStatusCompleted, State: s.State, Result: &result})
}
func (r *fixtureRemote) LoadRun(ctx context.Context, id string) (kernel.Snapshot, error) {
	return r.rt.Load(ctx, id)
}
func (r *fixtureRemote) ObservesFrozenRemoteShadow(rt *kernel.Runtime, d handoff.Delegation, s kernel.Snapshot) bool {
	return rt == r.rt && d.Remote && d.MemberID == "acp:research" && d.MemberRevision == "rev-1" &&
		s.Run.Kind == handoff.RemoteAgentRunKind && s.Run.ID == d.ChildRunID &&
		s.Run.Goal == d.Goal
}
func TestNamedAgentMixedLocalAndRemoteHostSelection(t *testing.T) {
	rtStore := memory.NewStore()
	hostStore := harness.NewMemoryStore()
	modelCalls := 0
	m := &agentSessionModel{generate: func(request model.Request) (model.Response, error) {
		modelCalls++
		for _, entry := range request.Messages {
			if entry.Role == model.RoleTool {
				return model.Response{Content: "native root consumed remote evidence"}, nil
			}
		}
		return model.Response{ToolCalls: []tools.Call{{ID: "delegate-remote", ToolKey: harness.DelegationToolKey,
			Arguments: json.RawMessage(`{"memberID":"acp:research","goal":"check one evidence"}`)}}}, nil
	}}
	opts := agentSessionOptions(t, rtStore, hostStore, m)
	rel, err := runrelation.New(memory.NewRunRelationStore(), nil)
	if err != nil {
		t.Fatal(err)
	}
	opts.Relations = rel
	opts.Session.Config.DelegationPolicy = harness.DelegationPolicySnapshot{MaxDepth: 1}
	opts.NamedAgents = []harness.NamedAgent{
		{MemberID: "acp:research", MemberRevision: "rev-1", Remote: true},
		{MemberID: "local-worker", MemberRevision: "rev-1", AgentSpec: harness.AgentSpec{Model: "worker"}},
	}
	remote := &fixtureRemote{rt: opts.Runtime}
	opts.RemoteChildren = handoff.ChildRunnerResolverFunc(func(_ context.Context, d handoff.Delegation) (handoff.ChildRunner, error) {
		if d.MemberID != "acp:research" || d.MemberRevision != "rev-1" || !d.Remote {
			return nil, errors.New("wrong Host-frozen remote binding")
		}
		return remote, nil
	})
	session, err := harness.OpenAgentSession(opts)
	if err != nil {
		t.Fatal(err)
	}
	snap, err := session.Prompt(t.Context(), "mixed-delegate", "Use researcher")
	if err != nil || snap.Turn.Status != harness.TurnCompleted || remote.starts != 1 || modelCalls != 2 {
		t.Fatalf("mixed local+remote Host roster did not execute: %s %v remote=%d model=%d",
			snap.Turn.Status, err, remote.starts, modelCalls)
	}
	// An original completed external effect is not repeated on reopen.
	opts.Session.Transcript = nil
	reopened, err := harness.OpenAgentSession(opts)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = reopened.Resume(t.Context(), "mixed-delegate"); err != nil || remote.starts != 1 {
		t.Fatalf("reopening original remote execution re-sent: %v count=%d", err, remote.starts)
	}
}

func TestNamedAgentRemoteNativeCapabilitiesRejectBeforeAnyEffect(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*harness.NamedAgent)
	}{
		{"model", func(a *harness.NamedAgent) { a.Model = "gpt-other" }},
		{"instructions", func(a *harness.NamedAgent) { a.Instructions = "execute this" }},
		{"tools", func(a *harness.NamedAgent) { a.ToolKeys = []string{"shell"} }},
		{"hosted", func(a *harness.NamedAgent) { a.HostedToolGrants = []agent.HostedToolGrant{{Key: "provider.search"}} }},
		{"output", func(a *harness.NamedAgent) { a.MaxModelOutputTokens = 100 }},
		{"limits", func(a *harness.NamedAgent) { a.Limits.MaxLLMCalls = 3 }},
		{"skills", func(a *harness.NamedAgent) {
			a.Skills = []harness.SkillSnapshot{{ID: "s", Revision: 1, Title: "s", Markdown: "private"}}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opts := agentSessionOptions(t, memory.NewStore(), harness.NewMemoryStore(), &agentSessionModel{})
			rel, err := runrelation.New(memory.NewRunRelationStore(), nil)
			if err != nil {
				t.Fatal(err)
			}
			opts.Relations = rel
			opts.Session.Config.DelegationPolicy = harness.DelegationPolicySnapshot{MaxDepth: 1}
			opts.NamedAgents = []harness.NamedAgent{{MemberID: "acp:r", MemberRevision: "1", Remote: true}}
			opts.RemoteChildren = handoff.ChildRunnerResolverFunc(func(context.Context, handoff.Delegation) (handoff.ChildRunner, error) {
				t.Error("resolver called prior to Host authorization")
				return nil, errors.New("unexpected resolver")
			})
			tc.mutate(&opts.NamedAgents[0])
			if _, err = harness.OpenAgentSession(opts); !errors.Is(err, harness.ErrInvalidRequest) {
				t.Fatalf("remote member accepted unproved capability: %v", err)
			}
		})
	}
}
