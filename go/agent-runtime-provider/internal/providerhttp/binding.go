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
}

// FingerprintBinding binds a Provider's effective endpoint, authenticated
// identity, protocol and selected Host policy without persisting secrets.
// HMAC resists offline guessing of low-entropy private endpoint URLs.
func FingerprintBinding(apiKey string, binding BindingSpec) string {
	binding.StructuredOutputModels = slices.Clone(binding.StructuredOutputModels)
	slices.Sort(binding.StructuredOutputModels)
	material, _ := json.Marshal(binding) // Only strings, integers and string lists.
	mac := hmac.New(sha256.New, []byte(apiKey))
	_, _ = mac.Write([]byte("gaoge-provider-binding.v1\x00"))
	_, _ = mac.Write(material)
	return hex.EncodeToString(mac.Sum(nil))
}
