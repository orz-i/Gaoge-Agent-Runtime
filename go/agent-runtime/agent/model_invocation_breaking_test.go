package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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
