package context_test

import (
	"errors"
	"testing"

	runtimecontext "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/context"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

// A pure Context Manager must preserve the same text/identity guarantee
// even when used without a Harness Session or Store.
func TestCanonicalUTF8ContextOpenRejectsMalformedSourceBeforeCheckpoint(t *testing.T) {
	manager := runtimecontext.NewManager(runtimecontext.Dependencies{})
	tests := []struct {
		name string
		edit func(*runtimecontext.OpenRequest)
	}{
		{"scope", func(r *runtimecontext.OpenRequest) { r.ScopeID = "bad-\xff" }},
		{"fingerprint", func(r *runtimecontext.OpenRequest) { r.StaticFingerprint = "bad-\xfe" }},
		{"instructions", func(r *runtimecontext.OpenRequest) { r.Instructions = "bad-\xff" }},
		{"source-path", func(r *runtimecontext.OpenRequest) {
			r.SourcePath[0], r.Entries[0].SourceID = "bad-\xfe", "bad-\xfe"
		}},
		{"message-reference", func(r *runtimecontext.OpenRequest) { r.Entries[0].ID = "bad-\xff" }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			entry := runtimecontext.Entry{ID: "prior/user", SourceID: "prior/user",
				Message: model.Message{Role: model.RoleUser, Content: model.TextParts("hello")}}
			req := runtimecontext.OpenRequest{ScopeID: "scope", StaticFingerprint: "fingerprint",
				SourcePath: []string{entry.SourceID}, Entries: []runtimecontext.Entry{entry}}
			tc.edit(&req)
			if _, err := manager.Open(t.Context(), req); !errors.Is(err, runtimecontext.ErrInvalidInput) {
				t.Fatalf("%s: malformed Context accepted: %v", tc.name, err)
			}
		})
	}
}
