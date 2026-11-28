package kernel

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/internal/featurecontrol"
)

func TestOwnedPhysicalEntryOperationAndAuthority(t *testing.T) {
	now := time.Date(2026, 11, 25, 0, 0, 0, 0, time.UTC)
	base := Snapshot{Run: Run{ID: "root", Kind: "agent", Revision: 3, Status: RunStatusRunning}, State: []byte("{}"), EventHead: 3}
	lease := ExecutionLease{RunID: "root", OwnerID: "owner", LeaseID: "lease", Epoch: 2, ExpiresAt: now.Add(time.Minute)}
	g := ChildExecutionGrant{ID: "grant", ChildRunID: "root", ExpectedRevision: 3, ExecutionOwnerID: "owner", ExecutionLeaseID: "lease", ExecutionEpoch: 2, AdmissionSourceHash: strings.Repeat("a", 64), AdmissionOperation: "resume"}
	for _, op := range []string{"start", "prepare", "resume", "approval", "managed_resume", "unknown"} {
		for _, status := range []RunStatus{RunStatusRunning, RunStatusWaitingInput, RunStatusPausedBudget, RunStatusCompleted, RunStatusCancelled, RunStatusFailed} {
			t.Run(op+"/"+string(status), func(t *testing.T) {
				a, s := g, base
				a.AdmissionOperation = op
				s.Run.Status = status
				transfer := ManagedTransfer{}
				if op == "managed_resume" {
					original, _, _, _, _, _ := managedReducerFixture()
					point := original.SafePoint
					point.Source = s
					selection := ManagedRecoveryAuthorization{Mode: ManagedResume}
					transfer = ManagedTransfer{SuccessorGrant: a, SuccessorLease: lease, SafePoint: point, Selection: selection, Revision: 3}
				}
				epoch := a.ExecutionEpoch - 1
				w, e := ValidateOwnedPhysicalEntry(a, s, s, lease, lease, now, false, epoch, 0, OwnedPhysicalCallbackCompletion{}, transfer)
				want := ((op == "start" || op == "prepare" || op == "resume" || op == "managed_resume") && status == RunStatusRunning) || (op == "approval" && status == RunStatusWaitingInput)
				if (e == nil) != want || (want && w.LeaseRemaining != time.Minute) {
					t.Fatalf("entry status mismatch: %+v %v", w, e)
				}
			})
		}
	}
	for _, tc := range []struct {
		name  string
		alter func(*Snapshot, *Snapshot, *ExecutionLease, *uint64, *uint64, *OwnedPhysicalCallbackCompletion, *ManagedTransfer, *bool)
		want  error
	}{
		{"expired persisted lease", func(_, _ *Snapshot, l *ExecutionLease, _, _ *uint64, _ *OwnedPhysicalCallbackCompletion, _ *ManagedTransfer, _ *bool) {
			l.ExpiresAt = now
		}, ErrExecutionConflict},
		{"foreign epoch", func(_, _ *Snapshot, l *ExecutionLease, _, _ *uint64, _ *OwnedPhysicalCallbackCompletion, _ *ManagedTransfer, _ *bool) {
			l.Epoch++
		}, ErrExecutionConflict},
		{"foreign owner", func(_, _ *Snapshot, l *ExecutionLease, _, _ *uint64, _ *OwnedPhysicalCallbackCompletion, _ *ManagedTransfer, _ *bool) {
			l.OwnerID = "foreign"
		}, ErrExecutionConflict},
		{"already entered", func(_, _ *Snapshot, _ *ExecutionLease, p, _ *uint64, _ *OwnedPhysicalCallbackCompletion, _ *ManagedTransfer, _ *bool) {
			*p = g.ExecutionEpoch
		}, ErrExecutionConflict},
		{"physical epoch changed", func(_, _ *Snapshot, _ *ExecutionLease, p, _ *uint64, _ *OwnedPhysicalCallbackCompletion, _ *ManagedTransfer, _ *bool) {
			*p = 3
		}, ErrExecutionConflict},
		{"settled", func(_, _ *Snapshot, _ *ExecutionLease, _, m *uint64, _ *OwnedPhysicalCallbackCompletion, _ *ManagedTransfer, _ *bool) {
			*m = 3
		}, ErrExecutionConflict},
		{"return exists", func(_, _ *Snapshot, _ *ExecutionLease, _, _ *uint64, c *OwnedPhysicalCallbackCompletion, _ *ManagedTransfer, _ *bool) {
			c.GrantID = g.ID
		}, ErrExecutionConflict},
		{"quarantined", func(_, _ *Snapshot, _ *ExecutionLease, _, _ *uint64, _ *OwnedPhysicalCallbackCompletion, _ *ManagedTransfer, q *bool) {
			*q = true
		}, ErrExecutionQuarantined},
		{"source event head", func(a, _ *Snapshot, _ *ExecutionLease, _, _ *uint64, _ *OwnedPhysicalCallbackCompletion, _ *ManagedTransfer, _ *bool) {
			a.EventHead++
		}, ErrExecutionConflict},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, b, l, p, m, c, transfer, q := base, base, lease, g.ExecutionEpoch-1, uint64(0), OwnedPhysicalCallbackCompletion{}, ManagedTransfer{}, false
			tc.alter(&a, &b, &l, &p, &m, &c, &transfer, &q)
			if w, e := ValidateOwnedPhysicalEntry(g, a, b, lease, l, now, q, p, m, c, transfer); !errors.Is(e, tc.want) || w != (OwnedPhysicalEntryWindow{}) {
				t.Fatalf("invalid entry: %+v %v", w, e)
			}
		})
	}
	// A due deadline is an observed fact, not a lost lease. Only the
	// first-party receipt-only path may use this distinction.
	due := now.Add(-time.Second)
	base.Run.DeadlineAt = &due
	window, err := ValidateOwnedPhysicalEntry(g, base, base, lease, lease, now, false, g.ExecutionEpoch-1, 0, OwnedPhysicalCallbackCompletion{}, ManagedTransfer{})
	if err != nil || window.DeadlineRemaining != -time.Second || window.LeaseRemaining != time.Minute {
		t.Fatalf("deadline observation: %+v %v", window, err)
	}

}

func TestOwnedNoEntryEvidenceCannotBecomeActualReturn(t *testing.T) {
	_, source, lease, g, _, _ := managedReducerFixture()
	g.ExpectedRevision = source.Run.Revision
	outcome := OwnedPhysicalCallbackOutcome{LeaseOwned: true, ControlCustody: OwnedCustodyNotEntered}
	payload := OwnedPhysicalCallbackReturnPayload(source, outcome)
	seal := func(domain string) OwnedPhysicalCallbackReturn {
		return OwnedPhysicalCallbackReturn{proof: featurecontrol.Seal(source.Run.ID, source.Run.Revision, domain, g.ID, payload, nil)}
	}
	if seal("agent_owned_callback_return").Matches(g, source, outcome) {
		t.Fatal("actual return domain forged no-entry proof")
	}
	p := seal("agent_owned_callback_not_entered")
	c := p.Completion(g, source, outcome)
	if !c.MatchesNotEnteredSnapshot(g, source) || c.HostCompleted || c.MatchesReturnSnapshot(source) {
		t.Fatalf("no-entry shape: %+v", c)
	}
	managed := g
	managed.AdmissionOperation = "managed_resume"
	if p.Matches(managed, source, outcome) || c.MatchesNotEnteredSnapshot(managed, source) {
		t.Fatal("managed successor borrowed ordinary retirement")
	}
	changed := source
	changed.EventHead++
	if c.MatchesNotEnteredSnapshot(g, changed) {
		t.Fatal("changed source inherited no-entry")
	}
	permit := ChildExecutionSettlement{proof: featurecontrol.Seal(source.Run.ID, source.Run.Revision, "agent_owned_physical_settled", g.ID, source.State, source)}
	receipt, e := BuildOwnedRetirementReceipt(g, source, lease, c, permit, []ManagedTransfer{})
	if e != nil || !receipt.Valid() || !receipt.Evidence.Completion.NotEntered() {
		t.Fatalf("exact no-entry could not retire: %v", e)
	}
	if _, e = BuildOwnedRetirementReceipt(g, changed, lease, c, permit, []ManagedTransfer{}); !errors.Is(e, ErrExecutionConflict) {
		t.Fatal("changed source borrowed no-entry")
	}
	if _, e = BuildOwnedRetirementReceipt(g, source, lease, c, permit, []ManagedTransfer{{}}); !errors.Is(e, ErrExecutionConflict) {
		t.Fatal("no-entry retirement borrowed managed closure")
	}
	if e = ValidateOwnedPhysicalRecovery(g, source, lease, source.Run.Revision, StoreMutation{}, OwnedPhysicalRecovery{modelEvidence: true}, c, 0); !errors.Is(e, ErrExecutionConflict) {
		t.Fatal("no-entry granted receipt CAS")
	}
}
