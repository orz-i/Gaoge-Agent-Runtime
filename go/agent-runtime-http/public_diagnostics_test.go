package http

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/runfeed"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/workbench"
)

const privateDiagnosticFixture = "Bearer sk-PRIVATE-PROVIDER-REQUEST-BODY"

func TestPublicSDKFailureProjectionKeepsCodesWithoutDiagnosticBytes(t *testing.T) {
	t.Parallel()
	internal := kernel.Run{
		ID: "run-1", Status: kernel.RunStatusFailed,
		ErrorCode: "model.upstream_auth_failed", ErrorDetail: privateDiagnosticFixture,
	}
	public := PublicRun(internal)
	if public.ErrorDetail == internal.ErrorDetail || public.ErrorCode != internal.ErrorCode ||
		!strings.Contains(internal.ErrorDetail, "sk-PRIVATE") {
		t.Fatalf("public Run lost semantics, leaked bytes, or mutated internal state: %#v", public)
	}
	encoded, _ := json.Marshal(snapshotResponse(kernel.Snapshot{
		Run: internal, State: json.RawMessage(`{"userContent":"hello","errorMessage":"Bearer sk-PRIVATE-PROVIDER-REQUEST-BODY"}`),
	}))
	if strings.Contains(string(encoded), privateDiagnosticFixture) || !strings.Contains(string(encoded), "model.upstream_auth_failed") ||
		!strings.Contains(string(encoded), "hello") {
		t.Fatalf("SDK Run snapshot leaked diagnostics or lost valid state: %s", encoded)
	}
	unknown := internal
	unknown.ErrorCode = "provider.header.Authorization.sk-SECRET"
	if projected := PublicRun(unknown); projected.ErrorCode != publicUnknownFailureCode ||
		strings.Contains(projected.ErrorDetail, privateDiagnosticFixture) {
		t.Fatalf("unknown code escaped public projection: %#v", projected)
	}
}

func TestPublicSDKWorkbenchAndFeedExcludeNestedProviderErrors(t *testing.T) {
	t.Parallel()
	secret := privateDiagnosticFixture
	run := kernel.Run{ID: "run-1", Status: kernel.RunStatusFailed,
		ErrorCode: "model.upstream_quota_exhausted", ErrorDetail: secret}
	detail := workbench.Detail{
		Overview: workbench.Overview{
			RunID: run.ID, Status: run.Status, ErrorCode: run.ErrorCode, ErrorDetail: secret,
		},
		Run: run,
		Sections: []workbench.Section{{Name: "model", Available: true,
			Content: json.RawMessage(`{"safe":true,"providerResponse":{"body":"Bearer sk-PRIVATE-PROVIDER-REQUEST-BODY"}}`)}},
		Diagnostics: []workbench.Diagnostic{{Provider: "model", Code: "provider_error", Message: secret}},
		Timeline: []workbench.TimelineItem{{
			ID: "t1", Kind: "agent.failed", Source: "kernel", Title: secret, Summary: secret,
			CreatedAt: time.Now(), Data: json.RawMessage(`{"nested":{"errorDetail":"Bearer sk-PRIVATE-PROVIDER-REQUEST-BODY"},"valid":"keep"}`),
		}},
	}
	public := workbenchResponse(detail)
	encoded, err := json.Marshal(public)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), secret) || !strings.Contains(string(encoded), "model.upstream_quota_exhausted") ||
		!strings.Contains(string(encoded), "keep") || !strings.Contains(string(encoded), "safe") {
		t.Fatalf("workbench leaked internal diagnostics or removed valid facts: %s", encoded)
	}
	if detail.Overview.ErrorDetail != secret || detail.Diagnostics[0].Message != secret {
		t.Fatal("HTTP projection mutated internal workbench detail")
	}
	event := runfeed.Event{
		Type: runfeed.EventRunFailed, Status: "failed", Message: secret,
		Data: json.RawMessage(`{"debug":{"requestBody":"Bearer sk-PRIVATE-PROVIDER-REQUEST-BODY"},"count":2}`),
	}
	projected := publicRunFeedEvent(event)
	encoded, _ = json.Marshal(projected)
	if strings.Contains(string(encoded), secret) || !strings.Contains(string(encoded), "count") {
		t.Fatalf("public runfeed leaked Provider details: %s", encoded)
	}
	if event.Message != secret {
		t.Fatal("SDK public projection modified the persistent feed fact")
	}
}

func TestPublicSDKErrorJournalFailsClosedOnMalformedDiagnostics(t *testing.T) {
	t.Parallel()
	events := []kernel.Event{{
		Type: "agent.failed", Seq: 1, Message: privateDiagnosticFixture,
		Data: json.RawMessage(`{"errorDetail":"Bearer sk-PRIVATE-PROVIDER-REQUEST-BODY"}`),
	}, {
		Type: "model.completed", Seq: 2, Message: "tool result 42",
		Data: json.RawMessage(`{"value":42,"requestBody":"Bearer sk-PRIVATE-PROVIDER-REQUEST-BODY"}`),
	}, {
		Type: "agent.failed", Seq: 3, Message: privateDiagnosticFixture, Data: json.RawMessage("invalid JSON"),
	}}
	public := publicKernelEvents(events)
	raw, _ := json.Marshal(public)
	if strings.Contains(string(raw), privateDiagnosticFixture) || !strings.Contains(string(raw), "tool result 42") ||
		!strings.Contains(string(raw), "value") || len(public[2].Data) != 0 {
		t.Fatalf("public journal leaked error detail or removed legitimate progress: %s", raw)
	}
	if events[0].Message != privateDiagnosticFixture {
		t.Fatal("projecting journal events must not modify durable events")
	}
}
