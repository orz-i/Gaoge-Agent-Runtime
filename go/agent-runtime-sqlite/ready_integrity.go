package sqlite

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

// readyPayloadFingerprint binds the immutable identity and transition payload.
// Delivery fields (lease, retry time, attempts, quarantine) are mutable and
// intentionally excluded. This detects accidental/torn record modification,
// not a malicious writer with unrestricted access to the local DB.
func readyPayloadFingerprint(workID, runID, kind, status string, revision uint64, committedNS int64, events []byte) string {
	digest := sha256.New()
	for _, part := range [][]byte{
		[]byte(workID), []byte(runID), []byte(kind), []byte(status),
		[]byte(fmt.Sprintf("%d", revision)),
		[]byte(fmt.Sprintf("%d", committedNS)),
		events,
	} {
		_, _ = fmt.Fprintf(digest, "%d:", len(part))
		_, _ = digest.Write(part)
	}
	return hex.EncodeToString(digest.Sum(nil))
}

func validProjectedTransition(workID, runID string, revision uint64, status kernel.RunStatus, events []kernel.EventDraft) bool {
	return revision > 0 && workID == transitionID(runID, revision) &&
		kernel.NeedsTransitionProjection(status, events)
}
