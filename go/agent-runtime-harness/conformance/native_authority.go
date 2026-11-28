package conformance

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	runtimeconformance "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/conformance"
	runtimecontext "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/context"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/runrelation"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

type NativeAuthorityFixture struct {
	Kernel      kernel.Store
	Host        harness.Store
	Relations   runrelation.Store
	SharedTable bool
	Reopen      func() (kernel.Store, harness.Store, runrelation.Store)
}
type nativeAuthorityKernelPorts interface {
	noEntryKernelPorts
	budget.LedgerStore
}

func NativeAuthorityCutover(t *testing.T, factory func(*testing.T) NativeAuthorityFixture) {
	t.Run("retry", func(t *testing.T) { nativeAuthorityRetrySelectionACK(t, factory) })
	t.Run("negative-projection", func(t *testing.T) { nativeNegativeProjection(t, factory) })
	t.Run("child", func(t *testing.T) { nativeAuthorityNamedChildTwoTools(t, factory) })
	t.Run("child-enrollment", func(t *testing.T) { nativeChildEnrollment(t, factory) })
}

type negativeProjectionCut struct {
	harness.Store
	budget.LedgerStore
	phase, runID string
	armed        bool
}

func (s *negativeProjectionCut) UpdateInvocation(ctx context.Context, inv harness.Invocation, revision uint64) (harness.Invocation, error) {
	if inv.Status == harness.InvocationCancelled {
		s.runID = inv.ExecutionRefID
		if s.armed && s.phase == "invocation-before" {
			s.armed = false
			return harness.Invocation{}, errRootRetryCancelAckLost
		}
	}
	out, err := s.Store.UpdateInvocation(ctx, inv, revision)
	if err == nil && inv.Status == harness.InvocationCancelled && s.armed && s.phase == "invocation-after" {
		s.armed = false
		return harness.Invocation{}, errRootRetryCancelAckLost
	}
	return out, err
}

func (s *negativeProjectionCut) UpdateTurn(ctx context.Context, turn harness.Turn, revision uint64) (harness.Turn, error) {
	if turn.Status == harness.TurnCancelled && s.armed && s.phase == "turn-before" {
		s.armed = false
		return harness.Turn{}, errRootRetryCancelAckLost
	}
	out, err := s.Store.UpdateTurn(ctx, turn, revision)
	if err == nil && turn.Status == harness.TurnCancelled && s.armed && s.phase == "turn-after" {
		s.armed = false
		return harness.Turn{}, errRootRetryCancelAckLost
	}
	return out, err
}

func nativeNegativeProjection(t *testing.T, factory func(*testing.T) NativeAuthorityFixture) {
	for _, phase := range []string{"invocation-before", "invocation-after", "turn-before", "turn-after"} {
		t.Run(phase, func(t *testing.T) {
			ctx := t.Context()
			f := factory(t)
			runs := f.Kernel.(nativeAuthorityKernelPorts)
			host := &negativeProjectionCut{Store: f.Host, LedgerStore: f.Host.(budget.LedgerStore), phase: phase}
			client := &nativeAuthorityModel{generate: func(model.Request) (model.Response, error) {
				t.Fatal("negative creation decision dispatched a Model")
				return model.Response{}, nil
			}}
			options := nativeAuthorityOptions(t, &nativeFirstCreateCut{nativeAuthorityKernelPorts: runs, cut: true}, host, client)
			session, err := harness.OpenAgentSession(options)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = session.Prompt(ctx, "projection", "original"); !errors.Is(err, errFirstCreateCut) {
				t.Fatalf("Create cut: %v", err)
			}
			host.armed = true
			if _, err = session.Cancel(ctx, "projection", "original stop"); !errors.Is(err, errRootRetryCancelAckLost) {
				t.Fatalf("projection cut %s: %v", phase, err)
			}
			decision, err := runs.LoadCreationDecision(ctx, host.runID)
			if err != nil || decision.Status != kernel.RunStatusCancelled || decision.Reason != "original stop" {
				t.Fatalf("projection failure changed Runtime decision: %+v %v", decision, err)
			}
			if f.Reopen != nil {
				k, h, _ := f.Reopen()
				runs = k.(nativeAuthorityKernelPorts)
				host.Store, host.LedgerStore = h, h.(budget.LedgerStore)
			}
			options.Runtime, err = kernel.New(kernel.Dependencies{Store: runs})
			if err != nil {
				t.Fatal(err)
			}
			options.Session.Transcript = nil
			session, err = harness.OpenAgentSession(options)
			if err != nil {
				t.Fatal(err)
			}
			out, err := session.Cancel(ctx, "projection", "must not replace original reason")
			if err != nil || out.Turn.Status != harness.TurnCancelled || out.Turn.ErrorDetail != decision.Reason {
				t.Fatalf("projection recovery: %+v %v", out.Turn, err)
			}
			root, ok := harness.TopLevelInvocation(out)
			if !ok || root.ExecutionRefID != decision.RunID || root.Attempt != 1 || len(client.requests) != 0 {
				t.Fatal("projection recovery replaced original execution selection")
			}
			if _, err = runs.Load(ctx, root.ExecutionRefID); !errors.Is(err, kernel.ErrNotFound) {
				t.Fatalf("negative identity manufactured a Run: %v", err)
			}
			for _, item := range out.Items {
				if item.ParentItemID == harness.ExecutionDispatchParentItemID || item.ParentItemID == harness.CancellationFenceParentItemID {
					t.Fatal("native projection elected Host execution/cancellation authority")
				}
			}
		})
	}
}

func nativeAuthorityRetrySelectionACK(t *testing.T, factory func(*testing.T) NativeAuthorityFixture) {
	for _, mode := range []string{"replay", "cancel-and-retry", "protective-reopen", "uncreated-first"} {
		t.Run(mode, func(t *testing.T) {
			ctx := t.Context()
			f := factory(t)
			runs, raw := f.Kernel.(nativeAuthorityKernelPorts), f.Host
			host := &nativeRetryLostACKStore{Store: raw, LedgerStore: raw.(budget.LedgerStore), failOnce: true}
			writes := 0
			client := &nativeAuthorityModel{generate: func(r model.Request) (model.Response, error) {
				return model.Response{ToolCalls: []tools.Call{{ID: "call", ToolKey: "write", Arguments: json.RawMessage("{}")}}}, nil
			}}
			var runStore kernel.Store = runs
			if mode == "uncreated-first" {
				runStore = &nativeFirstCreateCut{nativeAuthorityKernelPorts: runs, cut: true}
			}
			options := nativeAuthorityOptions(t, runStore, host, client)
			options.Session.Config.SharedBudget = &budget.Limits{MaxLLMCalls: 4, MaxToolCalls: 2}
			options.LocalTools = []harness.SessionTool{{Registration: nativeAuthorityTool("write", &writes), Policy: harness.ToolPolicySnapshot{DefinitionVersion: "v1", SideEffectLevel: "write", ApprovalMode: "always", ApprovalCapability: "per_call"}}}
			session, e := harness.OpenAgentSession(options)
			if e != nil {
				t.Fatal(e)
			}
			first, e := session.Prompt(ctx, "retry", "original")
			wantCalls := 2
			if mode == "uncreated-first" {
				if !errors.Is(e, errFirstCreateCut) || len(client.requests) != 0 {
					t.Fatalf("first Create cut: %v calls=%d", e, len(client.requests))
				}
				wantCalls = 1
			} else if e != nil || first.Turn.Status != harness.TurnWaitingInput {
				t.Fatalf("first wait: %s %v", first.Turn.Status, e)
			}
			cancelSession := session
			if mode == "protective-reopen" {
				protective := options
				protective.Session.Config.SharedBudget = nil
				cancelSession, e = harness.OpenAgentSession(protective)
				if e != nil {
					t.Fatal(e)
				}
			}
			stop, e := cancelSession.Cancel(ctx, "retry", "stop")
			if e != nil || stop.Turn.Status != harness.TurnCancelled {
				t.Fatalf("native cancel: %s %v", stop.Turn.Status, e)
			}
			root, ok := harness.TopLevelInvocation(stop)
			if !ok {
				t.Fatal("missing root")
			}
			first.Turn.ID = stop.Turn.ID
			before, e := runs.LoadBudget(ctx, first.Turn.ID)
			if e != nil || !before.RunCancelled(root.ExecutionRefID) {
				t.Fatalf("native budget did not close: %v %+v", e, before)
			}
			if mode == "uncreated-first" {
				d, e := runs.LoadCreationDecision(ctx, root.ExecutionRefID)
				if e != nil || d.Status != kernel.RunStatusCancelled || len(before.Runs) != 0 || before.View("").Usage.LLMCalls != 0 {
					t.Fatalf("negative first identity fabricated execution: %v %+v %+v", e, d, before)
				}
				if _, e = runs.Load(ctx, root.ExecutionRefID); !errors.Is(e, kernel.ErrNotFound) {
					t.Fatalf("negative first identity created a Run: %v", e)
				}
			}
			if _, e = session.Retry(ctx, "retry"); !errors.Is(e, errRootRetryCommittedAckLost) {
				t.Fatalf("Host selection ACK cut: %v", e)
			}
			selected, e := raw.GetInvocation(ctx, root.ID)
			if e != nil || selected.Status != harness.InvocationAccepted || selected.Attempt != 2 {
				t.Fatalf("original selection missing: %v %+v", e, selected)
			}
			unchanged, e := runs.LoadBudget(ctx, first.Turn.ID)
			if e != nil || !reflect.DeepEqual(before, unchanged) {
				t.Fatalf("Host rotation mutated native ledger: %v", e)
			}
			if _, e = runs.Load(ctx, selected.ExecutionRefID); !errors.Is(e, kernel.ErrNotFound) {
				t.Fatal("unadmitted Run existed")
			}
			options.Session.Transcript = nil
			if f.Reopen != nil {
				k, h, _ := f.Reopen()
				runs, raw = k.(nativeAuthorityKernelPorts), h
				host.Store, host.LedgerStore = raw, raw.(budget.LedgerStore)
				options.Runtime, e = kernel.New(kernel.Dependencies{Store: runs})
				if e != nil {
					t.Fatal(e)
				}
			}
			session, e = harness.OpenAgentSession(options)
			if e != nil {
				t.Fatal(e)
			}
			if mode == "cancel-and-retry" {
				host.cancelAckOnce = true
				if _, e = session.Cancel(ctx, "retry", "cancel selected attempt"); !errors.Is(e, errRootRetryCancelAckLost) {
					t.Fatalf("cancel ACK loss: %v", e)
				}
				stop, e = session.Cancel(ctx, "retry", "recover same cancel")
				if e != nil || stop.Turn.Status != harness.TurnCancelled {
					t.Fatalf("unstarted cancel replay: %s %v", stop.Turn.Status, e)
				}
				l, e := runs.LoadBudget(ctx, first.Turn.ID)
				if e != nil || !l.RunCancelled(selected.ExecutionRefID) || len(client.requests) != 1 {
					t.Fatalf("unstarted cancel advanced effects: %v %+v", e, l)
				}
			}
			again, e := session.Retry(ctx, "retry")
			if e != nil || again.Turn.Status != harness.TurnWaitingInput {
				t.Fatalf("selected attempt replay: %s %v", again.Turn.Status, e)
			}
			next, ok := harness.TopLevelInvocation(again)
			want := 2
			if mode == "cancel-and-retry" {
				want = 3
			}
			l, e := runs.LoadBudget(ctx, first.Turn.ID)
			if !ok || next.Attempt != want || e != nil || !l.RootRetries[next.ExecutionRefID].Active || l.RunCancelled(next.ExecutionRefID) || l.View("").Usage.LLMCalls != wantCalls || writes != 0 || len(client.requests) != wantCalls {
				t.Fatalf("unexpected retry identity or effects: %+v ledger=%+v err=%v calls=%d", next, l, e, len(client.requests))
			}
			observed, e := raw.(budget.LedgerStore).LoadBudget(ctx, first.Turn.ID)
			if f.SharedTable {
				if e != nil || !reflect.DeepEqual(l, observed) {
					t.Fatalf("different SQL accounting row: %v", e)
				}
			} else if !errors.Is(e, budget.ErrLedgerNotFound) {
				t.Fatalf("Host still has execution ledger: %v", e)
			}
		})
	}
}

func nativeAuthorityNamedChildTwoTools(t *testing.T, factory func(*testing.T) NativeAuthorityFixture) {
	for _, mode := range []string{"cancel", "blocked-then-approve", "refresh-second-approval", "unmetered-approve"} {
		t.Run(mode, func(t *testing.T) {
			ctx := t.Context()
			f := factory(t)
			runs, host := f.Kernel.(nativeAuthorityKernelPorts), f.Host
			calls, reads, writes := 0, 0, 0
			client := &nativeAuthorityModel{generate: func(r model.Request) (model.Response, error) {
				if r.Model == "child-model" {
					calls++
					if calls == 1 {
						return model.Response{ToolCalls: []tools.Call{{ID: "first", ToolKey: "child.read", Arguments: json.RawMessage("{}")}, {ID: "second", ToolKey: "child.write", Arguments: json.RawMessage("{}")}}}, nil
					}
					return model.Response{Content: "child completed"}, nil
				}
				for _, m := range r.Messages {
					if m.Role == model.RoleTool {
						return model.Response{Content: "done"}, nil
					}
				}
				return model.Response{ToolCalls: []tools.Call{{ID: "delegate", ToolKey: harness.DelegationToolKey, Arguments: json.RawMessage(`{"memberID":"child","goal":"finish"}`)}}}, nil
			}}
			options := nativeAuthorityOptions(t, runs, host, client)
			if mode != "unmetered-approve" {
				options.Session.Config.SharedBudget = &budget.Limits{MaxLLMCalls: 4, MaxToolCalls: 4, MaxChildRuns: 1, MaxConcurrentRuns: 2}
			}
			options.Session.Config.DelegationPolicy = harness.DelegationPolicySnapshot{MaxDepth: 1}
			var e error
			options.Relations, e = runrelation.New(f.Relations, nil)
			if e != nil {
				t.Fatal(e)
			}
			readMode := "never"
			if mode == "refresh-second-approval" {
				readMode = "always"
			}
			readPolicy := harness.ToolPolicySnapshot{DefinitionVersion: "v1", SideEffectLevel: "read", ApprovalMode: readMode}
			if readMode == "always" {
				readPolicy.ApprovalCapability = "per_call"
			}
			options.NamedAgents = []harness.NamedAgent{{MemberID: "child", MemberRevision: "v1", AgentSpec: harness.AgentSpec{Model: "child-model"}, Tools: []harness.SessionTool{
				{Registration: nativeAuthorityTool("child.read", &reads), Policy: readPolicy},
				{Registration: nativeAuthorityTool("child.write", &writes), Policy: harness.ToolPolicySnapshot{DefinitionVersion: "v1", SideEffectLevel: "write", ApprovalMode: "always", ApprovalCapability: "per_call"}},
			}}}
			session, e := harness.OpenAgentSession(options)
			if e != nil {
				t.Fatal(e)
			}
			pending, e := session.Prompt(ctx, "two", "original")
			if e != nil || len(pending.Subtasks) != 1 || pending.Subtasks[0].Approval == nil {
				t.Fatalf("missing approval: %v %+v", e, pending.Subtasks)
			}
			task := pending.Subtasks[0]
			// Bypassing the Runner must not recreate the retired Host election.
			childInvocation, e := host.GetInvocationByExecutionRefID(ctx, task.RunID)
			if e != nil {
				t.Fatal(e)
			}
			for _, phase := range []string{"handoff-start", "resolve-subtask-approval"} {
				payload, _ := json.Marshal(map[string]string{"phase": phase, "physicalRunID": task.RunID})
				_, _, e = host.AppendItem(ctx, harness.Item{ID: "forbidden-native-child-" + phase, TurnID: pending.Turn.ID, InvocationID: childInvocation.ID, RunID: task.RunID, Kind: harness.ItemDiagnostic, Status: harness.ItemStarted, ParentItemID: harness.ExecutionDispatchParentItemID, Payload: payload, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()})
				if !errors.Is(e, harness.ErrConflict) {
					t.Fatalf("native child admitted Host %s grant: %v", phase, e)
				}
			}
			if mode == "refresh-second-approval" {
				cut := &nativeChildReturnCut{nativeAuthorityKernelPorts: runs}
				options.Runtime, e = kernel.New(kernel.Dependencies{Store: cut})
				if e != nil {
					t.Fatal(e)
				}
				session, e = harness.OpenAgentSession(options)
				if e != nil {
					t.Fatal(e)
				}
				interruptNativeChildReturn(t, func() {
					_, _ = session.ResolveSubtaskApproval(ctx, "two", task.ID, task.Approval.CheckpointID, harness.ResolveApprovalRequest{Decision: harness.ApprovalApprove})
				})
				if reads != 1 || writes != 0 {
					t.Fatalf("first original approval cut: reads=%d writes=%d", reads, writes)
				}
				if f.Reopen != nil {
					k, h, rel := f.Reopen()
					runs, host = k.(nativeAuthorityKernelPorts), h
					options.Store = host
					options.Relations, e = runrelation.New(rel, nil)
					if e != nil {
						t.Fatal(e)
					}
				}
				options.Runtime, e = kernel.New(kernel.Dependencies{Store: runs})
				if e != nil {
					t.Fatal(e)
				}
				session, e = harness.OpenAgentSession(options)
				if e != nil {
					t.Fatal(e)
				}
				pending, e = session.Refresh(ctx, "two")
				if e != nil || len(pending.Subtasks) != 1 || pending.Subtasks[0].Approval == nil || pending.Subtasks[0].Approval.CheckpointID == task.Approval.CheckpointID {
					t.Fatalf("known nonterminal approval did not refresh: %v %+v", e, pending.Subtasks)
				}
				task = pending.Subtasks[0]
			}
			if reads != 1 || writes != 0 {
				t.Fatalf("incorrect original calls %d/%d", reads, writes)
			}
			child, e := runs.Load(ctx, task.RunID)
			if e != nil || child.Run.Status != kernel.RunStatusWaitingInput {
				t.Fatalf("second original approval: %v %+v", e, child.Run)
			}
			if mode == "blocked-then-approve" {
				// Synthetic held original reservation exercises the native no-effect
				// cancellation boundary without injecting a second actual Tool call.
				c := budget.Coordinator{Store: runs}
				// A held obligation consumes one additional slot.
				q := budget.Reservation{ID: "tool:" + task.RunID + ":held", RunID: task.RunID, RequestHash: "held", Requested: budget.Usage{ToolCalls: 1}}
				if _, e = c.Reserve(ctx, pending.Turn.ID, q, true); e != nil {
					t.Fatal(e)
				}
				if e = runtimeconformance.DispatchToolFixture(ctx, c, pending.Turn.ID, q); e != nil {
					t.Fatal(e)
				}
				before, e := runs.LoadBudget(ctx, pending.Turn.ID)
				if e != nil {
					t.Fatal(e)
				}
				if _, e = session.CancelSubtask(ctx, "two", task.ID, "blocked"); e == nil {
					t.Fatal("cancelled unknown physical obligation")
				}
				after, e := runs.LoadBudget(ctx, pending.Turn.ID)
				if e != nil || !reflect.DeepEqual(before, after) {
					t.Fatalf("refused cancel mutated accounting: %v", e)
				}
				observed, e := runs.Load(ctx, task.RunID)
				if e != nil || !reflect.DeepEqual(child, observed) {
					t.Fatalf("refused cancel mutated Run: %v", e)
				}
				items, e := host.ListItems(ctx, pending.Turn.ID, 0, 1000)
				if e != nil || harness.ActiveCancellationFence(items) {
					t.Fatalf("refused cancel left product fence: %v", e)
				}
				// Resolve the fixture's held obligation before approving the
				// same pending write. The refused cancellation must not block it.
				if _, e = c.Settle(ctx, pending.Turn.ID, q.ID, q.Requested, json.RawMessage("{}")); e != nil {
					t.Fatal(e)
				}
				if _, e = session.Refresh(ctx, "two"); e != nil {
					t.Fatalf("refresh blocked after refusal: %v", e)
				}
			}
			if mode == "cancel" {
				stopped, e := session.CancelSubtask(ctx, "two", task.ID, "stop")

				if e != nil || len(stopped.Subtasks) != 1 || stopped.Subtasks[0].Status != "cancelled" || writes != 0 {
					t.Fatalf("known child with ordinary Tool history could not cancel: %v %+v", e, stopped.Subtasks)
				}
				after, e := runs.LoadBudget(ctx, pending.Turn.ID)
				if e != nil || !after.RunCancelled(task.RunID) || after.RunCancelled("") || after.View(task.RunID).Usage.ToolCalls != 1 {
					t.Fatalf("wrong subtree accounting: %v %+v", e, after)
				}
			} else {
				comment := "approve original write"
				done, e := session.ResolveSubtaskApproval(ctx, "two", task.ID, task.Approval.CheckpointID, harness.ResolveApprovalRequest{Decision: harness.ApprovalApprove, Comment: comment})
				if e != nil || writes != 1 || len(done.Subtasks) != 1 || done.Subtasks[0].Status != "completed" {
					t.Fatalf("second original approval: %v %+v writes=%d", e, done.Subtasks, writes)
				}
				source, e := runs.Load(ctx, task.RunID)
				if e != nil {
					t.Fatal(e)
				}
				decisions, e := options.Runtime.NativeApprovalDecisions(ctx, source)
				want := kernel.ManagedApprovalDecision{CheckpointID: task.Approval.CheckpointID, Decision: "approve", Comment: comment}
				if e != nil || len(decisions) == 0 || decisions[len(decisions)-1] != want {
					t.Fatalf("native original decision not retained: %v %+v", e, decisions)
				}
				// Check the original approval control projection rather than
				// relying solely on the terminal child result.
				count := 0
				for _, i := range done.Items {
					if i.RunID == task.RunID && i.Kind == harness.ItemApproval && i.Status == harness.ItemCompleted {
						var observed struct{ CheckpointID, Decision, Comment string }
						if json.Unmarshal(i.Payload, &observed) == nil && observed.CheckpointID == want.CheckpointID {
							if observed.Decision != want.Decision || observed.Comment != want.Comment {
								t.Fatal("projected a different approval")
							}
							count++
						}
					}
					if i.RunID == task.RunID && i.ParentItemID == harness.ExecutionDispatchParentItemID {
						t.Fatal("qualified native child used Host dispatch")
					}
				}
				if count != 1 {
					t.Fatalf("original approval projections=%d", count)
				}
			}
			childOwner, e := host.GetInvocationByExecutionRefID(ctx, task.RunID)
			wantChildStatus := harness.InvocationCompleted
			wantWrites := 1
			if mode == "cancel" {
				wantChildStatus, wantWrites = harness.InvocationCancelled, 0
			}
			if e != nil || childOwner.Status != wantChildStatus {
				t.Fatalf("target invocation projection: %v %+v", e, childOwner)
			}
			finished, e := session.Resume(ctx, "two")
			if e != nil || finished.Turn.Status != harness.TurnCompleted || reads != 1 || writes != wantWrites {
				t.Fatalf("supervisor continuation: %v %s reads=%d writes=%d", e, finished.Turn.Status, reads, writes)
			}
		})
	}
}

type nativeAuthorityModel struct {
	requests []model.Request
	generate func(model.Request) (model.Response, error)
}

func (m *nativeAuthorityModel) Generate(_ context.Context, request model.Request) (model.Response, error) {
	m.requests = append(m.requests, model.CloneRequest(request))
	if m.generate != nil {
		return m.generate(request)
	}
	return model.Response{Content: "answer"}, nil
}

func nativeAuthorityOptions(t *testing.T, runStore kernel.Store, store harness.Store, client model.Client) harness.AgentSessionOptions {
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

func nativeAuthorityTool(key string, calls *int) tools.Registration {
	return tools.Registration{
		Definition: tools.Definition{Key: key, Name: key, InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false}`)},
		Handler: tools.HandlerFunc(func(_ context.Context, request tools.ExecutionRequest) (tools.ExecutionResult, error) {
			*calls++
			return tools.ExecutionResult{Content: json.RawMessage(`{"evidence":"verified"}`), Receipt: tools.Receipt{ExecutionID: request.Call.ID, Disposition: "read"}}, nil
		}),
	}
}

var (
	errRootRetryCommittedAckLost = errors.New("test original Retry Store response lost after commit")
	errRootRetryCancelAckLost    = errors.New("test unstarted Retry cancellation Store response lost after commit")
)

// Original Host Store itself records the Attempt selection,
// and the adapter discards only the ACK. Restarted callers must never
// reconstruct a second Attempt or execute an unadmitted external effect.
var errFirstCreateCut = errors.New("test original first Create interrupted before commit")

type nativeFirstCreateCut struct {
	nativeAuthorityKernelPorts
	cut bool
}

func (s *nativeFirstCreateCut) Create(ctx context.Context, r kernel.Record, events []kernel.EventDraft) (kernel.Snapshot, error) {
	if s.cut {
		s.cut = false
		return kernel.Snapshot{}, errFirstCreateCut
	}
	return s.nativeAuthorityKernelPorts.Create(ctx, r, events)
}

type nativeRetryLostACKStore struct {
	harness.Store
	budget.LedgerStore
	failOnce      bool
	cancelAckOnce bool
}

func (s *nativeRetryLostACKStore) UpdateTurn(ctx context.Context, turn harness.Turn, revision uint64) (harness.Turn, error) {
	updated, err := s.Store.UpdateTurn(ctx, turn, revision)
	if err == nil && turn.Status == harness.TurnCancelled && s.cancelAckOnce {
		s.cancelAckOnce = false
		return harness.Turn{}, errRootRetryCancelAckLost
	}
	return updated, err
}

func (s *nativeRetryLostACKStore) RetryInvocation(
	ctx context.Context, id string, revision uint64, next string, now time.Time,
) (harness.Invocation, error) {
	saved, err := s.Store.RetryInvocation(ctx, id, revision, next, now)
	if err == nil && s.failOnce {
		s.failOnce = false
		return harness.Invocation{}, errRootRetryCommittedAckLost
	}
	return saved, err
}

type nativeChildReturnInterrupted struct{}
type nativeChildReturnCut struct{ nativeAuthorityKernelPorts }

func (s *nativeChildReturnCut) RecordPhysicalCallbackReturn(ctx context.Context, g kernel.ChildExecutionGrant, rev uint64, o kernel.PhysicalCallbackOutcome, p kernel.PhysicalCallbackReturn) error {
	err := s.nativeAuthorityKernelPorts.RecordPhysicalCallbackReturn(ctx, g, rev, o, p)
	if err == nil && g.AdmissionOperation == "approval" && o.HostCompleted {
		panic(nativeChildReturnInterrupted{})
	}
	return err
}
func interruptNativeChildReturn(t *testing.T, f func()) {
	t.Helper()
	defer func() {
		if _, ok := recover().(nativeChildReturnInterrupted); !ok {
			t.Fatal("fixture did not interrupt the native return")
		}
	}()
	f()
}
