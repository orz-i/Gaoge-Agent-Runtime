package providerhttp

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

// Previous beta.24 layout (unreleased pending intents may still exist in
// developer Stores). No compatibility bridge is retained: old identity must
// differ from this new explicitly model-scoped authority.
type previousBindingSpec struct {
	Protocol               string
	Endpoint               string
	DefaultModel           string
	BindingScope           string
	MaxOutputTokens        int64
	StructuredOutputModels []string
	EffortModelLevels      map[string][]string `json:",omitempty"`
}

func previousBindingHMAC(key string, input previousBindingSpec) string {
	input.StructuredOutputModels = slices.Clone(input.StructuredOutputModels)
	slices.Sort(input.StructuredOutputModels)
	if len(input.EffortModelLevels) != 0 {
		copyLevels := make(map[string][]string, len(input.EffortModelLevels))
		for id, levels := range input.EffortModelLevels {
			copyLevels[id] = slices.Clone(levels)
			slices.Sort(copyLevels[id])
		}
		input.EffortModelLevels = copyLevels
	}
	raw, _ := json.Marshal(input)
	mac := hmac.New(sha256.New, []byte(key))
	_, _ = mac.Write([]byte("gaoge-provider-binding.v1\x00"))
	_, _ = mac.Write(raw)
	return hex.EncodeToString(mac.Sum(nil))
}

func TestModelProfileFingerprintScopesOnlySelectedModelAndNoLegacyBridge(t *testing.T) {
	const key = "private-fixture-key"
	for _, protocol := range []string{"openai.responses.v1", "anthropic.messages.2023-06-01"} {
		base := BindingSpec{
			Protocol: protocol, Endpoint: "https://provider.example/v1", DefaultModel: "model", MaxOutputTokens: 4096,
		}
		previous := previousBindingSpec{
			Protocol: protocol, Endpoint: base.Endpoint, DefaultModel: base.DefaultModel, MaxOutputTokens: base.MaxOutputTokens,
		}
		root := FingerprintBinding(key, base)
		unknown := FingerprintModelBinding(root, "model-a", model.Capabilities{}, false)
		if unknown == previousBindingHMAC(key, previous) || len(unknown) != 64 {
			t.Fatalf("new model-scoped authority reused beta.24 identity: %s", protocol)
		}
		original := model.Capabilities{
			StructuredJSONSchema: true, EffortLevels: []string{"high", "low"},
			SupportsSamplingWhenEffortOmitted: true,
		}
		same := model.Capabilities{
			StructuredJSONSchema: true, EffortLevels: []string{"low", "high"},
			SupportsSamplingWhenEffortOmitted: true,
		}
		exact := FingerprintModelBinding(root, "model-a", original, true)
		if protocol == "openai.responses.v1" &&
			exact != "586e81772670e81158a97352ac2759532baf923fae6089e96d7af0624ae0ca75" {
			t.Fatal("adding absent media grants changed the original text-only persisted model binding")
		}
		if exact == unknown || exact != FingerprintModelBinding(root, "model-a", same, true) {
			t.Fatal("selected model identity not bound canonically")
		}
		for _, changed := range []model.Capabilities{
			{StructuredJSONSchema: true, EffortLevels: []string{"low", "high"}},
			{EffortLevels: []string{"low", "high"}, SupportsSamplingWhenEffortOmitted: true},
			{StructuredJSONSchema: true, EffortLevels: []string{"medium"}, SupportsSamplingWhenEffortOmitted: true},
		} {
			if exact == FingerprintModelBinding(root, "model-a", changed, true) {
				t.Fatalf("selected model's capability change did not revoke pending execution grant: %+v", changed)
			}
		}
		for _, altered := range []model.Capabilities{
			func() model.Capabilities { next := original; next.SupportsImageInput = true; return next }(),
			func() model.Capabilities { next := original; next.SupportsPDFInput = true; return next }(),
		} {
			if exact == FingerprintModelBinding(root, "model-a", altered, true) {
				t.Fatal("media capability grant changed without rotating selected model binding")
			}
		}
		if exact == FingerprintModelBinding(root, "model-b", original, true) ||
			FingerprintModelBinding(root, "model-a", original, false) == exact {
			t.Fatal("changed original request model or certification did not rotate fingerprint")
		}
		modifiedTransport := base
		modifiedTransport.BindingScope = "rotated-host-v2"
		if exact == FingerprintModelBinding(FingerprintBinding(key, modifiedTransport), "model-a", original, true) ||
			exact == FingerprintModelBinding(FingerprintBinding(key+"rotated", base), "model-a", original, true) {
			t.Fatal("credential/transport drift did not invalidate original Provider identity")
		}
	}
}
