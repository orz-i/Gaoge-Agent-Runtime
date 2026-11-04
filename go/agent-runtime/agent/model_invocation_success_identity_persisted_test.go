package agent

import (
	"strings"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

// A completed ModelInvocation can survive a process restart. It must not
// bypass the live named-Provider physical identity hard cut on state load.
func TestPersistedNamedProviderCompletionNeedsPhysicalIdentity(t *testing.T) {
	for _, tc := range []struct {
		name, provider, persistedID, responseID string
		valid                                   bool
	}{
		{name: "named_missing_both", provider: "audit"},
		{name: "named_persisted_missing", provider: "audit", responseID: "physical-id"},
		{name: "named_response_missing", provider: "audit", persistedID: "physical-id"},
		{name: "named_normalized_from_padded", provider: "audit", persistedID: "physical-id", responseID: " physical-id "},
		{name: "named_mismatch", provider: "audit", persistedID: "physical-id", responseID: "other-id"},
		{name: "named_valid", provider: "audit", persistedID: "physical-id", responseID: "physical-id", valid: true},
		{name: "unnamed_local_optional", valid: true},
	} {
		for _, rejected := range []bool{false, true} {
			for _, completed := range []bool{false, true} {
				name := tc.name
				if rejected {
					name += "_rejected"
				} else {
					name += "_success"
				}
				if completed {
					name += "_completed"
				} else {
					name += "_consumed"
				}
				t.Run(name, func(t *testing.T) {
					now := time.Now().UTC()
					usage := &model.Usage{InputTokens: 11, OutputTokens: 3}
					invocation := ModelInvocation{
						ID: "inv-1", RunID: "run-1", SourceRevision: 1,
						RequestHash: strings.Repeat("b", 64), CreatedAt: now, CompletedAt: &now, ConsumedAt: &now,
						Provider: tc.provider, ProviderResponseID: tc.persistedID,
						Response: model.Response{ResponseID: tc.responseID, ToolCalls: []tools.Call{{ID: "call", ToolKey: "lookup"}}, Usage: model.CloneUsage(usage)},
						Usage:    model.CloneUsage(usage), Status: ModelInvocationConsumed,
					}
					if tc.provider != "" {
						invocation.BindingFingerprint = strings.Repeat("a", 64)
					}
					if rejected {
						invocation.ErrorCode = "model.post_generation_failed"
						invocation.Response.ToolCalls = nil
					}
					if completed {
						invocation.Status = ModelInvocationCompleted
						invocation.ConsumedAt = nil
						invocation.Request = model.Request{RunID: "run-1", InvocationID: "inv-1", Messages: []model.Message{{Role: model.RoleUser, Content: model.TextParts("hello")}}}
						hash, err := modelRequestHash(invocation.Request, nil, invocation.Provider, invocation.BindingFingerprint)
						if err != nil {
							t.Fatal(err)
						}
						invocation.RequestHash = hash
					}
					if got := validModelInvocations([]ModelInvocation{invocation}); got != tc.valid {
						t.Fatalf("persisted named Provider identity accepted=%t want=%t response=%q stored=%q", got, tc.valid, tc.responseID, tc.persistedID)
					}
				})
			}
		}
	}
}
