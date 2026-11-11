package harness_test

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// Test-only derivation of first-party deterministic Harness completion IDs.
// External Store fixtures must mint the same immutable receipt identity as
// the production emitter; a convenient arbitrary Completed Item is not proof.
func testAuthorityReceiptID(prefix, originalItemID string) string {
	raw := []byte(strings.TrimSpace(originalItemID) + "\x00")
	sum := sha256.Sum256(raw)
	return prefix + "_" + hex.EncodeToString(sum[:])[:24]
}
