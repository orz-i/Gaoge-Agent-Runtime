package harness_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"testing"
	"time"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
)

func TestModelOutputCeilingSealsConfigAndPreservesLegacyZeroPayload(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)
	legacy, err := harness.SealConfigSnapshot("output-limit-turn", harness.ConfigSnapshot{Model: "fixture"}, now)
	if err != nil {
		t.Fatal(err)
	}
	// Frozen beta.24 payload shape before the per-call output field was added.
	const previousPayload = `{"turnID":"output-limit-turn","environment":{"id":"","revision":0},"model":"fixture","toolKeys":[],"toolPolicies":[],"skills":[],"contextPolicy":{"softLimitPercent":80,"estimateSafetyPercent":15,"maxSerializedBytes":4194304,"preserveRecentTurns":8,"minimumRecentTurns":1,"maxCompactionTokens":1024,"maxToolResultBytes":2048},"approvalPolicyVersion":0,"limits":{},"delegationPolicy":{}}`
	sum := sha256.Sum256([]byte(previousPayload))
	if legacy.ContentHash != hex.EncodeToString(sum[:]) {
		t.Fatalf("legacy omitted output ceiling changed config hash: got=%s", legacy.ContentHash)
	}
	encoded, err := json.Marshal(legacy)
	if err != nil || bytes.Contains(encoded, []byte("maxModelOutputTokens")) {
		t.Fatalf("legacy zero field must remain omitted: %s err=%v", encoded, err)
	}
	limited, err := harness.SealConfigSnapshot("output-limit-turn", harness.ConfigSnapshot{Model: "fixture", MaxModelOutputTokens: 256}, now)
	if err != nil || limited.ID == legacy.ID || limited.ContentHash == legacy.ContentHash {
		t.Fatalf("output ceiling is not frozen into config: %+v err=%v", limited, err)
	}
	changed, err := harness.SealConfigSnapshot("output-limit-turn", harness.ConfigSnapshot{Model: "fixture", MaxModelOutputTokens: 257}, now)
	if err != nil || changed.ID == limited.ID {
		t.Fatalf("different ceiling reused config identity: %+v err=%v", changed, err)
	}
	if _, err = harness.SealConfigSnapshot("output-limit-turn", harness.ConfigSnapshot{MaxModelOutputTokens: -1}, now); !errors.Is(err, harness.ErrInvalidRequest) {
		t.Fatalf("negative output ceiling accepted: %v", err)
	}
}
