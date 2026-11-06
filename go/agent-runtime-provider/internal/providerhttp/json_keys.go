package providerhttp

import "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/jsoncontract"

// Keep transport error classification unchanged while sharing one strict
// JSON-member decoder with the trusted Harness ModelOptions freeze boundary.
func ValidateUniqueJSONKeys(data []byte) error {
	if jsoncontract.ValidateUniqueJSONKeys(data) != nil {
		return ErrProtocol
	}
	return nil
}

// Provider Tool argument roots must remain non-null JSON objects.
func ValidateUniqueJSONObject(data []byte) error {
	if jsoncontract.ValidateUniqueJSONObject(data) != nil {
		return ErrProtocol
	}
	return nil
}
