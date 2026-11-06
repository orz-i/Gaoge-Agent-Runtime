package providerhttp

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"testing"
)

// The type is intentionally locked to the previous beta.24 wire/HMAC layout.
// Recomputing its digest catches accidental changes to the historical binding
// identity of in-flight ModelInvocations (not just an equivalent new value).
type oldProviderBindingSpec struct {
	Protocol               string
	Endpoint               string
	DefaultModel           string
	BindingScope           string
	MaxOutputTokens        int64
	StructuredOutputModels []string
}

func oldProviderFingerprint(key string, value oldProviderBindingSpec) string {
	value.StructuredOutputModels = slices.Clone(value.StructuredOutputModels)
	slices.Sort(value.StructuredOutputModels)
	raw, _ := json.Marshal(value)
	mac := hmac.New(sha256.New, []byte(key))
	_, _ = mac.Write([]byte("gaoge-provider-binding.v1\x00"))
	_, _ = mac.Write(raw)
	return hex.EncodeToString(mac.Sum(nil))
}

func TestUnconfiguredEffortPreservesOldPersistedProviderBinding(t *testing.T) {
	for _, tc := range []struct {
		name       string
		protocol   string
		endpoint   string
		tokens     int64
		structured []string
	}{
		{"openai-default", "openai.responses.v1", "https://api.openai.com/v1/responses", 0, nil},
		{"openai-schema", "openai.responses.v1", "https://api.openai.com/v1/responses", 0, []string{"gpt-6-sol", "gpt-5.6"}},
		{"anthropic-default", "anthropic.messages.2023-06-01", "https://api.anthropic.com/v1/messages", 4096, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			prior := oldProviderBindingSpec{Protocol: tc.protocol, Endpoint: tc.endpoint,
				DefaultModel: "model", MaxOutputTokens: tc.tokens, StructuredOutputModels: tc.structured}
			current := BindingSpec{Protocol: tc.protocol, Endpoint: tc.endpoint,
				DefaultModel: "model", MaxOutputTokens: tc.tokens, StructuredOutputModels: tc.structured}
			expected := oldProviderFingerprint("private-fixture-key", prior)
			for _, noOverride := range []map[string][]string{nil, {}} {
				current.EffortModelLevels = noOverride
				if actual := FingerprintBinding("private-fixture-key", current); actual != expected {
					t.Fatalf("historical pending invocation cannot resume: original=%s current=%s", expected, actual)
				}
			}
			current.EffortModelLevels = map[string][]string{"private-model": {"high", "low"}}
			changed := FingerprintBinding("private-fixture-key", current)
			if changed == expected {
				t.Fatal("explicit new effort policy failed to rotate binding authority")
			}
			// Certification order is not a meaningful policy change.
			current.EffortModelLevels = map[string][]string{"private-model": {"low", "high"}}
			if changed != FingerprintBinding("private-fixture-key", current) {
				t.Fatal("capability order incorrectly rotates a frozen binding")
			}
		})
	}
}
