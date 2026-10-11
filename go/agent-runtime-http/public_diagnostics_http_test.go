package http

import (
	"encoding/json"
	stdhttp "net/http"
	"strings"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

func TestOwnerRunHTTPDoesNotReturnRawFailureDetailAcrossRoutes(t *testing.T) {
	t.Parallel()
	actor := kernel.ActorRef{TenantID: "tenant-p2", ActorID: "owner-p2"}
	engine, runtime := newAuthorizationHTTPTest(t, actor, nil)
	createAuthorizationRun(t, runtime, actor, "owner-failed-run")
	created, err := runtime.Load(t.Context(), "owner-failed-run")
	if err != nil {
		t.Fatal(err)
	}
	_, err = runtime.Apply(t.Context(), created.Run.ID, created.Run.Revision, kernel.Mutation{
		Status: kernel.RunStatusFailed, State: json.RawMessage(`{"validUserState":"keep"}`),
		ErrorCode: "model.upstream_quota_exhausted", ErrorDetail: privateDiagnosticFixture,
		Events: []kernel.EventDraft{{
			Type: "agent.failed", Message: privateDiagnosticFixture,
			Data: json.RawMessage(`{"errorDetail":"Bearer sk-PRIVATE-PROVIDER-REQUEST-BODY","valid":42}`),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}

	for _, endpoint := range []string{
		"/api/v1/runs/owner-failed-run",
		"/api/v1/runs/owner-failed-run/events",
		"/api/v1/runs/owner-failed-run/workbench",
	} {
		res := performAuthorizationRequest(t, engine, stdhttp.MethodGet, endpoint, nil)
		if res.Code != stdhttp.StatusOK || strings.Contains(res.Body.String(), privateDiagnosticFixture) {
			t.Fatalf("%s leaked raw error or denied owner: %d %s", endpoint, res.Code, res.Body.String())
		}
	}
	snapshot, err := runtime.Load(t.Context(), created.Run.ID)
	if err != nil || snapshot.Run.ErrorDetail != privateDiagnosticFixture {
		t.Fatalf("public read changed the internal failure record: %#v %v", snapshot.Run, err)
	}
}
