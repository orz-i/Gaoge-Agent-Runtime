package kernel_test

import (
	"errors"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

// A deferred Agent's original PREPARE callback may be acknowledged before
// ANY Host Approval/Completion or Model/Tool callback has run. Both native
// Store implementations validate this same sealed receipt at the Run lock.
// Later resumes, stale/duplicate retirements or arbitrary Control mutations
// must not convert the prepare receipt into post-callback custody.
func TestPreparedRootSettlementOnlyBeforeFirstPhysicalExecution(t *testing.T) {
	prepared, receipt := settledRootFixture()
	prepared.Run.Revision = 1
	prepared.Run.Status = kernel.RunStatusRunning
	prepared.EventHead = 2
	receipt.Evidence.Grant.ExpectedRevision = 1
	receipt.Evidence.Snapshot = prepared
	receipt.Evidence.Completion.ReturnRevision = 1
	receipt.Digest = receipt.Identity()
	if !receipt.Valid() {
		t.Fatal("prepared receipt fixture is not a valid original ACK")
	}
	original := []kernel.OwnedRetirementReceipt{receipt}
	if err := kernel.VerifyPreparedRootSettlementFacts(prepared, prepared, 3, 3, kernel.ExecutionLease{}, false, original); err != nil {
		t.Fatalf("initial acknowledged prepared Root rejected: %v", err)
	}
	cancelled := prepared
	cancelled.Run.Revision = 2
	cancelled.Run.Status = kernel.RunStatusCancelled
	cancelled.Run.ErrorCode = "run.cancelled"
	cancelled.EventHead++
	if err := kernel.VerifyPreparedRootSettlementFacts(cancelled, cancelled, 4, 3, kernel.ExecutionLease{}, false, original); err != nil {
		t.Fatalf("original native Cancel successor lost pre-dispatch proof: %v", err)
	}
	for _, testcase := range []struct {
		name     string
		now      kernel.Snapshot
		epoch    uint64
		active   bool
		receipts []kernel.OwnedRetirementReceipt
	}{
		{"no original ACK", prepared, 3, false, nil},
		{"active Model or Tool grant", prepared, 3, true, original},
		{"stale execution epoch", prepared, 4, false, original},
		{"duplicate ACK", prepared, 3, false, []kernel.OwnedRetirementReceipt{receipt, receipt}},
		{"already advanced physical revision", func() kernel.Snapshot { r := prepared; r.Run.Revision = 2; return r }(), 3, false, original},
		{"noncancel terminal", func() kernel.Snapshot {
			r := prepared
			r.Run.Revision = 2
			r.Run.Status = kernel.RunStatusCompleted
			return r
		}(), 3, false, original},
		{"unwitnessed later event", func() kernel.Snapshot { r := cancelled; r.EventHead++; return r }(), 4, false, original},
		{"resume ACK cannot claim Prepare", prepared, 3, false, []kernel.OwnedRetirementReceipt{func() kernel.OwnedRetirementReceipt {
			r := receipt
			r.Evidence.Grant.AdmissionOperation = "resume"
			r.Digest = r.Identity()
			return r
		}()}},
		{"start ACK cannot claim Prepare", prepared, 3, false, []kernel.OwnedRetirementReceipt{func() kernel.OwnedRetirementReceipt {
			r := receipt
			r.Evidence.Grant.AdmissionOperation = "start"
			r.Digest = r.Identity()
			return r
		}()}},
	} {
		t.Run(testcase.name, func(t *testing.T) {
			if err := kernel.VerifyPreparedRootSettlementFacts(testcase.now, testcase.now, testcase.epoch, testcase.epoch, kernel.ExecutionLease{}, testcase.active, testcase.receipts); !errors.Is(err, kernel.ErrExecutionConflict) {
				t.Fatalf("not-only-original-Prepare obtained native callback custody: %v", err)
			}
		})
	}
}
