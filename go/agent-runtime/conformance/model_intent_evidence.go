package conformance

import (
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/jsoncontract"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

// Reconstruct the independently observed business-intent tuple, not a
// persisted hash supplied by the implementation under test.
func ea1ModelIntentEvidenceHash(request model.Request, fingerprints []string, provider, binding string) (string, error) {
	proof, err := jsoncontract.SealOpaque("agent-model-intent-opaque-v2", struct {
		Request             model.Request `json:"request"`
		CatalogFingerprints []string      `json:"catalogFingerprints"`
		Provider            string        `json:"provider"`
		BindingFingerprint  string        `json:"bindingFingerprint"`
	}{request, fingerprints, provider, binding})
	return proof.Digest, err
}
