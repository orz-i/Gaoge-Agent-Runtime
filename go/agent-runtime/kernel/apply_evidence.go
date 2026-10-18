package kernel

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"strings"
)

// An explicit Apply operation is identified by the immutable combination of
// RunID, source revision, caller-stable operation ID and intended mutation.
// Its proof is an ordinary append-only Kernel Event committed in the exact
// same Store CAS as the Feature state, not a separate durable authority.
const identifiedApplyEventType = "kernel.apply.evidence"

var (
	ErrApplyAlreadyCommitted = errors.New("kernel identified Apply is already committed")
	ErrApplyOutcomeUnknown   = errors.New("kernel Apply outcome cannot be proven from the retained journal")
)

type ApplyEvidenceStatus string

const (
	ApplyEvidenceCommitted ApplyEvidenceStatus = "committed"
	// A missing proof while the Run is still at ExpectedRevision means it
	// is safe to RETRY only the same revision-bound CAS. Another caller may
	// still be in flight, but Store CAS prevents both from committing.
	ApplyEvidenceRetryableCAS ApplyEvidenceStatus = "retryable_cas"
)

// ApplyEvidence is a read-only interpretation of the authoritative Run
// snapshot and its append-only journal. It is not a historical Run snapshot.
type ApplyEvidence struct {
	Status            ApplyEvidenceStatus
	CommittedRevision uint64
	CurrentRevision   uint64
}

type identifiedApplyProof struct {
	OperationID    string `json:"operationID"`
	SourceRevision uint64 `json:"sourceRevision"`
	MutationDigest string `json:"mutationDigest"`
}

func validApplyOperation(id string, expectedRevision uint64) bool {
	return id != "" && len(id) <= 128 && strings.TrimSpace(id) == id &&
		!strings.ContainsAny(id, "\r\n\x00") &&
		expectedRevision != 0 && expectedRevision != math.MaxUint64
}

func hasReservedApplyEvidence(events []EventDraft) bool {
	for _, event := range events {
		if event.Type == identifiedApplyEventType {
			return true
		}
	}
	return false
}

func digestApplyMutation(m Mutation) (string, error) {
	// EventDraft.Wakeup is internal projection metadata (json:"-"), but it
	// affects durable delivery. Include it in the signature explicitly.
	flags := make([]bool, len(m.Events))
	for i := range m.Events {
		if m.Events[i].Type == identifiedApplyEventType {
			return "", ErrInvalidInput
		}
		flags[i] = m.Events[i].Wakeup
	}
	material := struct {
		Mutation Mutation `json:"mutation"`
		Wakeups  []bool   `json:"wakeups"`
	}{Mutation: m, Wakeups: flags}
	encoded, err := json.Marshal(material)
	if err != nil {
		return "", errors.Join(ErrInvalidInput, err)
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

// ApplyIdentified commits an explicit mutation and its stable identity in the
// existing EventJournal. Callers MUST persist the original operation ID,
// ExpectedRevision and mutation *before* dispatch and must not rebase a lost
// response onto a newer Revision. A returned store/transport error is UNKNOWN
// until VerifyIdentifiedApply has checked the authoritative journal.
//
// This is an opt-in recovery contract. It does not automatically make the
// external Model/Tool side effect exactly-once or provide caller RBAC.
func (runtime *Runtime) ApplyIdentified(
	ctx context.Context, runID string, expectedRevision uint64,
	operationID string, mutation Mutation,
) (Snapshot, error) {
	if runtime == nil || ctx == nil || strings.TrimSpace(runID) == "" ||
		!validApplyOperation(operationID, expectedRevision) {
		return Snapshot{}, ErrInvalidInput
	}
	digest, err := digestApplyMutation(mutation)
	if err != nil {
		return Snapshot{}, err
	}
	outcome, err := runtime.VerifyIdentifiedApply(ctx, runID, expectedRevision, operationID, mutation)
	if err != nil {
		return Snapshot{}, err
	}
	if outcome.Status == ApplyEvidenceCommitted {
		return Snapshot{}, ErrApplyAlreadyCommitted
	}
	proof, err := json.Marshal(identifiedApplyProof{
		OperationID: operationID, SourceRevision: expectedRevision, MutationDigest: digest,
	})
	if err != nil {
		return Snapshot{}, errors.Join(ErrInvalidInput, err)
	}
	// Keep the caller's EventDraft slice untouched.
	prepared := mutation
	prepared.Events = append(append([]EventDraft(nil), mutation.Events...),
		EventDraft{Type: identifiedApplyEventType, Data: proof})
	return runtime.apply(ctx, runID, expectedRevision, prepared, true)
}

// VerifyIdentifiedApply establishes whether the exact immutable request was
// durably committed, even if a later CAS or terminal transition already moved
// Run.State. Without matching evidence, a *newer* revision is UNKNOWN, never
// an authorization to retry with a rebased revision.
func (runtime *Runtime) VerifyIdentifiedApply(
	ctx context.Context, runID string, expectedRevision uint64,
	operationID string, mutation Mutation,
) (ApplyEvidence, error) {
	if runtime == nil || ctx == nil || strings.TrimSpace(runID) == "" ||
		!validApplyOperation(operationID, expectedRevision) {
		return ApplyEvidence{}, ErrInvalidInput
	}
	hash, err := digestApplyMutation(mutation)
	if err != nil {
		return ApplyEvidence{}, err
	}
	current, err := runtime.Load(ctx, runID)
	if err != nil {
		return ApplyEvidence{}, err
	}
	if current.Run.Revision < expectedRevision {
		return ApplyEvidence{}, ErrConflict
	}
	if current.EventHead < 0 {
		return ApplyEvidence{}, ErrApplyOutcomeUnknown
	}
	result := ApplyEvidence{CurrentRevision: current.Run.Revision}
	// Bounded pages: only the *read-only* rare recovery path scans history.
	// If a page is missing or corrupted, fail closed; never infer rollback.
	var after int64
	var matched bool
	for after < current.EventHead {
		if err := ctx.Err(); err != nil {
			return ApplyEvidence{}, err
		}
		events, readErr := runtime.ListEvents(ctx, runID, after, 512)
		if readErr != nil {
			return ApplyEvidence{}, errors.Join(ErrApplyOutcomeUnknown, readErr)
		}
		if len(events) == 0 || events[0].Seq != after+1 {
			return ApplyEvidence{}, ErrApplyOutcomeUnknown
		}
		for _, event := range events {
			if event.Seq > current.EventHead {
				break
			}
			if event.Seq != after+1 {
				return ApplyEvidence{}, ErrApplyOutcomeUnknown
			}
			after = event.Seq
			if event.Type != identifiedApplyEventType {
				continue
			}
			var proof identifiedApplyProof
			if len(event.Data) == 0 || json.Unmarshal(event.Data, &proof) != nil ||
				!validApplyOperation(proof.OperationID, proof.SourceRevision) ||
				len(proof.MutationDigest) != sha256.Size*2 {
				return ApplyEvidence{}, ErrApplyOutcomeUnknown
			}
			if proof.OperationID != operationID {
				continue
			}
			if matched || proof.SourceRevision != expectedRevision || proof.MutationDigest != hash {
				return ApplyEvidence{}, ErrConflict
			}
			matched = true
		}
	}
	if matched {
		// A historical proof cannot be valid without the target revision
		// having committed. Treat a rewound or damaged aggregate as unknown.
		if current.Run.Revision <= expectedRevision {
			return ApplyEvidence{}, ErrApplyOutcomeUnknown
		}
		result.Status = ApplyEvidenceCommitted
		result.CommittedRevision = expectedRevision + 1
		return result, nil
	}
	if current.Run.Revision != expectedRevision {
		return ApplyEvidence{}, ErrApplyOutcomeUnknown
	}
	result.Status = ApplyEvidenceRetryableCAS
	return result, nil
}
