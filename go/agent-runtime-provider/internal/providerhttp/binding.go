package providerhttp

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

// BindingSpec freezes provider-wide transport/authentication identity, not
// a mutable Host model catalog. Unrelated model catalog changes must not
// block recovery of a previously committed invocation for another model.
type BindingSpec struct {
	Protocol        string
	Endpoint        string
	DefaultModel    string
	BindingScope    string
	MaxOutputTokens int64
}

// FingerprintBinding creates a nonsecret stable HMAC of the underlying
// Provider endpoint, credentials, protocol and explicit transport policy.
// HMAC resists offline guessing of private endpoint URLs and credentials.
func FingerprintBinding(apiKey string, binding BindingSpec) string {
	material, _ := json.Marshal(binding)
	mac := hmac.New(sha256.New, []byte(apiKey))
	_, _ = mac.Write([]byte("gaoge-provider-binding.v1\x00"))
	_, _ = mac.Write(material)
	return hex.EncodeToString(mac.Sum(nil))
}

// FingerprintModelBinding binds exactly the selected model's immutable Host
// profile on top of the trusted Provider fingerprint. A catalog update that
// only adds/changes unrelated models does NOT revoke an existing pending
// ModelInvocation. A changed selected profile, transport, credential or
// Host binding does revoke its original durable execution grant.
//
// The model-specific HMAC is derived from the Provider HMAC, never from
// public catalog data alone. Its output is safe to persist and compare.
func FingerprintModelBinding(base, modelID string, profile model.Capabilities, certified bool) string {
	if base == "" {
		return ""
	}
	profile.EffortLevels = slices.Clone(profile.EffortLevels)
	slices.Sort(profile.EffortLevels)
	if len(profile.EffortLevels) == 0 {
		profile.EffortLevels = nil
	}
	material, _ := json.Marshal(struct {
		ModelID  string
		Present  bool
		Features model.Capabilities
	}{ModelID: modelID, Present: certified, Features: profile})
	mac := hmac.New(sha256.New, []byte(base))
	_, _ = mac.Write([]byte("gaoge-model-binding.v1\x00"))
	_, _ = mac.Write(material)
	return hex.EncodeToString(mac.Sum(nil))
}
