package agent

import (
	"strings"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

func TestPersistedModelCompletionIdentitiesAreUnique(t *testing.T) {
	for _, completed := range []bool{false, true} {
		for _, tc := range []struct {
			name, provider, binding, id string
			valid                       bool
		}{
			{"duplicate", "audit", strings.Repeat("a", 64), "physical", false},
			{"different_id", "audit", strings.Repeat("a", 64), "other", true},
			{"different_provider", "other", strings.Repeat("a", 64), "physical", true},
			{"different_binding", "audit", strings.Repeat("b", 64), "physical", true},
			{"unnamed_local", "", "", "physical", true},
		} {
			t.Run(tc.name+map[bool]string{false: "_consumed", true: "_completed"}[completed], func(t *testing.T) {
				now := time.Now().UTC()
				first := ModelInvocation{ID: "first", RunID: "run", SourceRevision: 1, RequestHash: strings.Repeat("c", 64), CreatedAt: now, CompletedAt: &now, ConsumedAt: &now, Status: ModelInvocationConsumed,
					Provider: "audit", BindingFingerprint: strings.Repeat("a", 64), ProviderResponseID: "physical", Response: model.Response{ResponseID: "physical", Content: "done"}}
				second := first
				second.ID = "second"
				second.Provider, second.BindingFingerprint, second.ProviderResponseID = tc.provider, tc.binding, tc.id
				second.Response.ResponseID = tc.id
				if completed {
					second.Status = ModelInvocationCompleted
					second.ConsumedAt = nil
					second.Request = model.Request{RunID: "run", InvocationID: "second", Messages: []model.Message{{Role: model.RoleUser, Content: model.TextParts("next")}}}
					var err error
					second.RequestHash, err = modelRequestHash(second.Request, nil, second.Provider, second.BindingFingerprint)
					if err != nil {
						t.Fatal(err)
					}
				}
				if got := validModelInvocations([]ModelInvocation{first, second}); got != tc.valid {
					t.Fatalf("accepted=%t want=%t", got, tc.valid)
				}
			})
		}
	}
}
