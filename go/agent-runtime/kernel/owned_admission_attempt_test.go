package kernel

import (
	"errors"
	"fmt"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/internal/featurecontrol"
)

func TestOwnedAdmissionAttemptBindsCommitAndSource(t *testing.T) {
	_, source, lease, grant, _, _ := managedReducerFixture()
	grant.ExpectedRevision = source.Run.Revision
	proof := featurecontrol.Seal(source.Run.ID, source.Run.Revision, "agent_owned_physical_admission", lease.LeaseID, source.State, source.Checkpoint)
	p := OwnedPhysicalAdmission{proof: proof, attempt: [32]byte{1}}
	other := OwnedPhysicalAdmission{proof: proof, attempt: [32]byte{2}}
	grant.AdmissionSourceHash = ownedAdmissionAttemptHash(source, lease, grant.AdmissionOperation, grant.ID, p.attempt)
	if !p.Matches(grant, source, lease) || other.Matches(grant, source, lease) || (OwnedPhysicalAdmission{proof: proof}).Matches(grant, source, lease) {
		t.Fatal("admission proof did not distinguish original attempt")
	}
	foreign := grant
	foreign.ID += "-foreign"
	if p.Matches(foreign, source, lease) {
		t.Fatal("commitment did not bind public Grant ID")
	}
	foreign = grant
	foreign.AdmissionOperation = "resume"
	if p.Matches(foreign, source, lease) {
		t.Fatal("commitment did not bind operation")
	}
	changed := source
	changed.State = []byte(`{"pending":"changed"}`)
	if p.Matches(grant, changed, lease) {
		t.Fatal("commitment did not bind original state")
	}
	lost := errors.New("transport response lost")
	e := p.CommitOutcome(lost)
	if !errors.Is(e, ErrPhysicalAdmissionCommitUnknown) || !errors.Is(e, lost) || !p.commitUncertain(e) || other.commitUncertain(e) {
		t.Fatalf("commit outcome not bound to original attempt: %v", e)
	}
	for _, notOriginal := range []error{nil, lost, ErrPhysicalAdmissionCommitUnknown, other.CommitOutcome(lost), errors.Join(e, ErrExecutionConflict), fmt.Errorf("wrapped: %w", e), p.CommitOutcome(ErrExecutionConflict)} {
		if p.commitUncertain(notOriginal) {
			t.Fatalf("unqualified outcome admitted: %v", notOriginal)
		}
	}
	if p.CommitOutcome(nil) != nil || !errors.Is((OwnedPhysicalAdmission{}).CommitOutcome(lost), lost) || errors.Is((OwnedPhysicalAdmission{}).CommitOutcome(lost), ErrPhysicalAdmissionCommitUnknown) {
		t.Fatal("zero or successful outcome unexpectedly classified as unknown")
	}
}
