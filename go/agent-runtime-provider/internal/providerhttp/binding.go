package providerhttp

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
)

// BindingSpec contains only stable effective wire-routing and Host policy
// facts. Neither the API key nor the concrete HTTP transport is serialized.
type BindingSpec struct {
	Protocol               string
	Endpoint               string
	DefaultModel           string
	BindingScope           string
	MaxOutputTokens        int64
	StructuredOutputModels []string
	// Exact Host-certified effort levels for private/gateway model IDs.
	EffortModelLevels map[string][]string `json:",omitempty"`
}

// FingerprintBinding binds a Provider's effective endpoint, authenticated
// identity, protocol and selected Host policy without persisting secrets.
// HMAC resists offline guessing of low-entropy private endpoint URLs.
func FingerprintBinding(apiKey string, binding BindingSpec) string {
	binding.StructuredOutputModels = slices.Clone(binding.StructuredOutputModels)
	slices.Sort(binding.StructuredOutputModels)
	if len(binding.EffortModelLevels) != 0 {
		copyLevels := make(map[string][]string, len(binding.EffortModelLevels))
		for id, levels := range binding.EffortModelLevels {
			copyLevels[id] = slices.Clone(levels)
			slices.Sort(copyLevels[id])
		}
		binding.EffortModelLevels = copyLevels
	}
	material, _ := json.Marshal(binding) // Stable encoding; no raw credentials or URLs leave HMAC.
	mac := hmac.New(sha256.New, []byte(apiKey))
	_, _ = mac.Write([]byte("gaoge-provider-binding.v1\x00"))
	_, _ = mac.Write(material)
	return hex.EncodeToString(mac.Sum(nil))
}
