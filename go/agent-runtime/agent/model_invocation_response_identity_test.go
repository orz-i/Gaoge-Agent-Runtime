package agent

import "testing"

// The physical completion-ID fence is scoped to one named Provider binding,
// one Run, and *distinct completed invocations*. It must not reject a retry
// of the same pending invocation or unrelated Providers/Host bindings.
func TestDuplicatePhysicalProviderResponseIDScope(t *testing.T) {
	previous := ModelInvocation{
		ID: "inv-1", Provider: "openai", BindingFingerprint: "binding-v1",
		ProviderResponseID: "resp-physical", Status: ModelInvocationConsumed,
	}
	current := ModelInvocation{
		ID: "inv-2", Provider: "openai", BindingFingerprint: "binding-v1",
		Status: ModelInvocationPending,
	}
	for _, tc := range []struct {
		name      string
		old, now  ModelInvocation
		id        string
		duplicate bool
	}{
		{name: "same_binding_different_invocation", old: previous, now: current,
			id: "resp-physical", duplicate: true},
		{name: "completed_before_consumed", old: func() ModelInvocation {
			p := previous
			p.Status = ModelInvocationCompleted
			return p
		}(), now: current, id: "resp-physical", duplicate: true},
		{name: "same_invocation_retry", old: previous, now: func() ModelInvocation {
			v := current
			v.ID = "inv-1"
			return v
		}(), id: "resp-physical"},
		{name: "previous_pending", old: func() ModelInvocation {
			p := previous
			p.Status = ModelInvocationPending
			return p
		}(), now: current, id: "resp-physical"},
		{name: "different_provider", old: previous, now: func() ModelInvocation {
			v := current
			v.Provider = "anthropic"
			return v
		}(), id: "resp-physical"},
		{name: "different_binding", old: previous, now: func() ModelInvocation {
			v := current
			v.BindingFingerprint = "binding-v2"
			return v
		}(), id: "resp-physical"},
		{name: "unnamed_local_model", old: previous, now: func() ModelInvocation {
			v := current
			v.Provider = ""
			return v
		}(), id: "resp-physical"},
		{name: "unverified_binding", old: previous, now: func() ModelInvocation {
			v := current
			v.BindingFingerprint = ""
			return v
		}(), id: "resp-physical"},
		{name: "no_physical_response_id", old: previous, now: current, id: ""},
		{name: "different_physical_response", old: previous, now: current, id: "new-id"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := runState{ModelInvocations: []ModelInvocation{tc.old, tc.now}}
			if got := duplicatePhysicalProviderResponseID(state, tc.now, tc.id); got != tc.duplicate {
				t.Fatalf("duplicate=%t, want %t", got, tc.duplicate)
			}
		})
	}
}
