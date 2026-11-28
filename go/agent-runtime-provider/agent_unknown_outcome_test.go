package provider_test

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/jsoncontract"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

func newProviderUnknownStore() (*memory.Store, *providerRetryClock) {
	clock := &providerRetryClock{now: time.Date(2026, 11, 13, 0, 0, 0, 0, time.UTC)}
	return memory.NewStoreWithExecutionClock(clock), clock
}

// Both the Store authority clock and the fresh owner's clock advance. Expiry is
// not a receipt: the exact original intent, whole snapshot/budget, and physical
// grant must survive without another HTTP request or Tool invocation.
func assertProviderUnknownNoReplay(t *testing.T, store *memory.Store, clock *providerRetryClock, deps agent.Dependencies, returned kernel.Snapshot, callErr error, effects func() int64) {
	t.Helper()
	if !errors.Is(callErr, agent.ErrModelOutcomeUnknown) || returned.Run.Status != kernel.RunStatusRunning {
		t.Fatalf("expected physical Model unknown, got %v %+v", callErr, returned.Run)
	}
	before, err := store.Load(t.Context(), returned.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	view, err := agent.ViewState(before)
	if err != nil || len(view.ModelInvocations) == 0 {
		t.Fatalf("missing original intent: %v", err)
	}
	pending := view.ModelInvocations[len(view.ModelInvocations)-1]
	// ViewState clones Response: empty ToolCalls, HostedToolCalls and Artifacts
	// are allocated zero-length slices. Compare the complete canonical zero
	// response, not a literal with nil slices; no response field is ignored.
	if pending.Status != agent.ModelInvocationPending || pending.DispatchAttempt == 0 || pending.ID == "" || pending.RequestHash == "" || pending.BindingFingerprint == "" || pending.ProviderResponseID != "" || pending.Usage != nil || pending.RetryAfter != nil || !reflect.DeepEqual(pending.Response, model.CloneResponse(model.Response{})) {
		t.Fatalf("unknown has false receipt/retry/source: %+v", pending)
	}
	assertProviderPendingSource(t, before.Run.ID, pending)
	grant, err := store.LookupChildExecution(t.Context(), before.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	count := effects()
	clock.now = clock.now.Add(24 * time.Hour)
	rt, err := kernel.New(kernel.Dependencies{Store: store, Clock: clock})
	if err != nil {
		t.Fatal(err)
	}
	deps.Runtime = rt
	deps.Clock = clock
	fresh, err := agent.NewRunner(deps)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = fresh.Resume(t.Context(), before.Run.ID, before.Run.Revision); !errors.Is(err, agent.ErrModelOutcomeUnknown) {
		t.Fatalf("expired unknown resumed: %v", err)
	}
	after, err := store.Load(t.Context(), before.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	afterGrant, err := store.LookupChildExecution(t.Context(), before.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(grant, afterGrant) || effects() != count {
		t.Fatalf("unknown recovery changed original source/budget/grant or replayed: before=%+v after=%+v grant=%+v afterGrant=%+v effects=%d/%d", before, after, grant, afterGrant, count, effects())
	}
}

// Embed the concrete Store, faithfully retaining every capability. Reject only
// this Run's actual dispatch-intent transaction, BEFORE forwarding/committing.
// This is an unsent positive, not a provider error relabelled as no-effect.
type providerUnsentStore struct {
	*memory.Store
	runID    string
	hits     int
	rejected agent.ModelInvocation
}

func (s *providerUnsentStore) Commit(commitCtx context.Context, commitID string, commitRev uint64, commitMutation kernel.StoreMutation, commitAuth kernel.CommitAuthority) (kernel.Snapshot, bool, error) {
	if commitAuth.Lease != nil {
		for _, e := range commitMutation.Events {
			if s.hits == 0 && commitMutation.Record.Run.ID == s.runID && e.Type == "agent.model_invocation.dispatch_intent" {
				original, err := s.Load(commitCtx, s.runID)
				if err != nil {
					return kernel.Snapshot{}, false, err
				}
				view, err := agent.ViewState(original)
				if err != nil || len(view.ModelInvocations) != 1 {
					return kernel.Snapshot{}, false, kernel.ErrInvalidInput
				}
				intent := view.ModelInvocations[0]
				if original.Run.Revision != commitRev || intent.ID != e.Message || intent.RunID != s.runID || intent.Status != agent.ModelInvocationPending || intent.DispatchAttempt != 0 || intent.ExecutionAttempt != 1 {
					return kernel.Snapshot{}, false, kernel.ErrInvalidInput
				}
				s.rejected = intent
				s.hits++
				return kernel.Snapshot{}, false, model.NewRetryableError(errors.New("fixture rejected dispatch CAS before commit"))
			}
		}
		return s.Store.Commit(commitCtx, commitID, commitRev, commitMutation, commitAuth)
	}
	return s.Store.Commit(commitCtx, commitID, commitRev, commitMutation, commitAuth)
}

func assertProviderPendingSource(t *testing.T, runID string, pending agent.ModelInvocation) {
	t.Helper()
	if pending.RunID != runID || pending.Request.InvocationID != pending.ID || pending.SourceRevision == 0 || pending.CreatedAt.IsZero() || pending.ExecutionAttempt == 0 {
		t.Fatalf("lost original invocation source: %+v", pending)
	}
	request := model.CloneRequest(pending.Request)
	request.InvocationID = ""
	proof, err := jsoncontract.SealOpaque("agent-model-intent-opaque-v2", struct {
		Request             model.Request `json:"request"`
		CatalogFingerprints []string      `json:"catalogFingerprints"`
		Provider            string        `json:"provider"`
		BindingFingerprint  string        `json:"bindingFingerprint"`
	}{request, pending.CatalogFingerprints, pending.Provider, pending.BindingFingerprint})
	if err != nil {
		t.Fatal(err)
	}
	if pending.RequestHash != proof.Digest {
		t.Fatal("original full request/catalog/provider/binding hash differs")
	}
}
