package model

import "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/jsoncontract"

// PhysicalRequestHash binds the complete typed SDK request, including exact
// opaque Tool/Provider bytes, not the token-count or provider-wire encoding.
// It is also used for the original logical request of a budget reservation.
func PhysicalRequestHash(request Request) (string, error) {
	proof, err := jsoncontract.SealOpaque("model-request-opaque-v2", CloneRequest(request))
	return proof.Digest, err
}

// ResponseEvidenceHash compares original response evidence, normalizing only
// ordinary SDK nil/empty collection representation, never raw JSON bytes.
func ResponseEvidenceHash(response Response) (string, error) {
	proof, err := jsoncontract.SealOpaque("model-response-opaque-v2", CloneResponse(response))
	return proof.Digest, err
}
