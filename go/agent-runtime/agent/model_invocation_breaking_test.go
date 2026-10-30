package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

func TestOldBetaPendingModelRequestHashIsNotAccepted(t *testing.T) {
	request := model.Request{RunID: "prestable", InvocationID: "old-invocation", Model: "model", Messages: []model.Message{{Role: model.RoleUser, Content: "old pending"}}}
	before := model.CloneRequest(request)
	before.InvocationID = ""
	oldJSON, err := json.Marshal(before) // Pre-ADR 0010 request-only fingerprint.
	if err != nil {
		t.Fatal(err)
	}
	oldHash := sha256.Sum256(oldJSON)
	legacy := ModelInvocation{
		ID: request.InvocationID, RunID: request.RunID, SourceRevision: 1,
		RequestHash: hex.EncodeToString(oldHash[:]), Request: request,
		Status: ModelInvocationPending, CreatedAt: time.Now().UTC(),
	}
	if validModelInvocations([]ModelInvocation{legacy}) {
		t.Fatal("an earlier Beta pending ModelInvocation was silently accepted or migrated")
	}
}

// The former direct-Agent compatibility path cleared its lease after a
// retryable failure without persisting RetryAfter. Reject that state rather
// than allowing it to bypass the new single retry-eligibility contract.
func TestPrestableReleasedModelAttemptWithoutReadyAtIsRejected(t *testing.T) {
	request := model.Request{RunID: "released-no-ready-at", InvocationID: "old-retry", Model: "model", Messages: []model.Message{{Role: model.RoleUser, Content: "retry"}}}
	hash, err := modelRequestHash(request, nil, "", "")
	if err != nil {
		t.Fatal(err)
	}
	old := ModelInvocation{ID: request.InvocationID, RunID: request.RunID, SourceRevision: 1,
		RequestHash: hash, Request: request, Status: ModelInvocationPending,
		ExecutionAttempt: 1, CreatedAt: time.Now().UTC()}
	if validModelInvocations([]ModelInvocation{old}) {
		t.Fatal("old direct Agent retry without RetryAfter was silently reenabled")
	}
}

func TestOldNamedPendingModelInvocationMissingBindingFingerprintIsRejected(t *testing.T) {
	request := model.Request{RunID: "old-named", InvocationID: "old-invocation", Model: "openai/gpt-test",
		Messages: []model.Message{{Role: model.RoleUser, Content: "please continue"}}}
	for _, binding := range []string{"", "not-a-sha256", "ABCDEF" + strings.Repeat("a", 58)} {
		hash, err := modelRequestHash(request, nil, "openai", binding)
		if err != nil {
			t.Fatal(err)
		}
		previous := ModelInvocation{
			ID: request.InvocationID, RunID: request.RunID, SourceRevision: 1, RequestHash: hash,
			Provider: "openai", BindingFingerprint: binding, Request: request,
			Status: ModelInvocationPending, CreatedAt: time.Now().UTC(),
		}
		if validModelInvocations([]ModelInvocation{previous}) {
			t.Fatalf("missing or malformed pending Provider binding %q accepted", binding)
		}
	}
}
