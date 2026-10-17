package conformance

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

// RunKernelApplyEvidenceSuite exercises actual Memory / SQLite / PostgreSQL
// EventJournal persistence, including when later Run revisions hide the
// original committed State.
func RunKernelApplyEvidenceSuite(t *testing.T, factory ExecutionStoreFactory) {
	t.Helper()
	t.Run("evidence-survives-newer-cas-and-denies-mismatched-replay", func(t *testing.T) {
		rt, err := kernel.New(kernel.Dependencies{Store: factory(t)})
		if err != nil {
			t.Fatal(err)
		}
		started, err := rt.Create(t.Context(), kernel.CreateRequest{
			ID: "apply-evidence-run", Kind: "test.evidence",
			Actor:  kernel.ActorRef{TenantID: "tenant", ActorID: "actor"},
			Thread: kernel.ThreadRef{Kind: "conversation", ID: "thread"},
			Goal:   "identify exact committed Apply", State: json.RawMessage(`{}`),
		})
		if err != nil {
			t.Fatal(err)
		}
		change := kernel.Mutation{
			Status: kernel.RunStatusRunning,
			State:  json.RawMessage(`{"value":"intended"}`),
			Events: []kernel.EventDraft{{Type: "feature.intended", Wakeup: true}},
		}
		committed, err := rt.ApplyIdentified(t.Context(), started.Run.ID, started.Run.Revision, "stable-operation-1", change)
		if err != nil || committed.Run.Revision != 2 {
			t.Fatalf("first Apply: %+v err=%v", committed.Run, err)
		}
		advanced, err := rt.Apply(t.Context(), committed.Run.ID, committed.Run.Revision, kernel.Mutation{
			Status: kernel.RunStatusRunning, State: json.RawMessage(`{"value":"later"}`),
			Events: []kernel.EventDraft{{Type: "feature.later"}},
		})
		if err != nil {
			t.Fatal(err)
		}
		result, err := rt.VerifyIdentifiedApply(t.Context(), started.Run.ID, started.Run.Revision, "stable-operation-1", change)
		if err != nil || result.Status != kernel.ApplyEvidenceCommitted ||
			result.CommittedRevision != 2 || result.CurrentRevision != advanced.Run.Revision {
			t.Fatalf("older exact Apply proof was lost: %+v err=%v", result, err)
		}
		if _, err = rt.ApplyIdentified(t.Context(), started.Run.ID, started.Run.Revision, "stable-operation-1", change); !errors.Is(err, kernel.ErrApplyAlreadyCommitted) {
			t.Fatalf("duplicate operation was replayed: %v", err)
		}
		different := change
		different.State = json.RawMessage(`{"value":"modified"}`)
		if _, err = rt.VerifyIdentifiedApply(t.Context(), started.Run.ID, started.Run.Revision, "stable-operation-1", different); !errors.Is(err, kernel.ErrConflict) {
			t.Fatalf("same identity with different payload received a false receipt: %v", err)
		}
		// Projection metadata is part of the immutable request fingerprint.
		different = change
		different.Events = []kernel.EventDraft{{Type: "feature.intended", Wakeup: false}}
		if _, err = rt.VerifyIdentifiedApply(t.Context(), started.Run.ID, started.Run.Revision, "stable-operation-1", different); !errors.Is(err, kernel.ErrConflict) {
			t.Fatalf("same ID with changed wakeup semantics received a false receipt: %v", err)
		}
		if _, err = rt.ApplyIdentified(t.Context(), started.Run.ID, advanced.Run.Revision, "stable-operation-1", change); !errors.Is(err, kernel.ErrConflict) {
			t.Fatalf("committed operation ID was rebased onto a later revision: %v", err)
		}
		// There was no proof for this operation, but another operation
		// advanced the Run: do not claim "not committed" or auto-retry.
		if _, err = rt.VerifyIdentifiedApply(t.Context(), started.Run.ID, committed.Run.Revision, "missing-operation", change); !errors.Is(err, kernel.ErrApplyOutcomeUnknown) {
			t.Fatalf("later Run CAS was mistaken for a known rollback: %v", err)
		}
		// Still at the exact expected revision, a fixed-revision CAS retry
		// is safe even if the original caller is still in-flight.
		uncommitted, err := rt.VerifyIdentifiedApply(t.Context(), started.Run.ID, advanced.Run.Revision, "not-submitted", change)
		if err != nil || uncommitted.Status != kernel.ApplyEvidenceRetryableCAS {
			t.Fatalf("absent operation at unchanged revision not retryable: %+v err=%v", uncommitted, err)
		}
	})
	t.Run("fenced-native-apply-evidence-requires-current-lease", func(t *testing.T) {
		rt, err := kernel.New(kernel.Dependencies{Store: factory(t)})
		if err != nil {
			t.Fatal(err)
		}
		created, err := rt.Create(t.Context(), kernel.CreateRequest{
			ExecutionRequired: true, ID: "native-apply-evidence", Kind: "agent",
			Actor:  kernel.ActorRef{TenantID: "p", ActorID: "a"},
			Thread: kernel.ThreadRef{Kind: "conversation", ID: "thread"},
			Goal:   "fenced operation evidence", State: json.RawMessage(`{}`),
		})
		if err != nil {
			t.Fatal(err)
		}
		mutation := kernel.Mutation{Status: kernel.RunStatusRunning, State: json.RawMessage(`{"step":1}`)}
		if _, err = rt.ApplyIdentified(t.Context(), created.Run.ID, created.Run.Revision, "native-apply", mutation); !errors.Is(err, kernel.ErrExecutionRequired) {
			t.Fatalf("unbound ApplyIdentified bypassed the Native execution fence: %v", err)
		}
		lease, err := rt.AcquireExecution(t.Context(), kernel.ExecutionAcquireRequest{
			RunID: created.Run.ID, OwnerID: "proof-writer", LeaseDuration: time.Minute,
		})
		if err != nil {
			t.Fatal(err)
		}
		bound, err := rt.WithExecution(lease)
		if err != nil {
			t.Fatal(err)
		}
		saved, err := bound.ApplyIdentified(t.Context(), created.Run.ID, created.Run.Revision, "native-apply", mutation)
		if err != nil || saved.Run.Revision != created.Run.Revision+1 {
			t.Fatalf("Native guarded ApplyIdentified failed: %+v err=%v", saved.Run, err)
		}
		if proof, verifyErr := rt.VerifyIdentifiedApply(t.Context(), created.Run.ID, created.Run.Revision, "native-apply", mutation); verifyErr != nil || proof.Status != kernel.ApplyEvidenceCommitted {
			t.Fatalf("unbound read-only recovery could not read Native proof: %+v err=%v", proof, verifyErr)
		}
	})
	t.Run("many-committed-events-do-not-hide-proof", func(t *testing.T) {
		rt, err := kernel.New(kernel.Dependencies{Store: factory(t)})
		if err != nil {
			t.Fatal(err)
		}
		created, err := rt.Create(t.Context(), kernel.CreateRequest{
			ID: "long-apply-journal", Kind: "test.evidence",
			Actor:  kernel.ActorRef{TenantID: "t", ActorID: "a"},
			Thread: kernel.ThreadRef{Kind: "conversation", ID: "thread"},
			Goal:   "paginated journal recovery", State: json.RawMessage(`{}`),
		})
		if err != nil {
			t.Fatal(err)
		}
		change := kernel.Mutation{Status: kernel.RunStatusRunning, State: json.RawMessage(`{"first":true}`)}
		first, err := rt.ApplyIdentified(t.Context(), created.Run.ID, created.Run.Revision, "first-operation", change)
		if err != nil {
			t.Fatal(err)
		}
		large := make([]kernel.EventDraft, 620)
		for i := range large {
			large[i] = kernel.EventDraft{Type: "feature.history"}
		}
		later, err := rt.Apply(t.Context(), first.Run.ID, first.Run.Revision, kernel.Mutation{
			Status: kernel.RunStatusRunning, State: json.RawMessage(`{"later":true}`), Events: large,
		})
		if err != nil {
			t.Fatal(err)
		}
		if later.EventHead <= 512 {
			t.Fatalf("paging fixture has only %d events", later.EventHead)
		}
		result, err := rt.VerifyIdentifiedApply(t.Context(), created.Run.ID, created.Run.Revision, "first-operation", change)
		if err != nil || result.Status != kernel.ApplyEvidenceCommitted || result.CurrentRevision != later.Run.Revision {
			t.Fatalf("recovery lost historical operation in paged journal: %+v err=%v", result, err)
		}
	})
	t.Run("reserved-proof-event-cannot-be-supplied-through-plain-apply", func(t *testing.T) {
		rt, err := kernel.New(kernel.Dependencies{Store: factory(t)})
		if err != nil {
			t.Fatal(err)
		}
		run, err := rt.Create(t.Context(), kernel.CreateRequest{
			ID: "reserved-evidence", Kind: "test.evidence",
			Actor:  kernel.ActorRef{TenantID: "t", ActorID: "a"},
			Thread: kernel.ThreadRef{Kind: "conversation", ID: "thread"},
			Goal:   "no unverified proofs", State: json.RawMessage(`{}`),
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = rt.Apply(t.Context(), run.Run.ID, run.Run.Revision, kernel.Mutation{
			Status: kernel.RunStatusRunning, State: run.State,
			Events: []kernel.EventDraft{{Type: "kernel.apply.evidence", Data: json.RawMessage(`{}`)}},
		}); !errors.Is(err, kernel.ErrInvalidInput) {
			t.Fatalf("feature forged the reserved evidence type: %v", err)
		}
	})
}
