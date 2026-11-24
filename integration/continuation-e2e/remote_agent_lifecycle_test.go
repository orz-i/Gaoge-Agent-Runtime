package continuatione2e

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	a2a "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-a2a"
	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	runtimecontext "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/context"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/runrelation"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

type remoteLifecycleModel struct {
	requests []model.Request
	generate func(model.Request) (model.Response, error)
}

func (m *remoteLifecycleModel) Generate(_ context.Context, request model.Request) (model.Response, error) {
	m.requests = append(m.requests, model.CloneRequest(request))
	if m.generate != nil {
		return m.generate(request)
	}
	return model.Response{Content: "answer"}, nil
}

func remoteLifecycleSessionOptions(t *testing.T, runStore kernel.Store, store harness.Store, client model.Client) harness.AgentSessionOptions {
	t.Helper()
	rt, err := kernel.New(kernel.Dependencies{Store: runStore})
	if err != nil {
		t.Fatal(err)
	}
	return harness.AgentSessionOptions{
		Runtime: rt, Store: store, Model: client,
		Session: harness.SessionOptions{
			HostThread: harness.HostRef{Kind: "conversation", ID: "tenant/tool-session"},
			Actor:      kernel.ActorRef{TenantID: "tenant", ActorID: "operator"},
			Thread:     kernel.ThreadRef{Kind: "conversation", ID: "tenant/tool-session"},
			Config:     harness.ConfigSnapshot{AgentSpec: harness.AgentSpec{Model: "fixture", Instructions: "answer accurately", MaxModelOutputTokens: 128}},
			Transcript: harness.TranscriptHistoryFunc(func(context.Context, harness.HostRef, harness.HostRef) ([]runtimecontext.Entry, error) {
				return nil, nil
			}),
		},
		Context: harness.AgentSessionContextOptions{ResolveWindow: func(context.Context, model.Request) (runtimecontext.ModelWindow, error) {
			return runtimecontext.ModelWindow{ContextTokens: 16384, ReservedOutputTokens: 128}, nil
		}},
	}
}

type independent151A2AClient struct {
	sends, gets, cancels int
	task                 a2a.TaskSnapshot
	cancelPending        bool
}

func (c *independent151A2AClient) SendMessage(context.Context, a2a.Discovery, a2a.SendRequest) (a2a.Interaction, error) {
	c.sends++
	return a2a.Interaction{Task: &c.task, Raw: c.task.Raw}, nil
}
func (c *independent151A2AClient) GetTask(context.Context, a2a.Discovery, string) (a2a.TaskSnapshot, error) {
	c.gets++
	return c.task, nil
}
func (c *independent151A2AClient) CancelTask(context.Context, a2a.Discovery, string) (a2a.TaskSnapshot, error) {
	c.cancels++
	if c.cancelPending {
		return c.task, nil
	}
	c.setCancelled()
	return c.task, nil
}
func (c *independent151A2AClient) setCancelled() {
	c.task.State = "TASK_STATE_CANCELED"
	c.task.Terminal = true
	c.task.Raw = json.RawMessage("{\"id\":\"remote-task\",\"contextId\":\"remote-context\",\"status\":{\"state\":\"TASK_STATE_CANCELED\"}}")
}
func TestPR151StandardNamedAgentCancelsOriginalA2A(t *testing.T) {
	m := &remoteLifecycleModel{generate: func(r model.Request) (model.Response, error) {
		for _, entry := range r.Messages {
			if entry.Role == model.RoleTool {
				return model.Response{Content: "done"}, nil
			}
		}
		return model.Response{ToolCalls: []tools.Call{{ID: "delegate-remote", ToolKey: harness.DelegationToolKey, Arguments: json.RawMessage("{\"memberID\":\"a2a:remote\",\"goal\":\"remote goal\"}")}}}, nil
	}}
	opts := remoteLifecycleSessionOptions(t, memory.NewStore(), harness.NewMemoryStore(), m)
	rel, err := runrelation.New(memory.NewRunRelationStore(), nil)
	if err != nil {
		t.Fatal(err)
	}
	opts.Relations = rel
	opts.Session.Config.DelegationPolicy = harness.DelegationPolicySnapshot{MaxDepth: 1}
	opts.NamedAgents = []harness.NamedAgent{{MemberID: "a2a:remote", MemberRevision: "r1", Remote: true}}
	client := &independent151A2AClient{task: a2a.TaskSnapshot{ID: "remote-task", ContextID: "remote-context", State: "TASK_STATE_WORKING", Raw: json.RawMessage("{\"id\":\"remote-task\",\"contextId\":\"remote-context\",\"status\":{\"state\":\"TASK_STATE_WORKING\"}}")}}
	plugin, err := a2a.NewPlugin(a2a.PluginDependencies{Runtime: opts.Runtime, AttestationKey: []byte(strings.Repeat("K", 32)), Bindings: a2a.BindingResolverFunc(func(_ context.Context, target, rev string) (a2a.Binding, error) {
		return a2a.Binding{TargetID: target, Revision: rev, Client: client, Discovery: a2a.Discovery{Descriptor: a2a.RemoteAgentDescriptor{Name: "audit-agent", PreferredURL: "https://agent.example/a2a", ProtocolVersion: a2a.ProtocolVersion, ProtocolBinding: "HTTP+JSON"}}}, nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	opts.RemoteChildren = plugin
	sess, err := harness.OpenAgentSession(opts)
	if err != nil {
		t.Fatal(err)
	}
	snap, err := sess.Prompt(t.Context(), "audit-turn", "Use remote")
	t.Logf("Prompt status=%s subtask_count=%d sends=%d gets=%d error=%v", snap.Turn.Status, len(snap.Subtasks), client.sends, client.gets, err)
	if client.sends != 1 {
		t.Fatal("fixture did not start a real A2A shadow")
	}
	var remoteID string
	for _, inv := range snap.Invocations {
		s, e := opts.Runtime.Load(t.Context(), inv.ExecutionRefID)
		if e == nil && s.Run.Kind == a2a.RunKind {
			remoteID = s.Run.ID
		}
	}
	if remoteID == "" {
		t.Fatal("missing real shadow")
	}
	canceled, cancelErr := sess.Cancel(t.Context(), "audit-turn", "stop")
	after, loadErr := opts.Runtime.Load(t.Context(), remoteID)
	t.Logf("Cancel turn=%s remote_status=%s sends=%d gets=%d cancels=%d err=%v loadErr=%v unverified=%t", canceled.Turn.Status, after.Run.Status, client.sends, client.gets, client.cancels, cancelErr, loadErr, errors.Is(cancelErr, harness.ErrHandoffCancellationUnverified))
	if cancelErr != nil || loadErr != nil || canceled.Turn.Status != harness.TurnCancelled || client.sends != 1 || client.cancels != 1 || after.Run.Status != kernel.RunStatusCancelled {
		t.Fatal("standard named Agent session cannot dispatch remote cancellation")
	}
}

func TestPR151PendingA2ACancellationReconcilesWithoutReplay(t *testing.T) {
	m := &remoteLifecycleModel{generate: func(r model.Request) (model.Response, error) {
		for _, entry := range r.Messages {
			if entry.Role == model.RoleTool {
				return model.Response{Content: "done"}, nil
			}
		}
		return model.Response{ToolCalls: []tools.Call{{ID: "delegate-remote", ToolKey: harness.DelegationToolKey, Arguments: json.RawMessage("{\"memberID\":\"a2a:remote\",\"goal\":\"remote goal\"}")}}}, nil
	}}
	opts := remoteLifecycleSessionOptions(t, memory.NewStore(), harness.NewMemoryStore(), m)
	rel, err := runrelation.New(memory.NewRunRelationStore(), nil)
	if err != nil {
		t.Fatal(err)
	}
	opts.Relations = rel
	opts.Session.Config.DelegationPolicy = harness.DelegationPolicySnapshot{MaxDepth: 1}
	opts.NamedAgents = []harness.NamedAgent{{MemberID: "a2a:remote", MemberRevision: "r1", Remote: true}}
	client := &independent151A2AClient{cancelPending: true, task: a2a.TaskSnapshot{ID: "remote-task", ContextID: "remote-context", State: "TASK_STATE_WORKING", Raw: json.RawMessage("{\"id\":\"remote-task\",\"contextId\":\"remote-context\",\"status\":{\"state\":\"TASK_STATE_WORKING\"}}")}}
	plugin, err := a2a.NewPlugin(a2a.PluginDependencies{Runtime: opts.Runtime, AttestationKey: []byte(strings.Repeat("K", 32)), Bindings: a2a.BindingResolverFunc(func(_ context.Context, target, rev string) (a2a.Binding, error) {
		return a2a.Binding{TargetID: target, Revision: rev, Client: client, Discovery: a2a.Discovery{Descriptor: a2a.RemoteAgentDescriptor{Name: "audit-agent", PreferredURL: "https://agent.example/a2a", ProtocolVersion: a2a.ProtocolVersion, ProtocolBinding: "HTTP+JSON"}}}, nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	opts.RemoteChildren = plugin
	sess, err := harness.OpenAgentSession(opts)
	if err != nil {
		t.Fatal(err)
	}
	snap, err := sess.Prompt(t.Context(), "audit-turn", "Use remote")
	t.Logf("Prompt status=%s subtask_count=%d sends=%d gets=%d error=%v", snap.Turn.Status, len(snap.Subtasks), client.sends, client.gets, err)
	if client.sends != 1 {
		t.Fatal("fixture did not start a real A2A shadow")
	}
	var remoteID string
	for _, inv := range snap.Invocations {
		s, e := opts.Runtime.Load(t.Context(), inv.ExecutionRefID)
		if e == nil && s.Run.Kind == a2a.RunKind {
			remoteID = s.Run.ID
		}
	}
	if remoteID == "" {
		t.Fatal("missing real shadow")
	}
	_, cancelErr := sess.Cancel(t.Context(), "audit-turn", "stop")
	if !errors.Is(cancelErr, harness.ErrCancellationOutcomeUnknown) || client.cancels != 1 {
		t.Fatalf("expected a single pending Cancel, calls=%d err=%v", client.cancels, cancelErr)
	}
	client.setCancelled()
	canceled, cancelErr := sess.Cancel(t.Context(), "audit-turn", "stop")
	after, loadErr := opts.Runtime.Load(t.Context(), remoteID)
	t.Logf("Cancel turn=%s remote_status=%s sends=%d gets=%d cancels=%d err=%v loadErr=%v unverified=%t", canceled.Turn.Status, after.Run.Status, client.sends, client.gets, client.cancels, cancelErr, loadErr, errors.Is(cancelErr, harness.ErrHandoffCancellationUnverified))
	if cancelErr != nil || loadErr != nil || canceled.Turn.Status != harness.TurnCancelled || client.sends != 1 || client.cancels != 1 || after.Run.Status != kernel.RunStatusCancelled {
		t.Fatal("standard named Agent session cannot dispatch remote cancellation")
	}
}

func TestPR151StandardNamedA2ARefresh(t *testing.T) {
	m := &remoteLifecycleModel{generate: func(r model.Request) (model.Response, error) {
		for _, entry := range r.Messages {
			if entry.Role == model.RoleTool {
				return model.Response{Content: "done"}, nil
			}
		}
		return model.Response{ToolCalls: []tools.Call{{ID: "delegate-remote", ToolKey: harness.DelegationToolKey, Arguments: json.RawMessage("{\"memberID\":\"a2a:remote\",\"goal\":\"remote goal\"}")}}}, nil
	}}
	opts := remoteLifecycleSessionOptions(t, memory.NewStore(), harness.NewMemoryStore(), m)
	rel, err := runrelation.New(memory.NewRunRelationStore(), nil)
	if err != nil {
		t.Fatal(err)
	}
	opts.Relations = rel
	opts.Session.Config.DelegationPolicy = harness.DelegationPolicySnapshot{MaxDepth: 1}
	opts.NamedAgents = []harness.NamedAgent{{MemberID: "a2a:remote", MemberRevision: "r1", Remote: true}}
	client := &independent151A2AClient{task: a2a.TaskSnapshot{ID: "remote-task", ContextID: "remote-context", State: "TASK_STATE_WORKING", Raw: json.RawMessage("{\"id\":\"remote-task\",\"contextId\":\"remote-context\",\"status\":{\"state\":\"TASK_STATE_WORKING\"}}")}}
	plugin, err := a2a.NewPlugin(a2a.PluginDependencies{Runtime: opts.Runtime, AttestationKey: []byte(strings.Repeat("K", 32)), Bindings: a2a.BindingResolverFunc(func(_ context.Context, target, rev string) (a2a.Binding, error) {
		return a2a.Binding{TargetID: target, Revision: rev, Client: client, Discovery: a2a.Discovery{Descriptor: a2a.RemoteAgentDescriptor{Name: "audit-agent", PreferredURL: "https://agent.example/a2a", ProtocolVersion: a2a.ProtocolVersion, ProtocolBinding: "HTTP+JSON"}}}, nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	opts.RemoteChildren = plugin
	sess, err := harness.OpenAgentSession(opts)
	if err != nil {
		t.Fatal(err)
	}
	snap, err := sess.Prompt(t.Context(), "audit-refresh", "Use remote")
	if err != nil || client.sends != 1 {
		t.Fatalf("fixture no remote effect status=%s err=%v sends=%d", snap.Turn.Status, err, client.sends)
	}
	client.task.State = "TASK_STATE_COMPLETED"
	client.task.Terminal = true
	client.task.Raw = json.RawMessage("{\"id\":\"remote-task\",\"contextId\":\"remote-context\",\"status\":{\"state\":\"TASK_STATE_COMPLETED\"}}")
	for range 3 {
		snap, err = sess.Refresh(t.Context(), "audit-refresh")
		t.Logf("Refresh turn=%s send=%d get=%d err=%v", snap.Turn.Status, client.sends, client.gets, err)
		if snap.Turn.Status == harness.TurnCompleted {
			break
		}
		snap, err = sess.Resume(t.Context(), "audit-refresh")
		t.Logf("Resume turn=%s send=%d get=%d err=%v", snap.Turn.Status, client.sends, client.gets, err)
	}
	if err != nil || snap.Turn.Status != harness.TurnCompleted || client.gets == 0 || client.sends != 1 || client.cancels != 0 {
		t.Fatal("standard session never refreshes remote Task")
	}
}
