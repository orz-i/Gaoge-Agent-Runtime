package harnesspostgres_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/runrelation"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

func TestEA1HarnessSubtaskControlAdmission(t *testing.T) {
	for _, backend := range []string{"memory", "sqlite", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			for _, mode := range []string{"missing", "checkpoint", "decision", "actor", "tenant", "thread", "parent", "origin", "original-runner", "foreign-recorder"} {
				t.Run("P0/"+mode, func(t *testing.T) { ea1HACAdmissionDenied(t, backend, mode) })
			}
			for _, native := range []bool{false, true} {
				name := map[bool]string{false: "default", true: "native"}[native]
				t.Run("P1/"+name, func(t *testing.T) { ea1HACAdmissionBarrier(t, backend, native) })
			}
			for _, mode := range []string{"unknown-tool", "receipt-fault", "opaque-unknown-tool"} {
				t.Run("P2/"+mode, func(t *testing.T) { ea1HACAdmissionUnknown(t, backend, mode) })
			}
		})
	}
}

type ea1HACAdmissionSnapshot struct {
	turn          harness.Turn
	invocation    harness.Invocation
	parent, child kernel.Snapshot
	items         []harness.Item
	ledger        budget.Ledger
}

func ea1HACAdmissionCapture(t *testing.T, f *ea1HACFixture, tid, iid, parent, child string) ea1HACAdmissionSnapshot {
	t.Helper()
	turn, e := f.store.GetTurn(t.Context(), tid)
	if e != nil {
		t.Fatal(e)
	}
	inv, e := f.store.GetInvocation(t.Context(), iid)
	if e != nil {
		t.Fatal(e)
	}
	return ea1HACAdmissionSnapshot{turn: turn, invocation: inv, parent: ea1HACLoad(t, f, parent), child: ea1HACLoad(t, f, child), items: ea1HACItems(t, f.store, tid), ledger: ea1HACBudget(t, f.backend.ledgers, tid)}
}
func (s ea1HACAdmissionSnapshot) unchanged(t *testing.T, f *ea1HACFixture) {
	t.Helper()
	got := ea1HACAdmissionCapture(t, f, s.turn.ID, s.invocation.ID, s.parent.Run.ID, s.child.Run.ID)
	if !reflect.DeepEqual(s, got) {
		t.Fatal("denied Host control changed full Turn/Invocation/Kernel snapshots/items/election/budget")
	}
}
func ea1HACAdmissionDenied(t *testing.T, backend, mode string) {
	f := newEA1HACFixture(t, backend, false)
	waiting, task, root, _ := ea1HACStart(t, f)
	ea1HACAdmissionModel(t, f, ea1HACLoad(t, f, task.RunID))
	before := ea1HACAdmissionCapture(t, f, waiting.Turn.ID, root.ID, root.ExecutionRefID, task.RunID)
	candidate := f.harness
	expected := harness.ErrRuntimeCancellationUnverified
	approval := false
	cp := task.Approval.CheckpointID
	id := task.ID
	decision := harness.ApprovalApprove
	switch mode {
	case "missing":
		approval = true
		id = "missing"
		expected = harness.ErrNotFound
	case "checkpoint":
		approval = true
		cp += "-foreign"
		expected = harness.ErrConflict
	case "decision":
		approval = true
		decision = "invalid"
		expected = harness.ErrInvalidRequest
	case "original-runner":
		other, e := agent.NewRunner(agent.Dependencies{Runtime: f.runtime, Model: f.model})
		if e != nil {
			t.Fatal(e)
		}
		f.executor.original = other
	case "foreign-recorder":
		registry, e := runrelation.New(memory.NewRunRelationStore(), nil)
		if e != nil {
			t.Fatal(e)
		}
		rel, e := f.relations.GetByChild(t.Context(), task.RunID)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = registry.Ensure(t.Context(), runrelation.Draft{ParentRunID: rel.ParentRunID, ChildRunID: rel.ChildRunID, Kind: rel.Kind, OwnerNodeID: rel.OwnerNodeID}); e != nil {
			t.Fatal(e)
		}
		candidate = ea1HACRunner(t, f, f.store, f.backend.ledgers, registry)
	default:
		f.physical.poison = func(s kernel.Snapshot) kernel.Snapshot {
			switch mode {
			case "actor":
				s.Run.Actor.ActorID = "foreign"
			case "tenant":
				s.Run.Actor.TenantID = "foreign"
			case "thread":
				s.Run.Thread.ID = "foreign"
			case "parent":
				s.Run.ParentRunID = "foreign"
			case "origin":
				var state map[string]json.RawMessage
				if e := json.Unmarshal(s.State, &state); e != nil {
					t.Fatal(e)
				}
				state["originFingerprint"] = json.RawMessage(`"foreign"`)
				var e error
				s.State, e = json.Marshal(state)
				if e != nil {
					t.Fatal(e)
				}
			}
			return s
		}
	}
	var err error
	if approval {
		_, err = candidate.ResolveSubtaskApproval(t.Context(), waiting.Turn.ID, id, cp, harness.ResolveApprovalRequest{Decision: decision})
	} else {
		_, err = candidate.CancelSubtask(t.Context(), waiting.Turn.ID, task.ID, "reject foreign original source")
	}
	f.physical.poison = nil
	if !errors.Is(err, expected) {
		t.Fatalf("actual Host %s: %v want %v", mode, err, expected)
	}
	before.unchanged(t, f)
	if f.hostCalls.Load() != 0 || f.approvalCalls.Load() != 0 || f.toolCalls != 0 || f.model.calls != 1 || f.physical.controlCalls.Load() != 0 {
		t.Fatal("denied Host control reached physical callback")
	}
	if _, e := f.runtime.LookupChildExecution(t.Context(), task.RunID); !errors.Is(e, kernel.ErrNotFound) {
		t.Fatalf("denial created child scope: %v", e)
	}
}
func ea1HACAdmissionBarrier(t *testing.T, backend string, native bool) {
	f := newEA1HACFixtureWithOptions(t, backend, false, native, true)
	waiting, task, root, _ := ea1HACStart(t, f)
	ea1HACAdmissionModel(t, f, ea1HACLoad(t, f, task.RunID))
	cuts := 0
	check := func() {
		cuts++
		before := ea1HACAdmissionCapture(t, f, waiting.Turn.ID, root.ID, root.ExecutionRefID, task.RunID)
		grant, e := f.runtime.LookupChildExecution(t.Context(), task.RunID)
		if e != nil {
			t.Fatal(e)
		}
		if grant.IsOwned() || grant.ChildRunID != task.RunID || grant.ExpectedRevision != before.child.Run.Revision {
			t.Fatal("Host approval replaced original child grant")
		}
		_, e = f.harness.CancelSubtask(t.Context(), waiting.Turn.ID, task.ID, "competing active approval")
		if !errors.Is(e, harness.ErrConflict) {
			t.Fatalf("active Host approval cancellation: %v", e)
		}
		before.unchanged(t, f)
		peer, e := kernel.New(kernel.Dependencies{Store: f.kernelBackend.peer})
		if e != nil {
			t.Fatal(e)
		}
		for _, s := range []kernel.Snapshot{before.parent, before.child} {
			_, e = peer.Cancel(t.Context(), s.Run.ID, s.Run.Revision, "direct competing control")
			if !errors.Is(e, kernel.ErrChildExecutionBusy) {
				t.Fatalf("Kernel %s Cancel: %v", s.Run.ID, e)
			}
			before.unchanged(t, f)
			_, e = peer.Apply(t.Context(), s.Run.ID, s.Run.Revision, kernel.Mutation{Status: kernel.RunStatusFailed, State: s.State, Checkpoint: s.Checkpoint})
			want := kernel.ErrChildExecutionBusy
			if native && s.Run.ID == task.RunID {
				want = kernel.ErrExecutionRequired
			}
			if !errors.Is(e, want) {
				t.Fatalf("Kernel %s Apply: %v want %v", s.Run.ID, e, want)
			}
			before.unchanged(t, f)
		}
		after, e := peer.LookupChildExecution(t.Context(), task.RunID)
		if e != nil || after != grant {
			t.Fatal("denial changed original child grant")
		}
	}
	f.hostBefore = check
	f.approvalBefore = check
	_, err := f.harness.ResolveSubtaskApproval(t.Context(), waiting.Turn.ID, task.ID, task.Approval.CheckpointID, harness.ResolveApprovalRequest{Decision: harness.ApprovalApprove})
	if err != nil || cuts != 2 || f.hostCalls.Load() != 1 || f.approvalCalls.Load() != 1 {
		t.Fatalf("actual Host approval failed: %v cuts=%d", err, cuts)
	}
	child := ea1HACLoad(t, f, task.RunID)
	if child.Run.Status != kernel.RunStatusRunning || f.toolCalls != 0 || f.model.calls != 1 || child.Checkpoint == nil {
		t.Fatal("deferred/native approval dispatched new physical work")
	}
	ea1HACAdmissionCheckpoint(t, f, child)
	ea1HACAdmissionModel(t, f, child)
}
func ea1HACAdmissionCheckpoint(t *testing.T, f *ea1HACFixture, s kernel.Snapshot) {
	t.Helper()
	cp, old, actual := s.Checkpoint, f.approvalOriginal, f.approvalResolved
	if cp == nil || old == nil || actual == nil || cp.ID != old.ID || cp.Kind != old.Kind || !cp.CreatedAt.Equal(old.CreatedAt) || cp.Status != kernel.CheckpointResolved || cp.ResolvedAt == nil {
		t.Fatal("original approval checkpoint changed")
	}
	ea1HACAdmissionJSONEqual(t, "original checkpoint payload", cp.Payload, old.Payload)
	ea1HACAdmissionJSONEqual(t, "actual resolved checkpoint", cp, actual)

	var response struct {
		Decision string `json:"decision"`
	}
	if json.Unmarshal(cp.Response, &response) != nil || response.Decision != "approve" {
		t.Fatal("durable checkpoint lacks original decision")
	}
}

func ea1HACAdmissionUnknown(t *testing.T, backend, mode string) {
	// Preserve both contracts: native-owned cancellation can reach its
	// Kernel fence; arbitrary Agent middleware requires stronger local proof.
	opaque := mode == "opaque-unknown-tool"
	f := newEA1HACFixtureWithHostOptions(t, backend, false, false, false, opaque)
	expectedHostCalls := int32(0)
	if opaque {
		expectedHostCalls = 1
	}
	waiting, task, root, ledger := ea1HACStart(t, f)
	ea1HACAdmissionModel(t, f, ea1HACLoad(t, f, task.RunID))
	stop := errors.New("original terminal Tool receipt CAS unavailable")
	hits := 0
	if mode != "receipt-fault" {
		f.toolUnknown = true
	} else {
		f.physical.beforeApply = func(_ context.Context, id string, _ uint64, m kernel.StoreMutation) error {
			if id == task.RunID && m.Record.Run.ID == id && m.Record.Run.Status == kernel.RunStatusCompleted && m.Record.Result != nil {
				for _, event := range m.Events {
					if event.Type == "tool.completed" {
						hits++
						return stop
					}
				}
			}
			return nil
		}
	}
	_, err := f.harness.ResolveSubtaskApproval(t.Context(), waiting.Turn.ID, task.ID, task.Approval.CheckpointID, harness.ResolveApprovalRequest{Decision: harness.ApprovalApprove})
	want := agent.ErrToolOutcomeUnknown
	if mode == "receipt-fault" {
		want = stop
	}
	if !errors.Is(err, want) || !errors.Is(err, kernel.ErrChildExecutionBusy) {
		t.Fatalf("actual Host approval send: %v want %v", err, want)
	}
	if mode == "receipt-fault" && !errors.Is(err, harness.ErrExecutionOutcomeUnknown) {
		t.Fatalf("missing Host callback outcome classification: %v", err)
	}
	if f.toolCalls != 1 || f.model.calls != 1 || f.approvalCalls.Load() != 1 || f.hostCalls.Load() != expectedHostCalls || !reflect.DeepEqual(f.toolRequest, ea1HACExpectedToolRequest(task.RunID)) {
		t.Fatal("actual approved original Tool request/callback counts changed")
	}
	if mode == "receipt-fault" && hits != 1 {
		t.Fatal("ordinary Apply receipt cutpoint did not hit once")
	}
	if mode != "receipt-fault" && !reflect.DeepEqual(f.toolResult, tools.ExecutionResult{}) {
		t.Fatal("unknown provider fabricated receipt")
	}
	if mode == "receipt-fault" {
		ea1HACAdmissionJSONEqual(t, "actual sent Tool full result", f.toolResult, tools.ExecutionResult{
			Content: json.RawMessage(`{"evidence":"approved"}`), Receipt: tools.Receipt{ExecutionID: f.toolRequest.Call.ID, Disposition: "read"},
		})
	}
	sentRequest, sentResult := tools.CloneCall(f.toolRequest.Call), tools.CloneExecutionResult(f.toolResult)
	child := ea1HACLoad(t, f, task.RunID)
	ea1HACAdmissionCheckpoint(t, f, child)
	ea1HACAdmissionModel(t, f, child)
	var state struct {
		ToolDispatchID string `json:"toolDispatchID"`
	}
	if json.Unmarshal(child.State, &state) != nil || state.ToolDispatchID != f.toolRequest.Call.ID {
		t.Fatal("unknown original Tool identity erased")
	}
	before := ea1HACAdmissionCapture(t, f, waiting.Turn.ID, root.ID, root.ExecutionRefID, task.RunID)
	grant, e := f.runtime.LookupChildExecution(t.Context(), task.RunID)
	if e != nil {
		t.Fatal(e)
	}
	pending := mode == "receipt-fault"
	if harness.PendingExecutionDispatch(before.items, root.ID) != pending {
		t.Fatal("Host return fact confused with Kernel effect settlement")
	}
	_, err = f.harness.CancelSubtask(t.Context(), waiting.Turn.ID, task.ID, "cancel uncertain original approved Tool")
	if opaque {
		if !errors.Is(err, harness.ErrRuntimeCancellationUnverified) {
			t.Fatalf("opaque Agent cancellation ownership: %v", err)
		}
		before.unchanged(t, f)
		if f.physical.controlCalls.Load() != 0 {
			t.Fatal("opaque cancellation passed read-only ownership gate")
		}
	} else if pending {
		if !errors.Is(err, harness.ErrConflict) {
			t.Fatalf("pending Host grant cancellation: %v", err)
		}
		before.unchanged(t, f)
	} else {
		if !errors.Is(err, kernel.ErrChildExecutionBusy) {
			t.Fatalf("admitted cancellation lost original Kernel fence: %v", err)
		}
		ea1HACAssertOwner(t, f, before.invocation, before.parent, before.child)
		if !reflect.DeepEqual(ea1HACExpectedBudget(ledger, task.RunID), ea1HACBudget(t, f.backend.ledgers, waiting.Turn.ID)) {
			t.Fatal("admitted cancellation changed more than original subtree budget transition")
		}
		items := ea1HACItems(t, f.store, waiting.Turn.ID)
		ea1OnlyCancelIntent(t, items, waiting.Turn.ID, task.RunID)
		if !harness.ActiveCancellationFence(items) {
			t.Fatal("unknown cancellation lost original election")
		}
	}
	retained, e := f.runtime.LookupChildExecution(t.Context(), task.RunID)
	if e != nil || retained != grant {
		t.Fatal("Host cancellation changed unknown Kernel grant")
	}
	after := ea1HACAdmissionCapture(t, f, waiting.Turn.ID, root.ID, root.ExecutionRefID, task.RunID)
	repeatError := harness.ErrConflict
	if opaque {
		repeatError = harness.ErrRuntimeCancellationUnverified
	}
	_, err = f.harness.CancelSubtask(t.Context(), waiting.Turn.ID, task.ID, "repeat original uncertain cancellation")
	if !errors.Is(err, repeatError) {
		t.Fatalf("repeat Host election: %v", err)
	}
	after.unchanged(t, f)
	// Rebuild the complete original route on reopened persistence, not a bare
	// Core substitute or an unrelated Agent that fails ownership before effects.
	ea1HACReopen(t, f)
	after.unchanged(t, f)
	ea1HACAdmissionModel(t, f, ea1HACLoad(t, f, task.RunID))
	if _, err = f.harness.CancelSubtask(t.Context(), waiting.Turn.ID, task.ID, "fresh owner observes original fence"); !errors.Is(err, repeatError) {
		t.Fatalf("fresh Host election: %v", err)
	}
	after.unchanged(t, f)
	if e = f.agent.ReconcileChildPhysicalGrant(t.Context(), task.RunID); !errors.Is(e, kernel.ErrChildExecutionBusy) {
		t.Fatalf("unknown original callback retired: %v", e)
	}
	if _, e = f.agent.Resume(t.Context(), task.RunID, child.Run.Revision); !errors.Is(e, agent.ErrToolOutcomeUnknown) {
		t.Fatalf("unknown Tool replayed after reopen: %v", e)
	}
	after.unchanged(t, f)
	retained, e = f.runtime.LookupChildExecution(t.Context(), task.RunID)
	if e != nil || retained != grant {
		t.Fatal("fresh owner replaced original grant")
	}
	if f.toolCalls != 1 || f.model.calls != 1 || f.approvalCalls.Load() != 1 || f.hostCalls.Load() != expectedHostCalls {
		t.Fatal("fresh recovery dispatched new physical callback")
	}
	if opaque && f.physical.controlCalls.Load() != 0 {
		t.Fatal("opaque repeat/reopen entered Kernel cancellation")
	}
	ea1HACAdmissionJSONEqual(t, "retained original Tool call", f.toolRequest.Call, sentRequest)
	ea1HACAdmissionJSONEqual(t, "retained actual Tool result", f.toolResult, sentResult)
}

func ea1HACAdmissionJSONEqual(t *testing.T, name string, a, b any) {
	t.Helper()
	decode := func(v any) any {
		raw, e := json.Marshal(v)
		if e != nil {
			t.Fatal(e)
		}
		d := json.NewDecoder(bytes.NewReader(raw))
		d.UseNumber()
		var out any
		if e = d.Decode(&out); e != nil {
			t.Fatal(e)
		}
		return out
	}
	if !reflect.DeepEqual(decode(a), decode(b)) {
		t.Fatalf("%s complete value changed", name)
	}
}

// Capture comes from the real Generate call, before legal consumed-request
// pruning. Recompute the complete original tuple rather than compare pruned
// Request/CatalogFingerprints with the original provider input.
func ea1HACAdmissionModel(t *testing.T, f *ea1HACFixture, s kernel.Snapshot) {
	t.Helper()
	m := f.model
	if m.calls != 1 || len(m.requests) != 1 || len(m.responses) != 1 || len(m.intents) != 1 {
		t.Fatal("original provider call/response/intent capture missing or replayed")
	}
	original := m.intents[0]
	actual := m.requests[0]
	if actual.RunID != s.Run.ID || actual.InvocationID != original.ID || original.RunID != s.Run.ID || original.Status != agent.ModelInvocationPending || original.ExecutionAttempt != 1 || original.DispatchAttempt != 1 {
		t.Fatal("actual Generate lacked original claimed identity/attempt")
	}
	ea1HACAdmissionJSONEqual(t, "full actual original Model request", original.Request, actual)
	request := model.CloneRequest(actual)
	request.InvocationID = ""
	raw, e := json.Marshal(struct {
		Request             model.Request `json:"request"`
		CatalogFingerprints []string      `json:"catalogFingerprints"`
		Provider            string        `json:"provider"`
		BindingFingerprint  string        `json:"bindingFingerprint"`
	}{request, original.CatalogFingerprints, original.Provider, original.BindingFingerprint})
	if e != nil {
		t.Fatal(e)
	}
	digest := sha256.Sum256(raw)
	if original.RequestHash != hex.EncodeToString(digest[:]) {
		t.Fatal("actual complete request/catalog/provider/binding tuple hash mismatch")
	}
	view, e := agent.ViewState(s)
	if e != nil || len(view.ModelInvocations) != 1 {
		t.Fatal("durable original Model invocation missing")
	}
	got := view.ModelInvocations[0]
	if got.ID != original.ID || got.RunID != original.RunID || got.SourceRevision != original.SourceRevision || got.RequestHash != original.RequestHash || got.Model != original.Model || got.Provider != original.Provider || got.BindingFingerprint != original.BindingFingerprint || got.ExecutionAttempt != original.ExecutionAttempt || got.DispatchAttempt != original.DispatchAttempt || !got.CreatedAt.Equal(original.CreatedAt) || got.Status != agent.ModelInvocationConsumed || got.CompletedAt == nil || got.ConsumedAt == nil {
		t.Fatal("durable consumed Model identity/source/attempt changed")
	}
	ea1HACAdmissionJSONEqual(t, "actual complete provider Response", got.Response, m.responses[0])
	ea1HACAdmissionJSONEqual(t, "actual provider Usage", got.Usage, m.responses[0].Usage)
	if got.ProviderResponseID != m.responses[0].ResponseID {
		t.Fatal("actual provider response identity changed")
	}
}
