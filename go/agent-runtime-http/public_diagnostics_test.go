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

func TestPublicContextLimitFailuresExplainRecoveryWithoutDiagnosticBytes(t *testing.T) {
	t.Parallel()
	for _, code := range []string{"context.artifact_payload_too_large", "context.input_budget_exceeded"} {
		public := PublicRun(kernel.Run{ErrorCode: code, ErrorDetail: privateDiagnosticFixture})
		if public.ErrorCode != code || strings.Contains(public.ErrorDetail, privateDiagnosticFixture) ||
			!strings.Contains(public.ErrorDetail, "start a new conversation") {
			t.Fatalf("unsafe or unclear context failure projection: %#v", public)
		}
	}
}

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

func TestPublicSDKStructuredDiagnosticsDoNotForwardNestedHeadersOrGenericErrorObjects(t *testing.T) {
	t.Parallel()
	payload := json.RawMessage(`{"valid":{"answer":"42"},"providerHeaders":{"authorization":"Bearer sk-PRIVATE-PROVIDER-REQUEST-BODY"},"error":{"message":"Bearer sk-PRIVATE-PROVIDER-REQUEST-BODY"},"diagnostics":[{"headers":{"x-key":"Bearer sk-PRIVATE-PROVIDER-REQUEST-BODY"}}]}`)
	projected := PublicDiagnosticPayload(payload)
	if strings.Contains(string(projected), privateDiagnosticFixture) || !strings.Contains(string(projected), "42") {
		t.Fatalf("nested diagnostic fields leaked or normal results were removed: %s", projected)
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

func TestPublicRunFeedRedactsToolArgumentsAndProviderDeltaMetadata(t *testing.T) {
	t.Parallel()
	const secret = "PRIVATE_TEST_TOOL_TOKEN"
	for _, event := range []runfeed.Event{
		{RunID: "child-run", Seq: 1, Type: runfeed.EventToolStarted, Message: secret, Delta: secret, Data: json.RawMessage(`{"id":"call-one","toolKey":"mcp.web_search","name":"web_search","arguments":{"apiKey":"PRIVATE_TEST_TOOL_TOKEN"},"secret":"PRIVATE_TEST_TOOL_TOKEN"}`)},
		{RunID: "child-run", Seq: 2, Type: runfeed.EventToolCompleted, Message: "tool.arguments_schema", Data: json.RawMessage(`{"id":"call-one","toolKey":"mcp.web_search","arguments":{"apiKey":"PRIVATE_TEST_TOOL_TOKEN"}}`)},
		{RunID: "child-run", Seq: 3, Type: runfeed.EventModelDelta, Delta: "Visible answer", Data: json.RawMessage(`{"reasoning":"PRIVATE_TEST_TOOL_TOKEN","usage":42}`)},
		{RunID: "child-run", Seq: 4, Type: runfeed.EventModelCompleted, Data: json.RawMessage(`{"response":{"internal":"PRIVATE_TEST_TOOL_TOKEN"}}`)},
	} {
		projected := publicRunFeedEvent(event)
		encoded, _ := json.Marshal(projected)
		if strings.Contains(string(encoded), secret) || strings.Contains(string(encoded), `"arguments"`) || strings.Contains(string(encoded), `"reasoning"`) {
			t.Fatalf("public feed event %s leaked raw payload: %s", event.Type, encoded)
		}
		if event.Type == runfeed.EventToolStarted && (!strings.Contains(string(encoded), `"toolKey":"mcp.web_search"`) || !strings.Contains(string(encoded), `"status":"running"`)) {
			t.Fatalf("missing safe tool identity: %s", encoded)
		}
		if event.Type == runfeed.EventToolCompleted && !strings.Contains(string(encoded), `"status":"needs_correction"`) {
			t.Fatalf("recoverable tool error was marked successful: %s", encoded)
		}
		if event.Type == runfeed.EventModelDelta && !strings.Contains(string(encoded), `"delta":"Visible answer"`) {
			t.Fatalf("visible model delta removed: %s", encoded)
		}
		if !strings.Contains(string(event.Data), secret) {
			t.Fatal("projection mutated internal Run Feed event")
		}
	}
}

func TestPublicToolFeedProjectionDoesNotForwardUntrustedToolIdentifier(t *testing.T) {
	t.Parallel()
	event := runfeed.Event{RunID: "child", Type: runfeed.EventToolStarted, Data: json.RawMessage(`{"id":"call:secret-key","toolKey":"Bearer PRIVATE_TOOL_SECRET","arguments":{"secret":"PRIVATE_TOOL_SECRET"}}`)}
	projected := publicRunFeedEvent(event)
	if strings.Contains(string(projected.Data), "PRIVATE") || strings.Contains(string(projected.Data), "Bearer") || strings.Contains(string(projected.Data), "secret-key") {
		t.Fatalf("tool identifiers escaped: %s", projected.Data)
	}
	if !strings.Contains(string(projected.Data), `"callID":"call_`) {
		t.Fatalf("opaque stable id absent: %s", projected.Data)
	}
}

func TestPublicRunFeedDiscoveryProgressOnlyAcceptsTrustedEnumeratedFacts(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		data     string
		expected string
	}{
		{`{"code":"tool_discovery.empty","count":0,"secret":"PRIVATE_DISCOVERY_QUERY"}`, "No matching authorized tools found"},
		{`{"code":"tool_discovery.matched","count":2,"searchTerms":"PRIVATE_DISCOVERY_QUERY"}`, "Matched 2 authorized tools"},
		{`{"title":"PRIVATE_DISCOVERY_QUERY","summary":"unsafe"}`, ""},
		{`{"code":"tool_discovery.matched","count":99}`, ""},
	} {
		event := runfeed.Event{RunID: "child", Type: "activity.user", Data: json.RawMessage(tc.data), Message: "PRIVATE_DISCOVERY_QUERY"}
		projected := publicRunFeedEvent(event)
		encoded, _ := json.Marshal(projected)
		if strings.Contains(string(encoded), "PRIVATE_DISCOVERY_QUERY") {
			t.Fatalf("untrusted progress leaked: %s", encoded)
		}
		if tc.expected != "" && !strings.Contains(string(encoded), tc.expected) || tc.expected == "" && len(projected.Data) != 0 {
			t.Fatalf("public progress projection=%s expected=%q", encoded, tc.expected)
		}
	}
}

func TestPublicRunFeedPairsStableToolPhasesWithoutDisclosure(t *testing.T) {
	t.Parallel()
	started := publicRunFeedEvent(runfeed.Event{RunID: "child", Type: runfeed.EventToolStarted, Data: json.RawMessage(`{"id":"local-call-1","toolKey":"mcp.web_search","arguments":{"query":"PRIVATE_SEARCH_TEXT"}}`)})
	finished := publicRunFeedEvent(runfeed.Event{RunID: "child", Type: runfeed.EventToolCompleted, Data: json.RawMessage(`{"callID":"local-call-1","executionID":"executor-receipt-differs","toolKey":"mcp.web_search","disposition":"committed"}`)})
	var start, finish struct{ CallID, ToolKey, Status string }
	if json.Unmarshal(started.Data, &start) != nil || json.Unmarshal(finished.Data, &finish) != nil || start.CallID == "" || start.CallID != finish.CallID || start.ToolKey != "mcp.web_search" || start.ToolKey != finish.ToolKey || start.Status != "running" || finish.Status != "completed" {
		t.Fatalf("started=%s finished=%s", started.Data, finished.Data)
	}
	if strings.Contains(string(started.Data), "PRIVATE_SEARCH_TEXT") || strings.Contains(string(finished.Data), "executor-receipt-differs") {
		t.Fatal("private identifiers escaped public feed")
	}
}

// Every owner-visible lifecycle phase must be covered: hiding only tool.started
// leaves the earlier tool.requested model arguments, and interaction.required
// checkpoint payload, accessible through SSE replay and live delivery.
func TestPublicRunFeedRedactsToolRequestPendingAndApprovalCheckpoint(t *testing.T) {
	t.Parallel()
	const secret = "DUMMY_SECRET_FOR_PUBLIC_FEED_PROBE"
	for _, fixture := range []struct {
		kind           string
		event          runfeed.Event
		expectedStatus string
	}{
		{kind: "requested", event: runfeed.Event{RunID: "child", Type: runfeed.EventToolRequested, Message: secret, Delta: secret,
			Data: json.RawMessage(`{"id":"call-local","toolKey":"mcp.web_search","arguments":{"query":"DUMMY_SECRET_FOR_PUBLIC_FEED_PROBE"},"internal":"DUMMY_SECRET_FOR_PUBLIC_FEED_PROBE"}`)}, expectedStatus: "requested"},
		{kind: "pending", event: runfeed.Event{RunID: "child", Type: "tool.pending", Message: secret, Delta: secret,
			Data: json.RawMessage(`{"executionID":"receipt-1","toolKey":"mcp.web_search","arguments":{"key":"DUMMY_SECRET_FOR_PUBLIC_FEED_PROBE"},"internal":"DUMMY_SECRET_FOR_PUBLIC_FEED_PROBE"}`)}, expectedStatus: "pending"},
		{kind: "approval", event: runfeed.Event{RunID: "child", Type: runfeed.EventInteractionRequired, Message: secret, Delta: secret,
			Data: json.RawMessage(`{"id":"checkpoint-one","kind":"tool_approval","status":"pending","payload":{"toolCallID":"call-local","toolKey":"mcp.web_search","toolName":"Search","arguments":{"apiKey":"DUMMY_SECRET_FOR_PUBLIC_FEED_PROBE"}},"response":{"comment":"DUMMY_SECRET_FOR_PUBLIC_FEED_PROBE"}}`)}, expectedStatus: "pending"},
	} {
		t.Run(fixture.kind, func(t *testing.T) {
			original, _ := json.Marshal(fixture.event)
			projected := publicRunFeedEvent(fixture.event)
			exported, _ := json.Marshal(projected)
			if strings.Contains(string(exported), secret) || strings.Contains(string(exported), `"arguments"`) || strings.Contains(string(exported), `"payload"`) || strings.Contains(string(exported), `"response"`) {
				t.Fatalf("private %s event leaked: %s", fixture.kind, exported)
			}
			if !strings.Contains(string(exported), `"status":"`+fixture.expectedStatus+`"`) || !strings.Contains(string(exported), `"call_`) {
				t.Fatalf("safe %s identity/status missing: %s", fixture.kind, exported)
			}
			if fixture.kind == "approval" && !strings.Contains(string(exported), `"id":"checkpoint-one"`) {
				t.Fatalf("approval checkpoint identity missing: %s", exported)
			}
			after, _ := json.Marshal(fixture.event)
			if string(after) != string(original) {
				t.Fatal("projection changed durable Run Feed content")
			}
		})
	}
}

func TestPublicRunFeedUnknownInteractionAndMalformedPayloadFailClosed(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{
		`{"id":"checkpoint-one","kind":"unknown_extension","status":"pending","payload":{"arguments":{"secret":"DUMMY_SECRET"}}}`,
		`{"id":"checkpoint-one","kind":"tool_approval","status":"pending","payload":{"arguments":{"secret":"DUMMY_SECRET"},"toolCallID":42}}`,
		`{"id":"DUMMY_SECRET checkpoint","kind":"tool_approval","status":"pending","payload":{"toolCallID":"call-one"}}`,
		`not json`,
	} {
		event := runfeed.Event{RunID: "child", Type: runfeed.EventInteractionRequired, Message: "DUMMY_SECRET", Data: json.RawMessage(raw)}
		projected := publicRunFeedEvent(event)
		if len(projected.Data) > 0 || projected.Message != "" || projected.Delta != "" {
			t.Fatalf("untrusted approval data escaped: %s", projected.Data)
		}
	}
}
