package http

import (
	"testing"

	model "github.com/orz-i/Gaoge/sdk/go/agent-runtime/domain"
)

func TestRunResponseUsesNeutralThreadProjection(t *testing.T) {
	response := toRunResponse(model.Run{RunID: "run_1", Actor: model.ActorRef{TenantID: "tenant", ActorID: "42"}, Thread: model.ThreadRef{Kind: "host.thread", ID: "thread_public"}})
	thread, ok := response[valueThread].(map[string]string)
	if !ok || thread[valueKind72883EFB] != "host.thread" || thread["id"] != "thread_public" {
		t.Fatalf("thread projection = %#v", response[valueThread])
	}
	actor, ok := response["actor"].(map[string]string)
	if !ok || actor["id"] != "42" {
		t.Fatalf("actor projection = %#v", response["actor"])
	}
	if _, leaked := response["conversationID"]; leaked {
		t.Fatal("Run-first DTO leaked host numeric ID")
	}
	if response["schemaVersion"] != 1 {
		t.Fatalf("schemaVersion = %#v", response["schemaVersion"])
	}
}
