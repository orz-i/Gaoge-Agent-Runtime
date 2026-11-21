package context

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/jsoncontract"
)

// sealCheckpoint assigns the content hash and ID after the revision is complete.
func sealCheckpoint(value Checkpoint) (Checkpoint, error) {
	hash, err := checkpointWindowHash(value.Window)
	if err != nil {
		return Checkpoint{}, err
	}
	value.ContentHashEncoding = CheckpointContentHashEncodingLosslessJSON
	value.ContentHash = hash
	value.ID = checkpointID(value)
	return value, nil
}

func checkpointID(value Checkpoint) string {
	return stableID("ctxc",
		value.ScopeID, strconv.Itoa(value.Generation), strconv.Itoa(value.Revision),
		value.ParentCheckpointID, value.CacheIdentity, value.LineageHash,
		value.CoveredThroughSourceID, value.CoveredPathHash, value.StaticFingerprint,
		value.ModelWindowFingerprint, value.ContentHash, strings.Join(value.ArtifactIDs, ","),
		value.ContentHashEncoding,
	)
}

func checkpointWindowHash(window Window) (string, error) {
	if ValidateWorkingState(window.WorkingState) != nil {
		return "", ErrInvalidInput
	}
	encoded, err := canonicalLosslessJSON(normalizeWindow(window))
	if err != nil {
		return "", err
	}
	return hashBytes(encoded), nil
}

func canonicalLosslessJSON(value any) ([]byte, error) {
	if jsoncontract.ValidateCanonicalUTF8(value) != nil {
		return nil, ErrInvalidInput
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, errors.Join(ErrInvalidInput, err)
	}
	return canonicalArchiveJSON(encoded)
}
