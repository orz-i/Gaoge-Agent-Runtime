package http

import (
	"encoding/json"
	"testing"
)

func TestWorkflowDraftOpenAPIRejectsUnsupportedTopLevelExtensions(t *testing.T) {
	validator := wireSchema(t, "WorkflowDefinitionDraft")
	base := `{"id":"example","revision":0,"name":"Example","nodes":[{"id":"done","type":"return","return":{"value":true}}]}`
	if err := validator.Validate([]byte(base)); err != nil {
		t.Fatalf("valid Workflow draft rejected by OpenAPI: %v", err)
	}
	unsupported := `{"id":"example","revision":0,"name":"Example","nodes":[{"id":"done","type":"return","return":{"value":true}}],"unrecognizedExtension":true}`
	if err := validator.Validate([]byte(unsupported)); err == nil {
		t.Fatal("OpenAPI advertises unsupported top-level Workflow draft extension")
	}
}

func TestOpenAPIRejectsEmptyRequiredRequestStrings(t *testing.T) {
	for _, testCase := range []struct {
		schema, body string
	}{
		{"ThreadRequest", `{"kind":"","id":"thread"}`},
		{"ThreadRequest", `{"kind":"conversation","id":""}`},
		{"TextInputRequest", `{"content":""}`},
		{"StartWorkflowRunRequest", `{"thread":{"kind":"conversation","id":"thread"},"input":{},"goal":"","definitionReference":{"id":"valid"}}`},
		{"TeamMemberRequest", `{"id":"","goal":"hi"}`},
		{"StartTeamRunRequest", `{"thread":{"kind":"conversation","id":"thread"},"goal":"","mode":"parallel","members":[{"id":"member","goal":"hi"}],"join":{"mode":"all"}}`},
	} {
		if err := wireSchema(t, testCase.schema).Validate([]byte(testCase.body)); err == nil {
			t.Errorf("OpenAPI %s unexpectedly allows empty required string in %s", testCase.schema, testCase.body)
		}
	}
}

func TestOpenAPIAllowanceGrantAndPausedStatuses(t *testing.T) {
	validate := wireSchema(t, "GrantHarnessCallAllowanceRequest")
	for _, value := range []string{
		`{"expectedTurnRevision":2,"modelCalls":1}`,
		`{"expectedTurnRevision":2,"toolCalls":2}`,
	} {
		if err := validate.Validate([]byte(value)); err != nil {
			t.Fatalf("valid allowance %s rejected: %v", value, err)
		}
	}
	for _, value := range []string{
		`{}`, `{"expectedTurnRevision":2}`,
		`{"expectedTurnRevision":0,"modelCalls":1}`,
		`{"expectedTurnRevision":2,"modelCalls":0}`,
		`{"expectedTurnRevision":2,"toolCalls":-1}`,
		`{"expectedTurnRevision":2,"modelCalls":1,"absoluteMaxCalls":999}`,
	} {
		if err := validate.Validate([]byte(value)); err == nil {
			t.Fatalf("invalid allowance %s accepted", value)
		}
	}
	pausedRun := map[string]any{
		"id": "run-1", "kind": "agent",
		"actor":  map[string]any{"tenantID": "tenant", "actorID": "user"},
		"thread": map[string]any{"kind": "conversation", "id": "thread-1"},
		"goal":   "continue", "status": "paused_budget", "revision": float64(5),
		"errorCode": "agent.model_allowance_exhausted",
		"createdAt": "2026-10-09T00:00:00Z", "updatedAt": "2026-10-09T00:00:05Z",
	}
	pausedTurn := map[string]any{
		"id": "ht-1", "hostTurn": map[string]any{"kind": "conversation_turn", "id": "turn-1"},
		"status": "paused_budget", "revision": float64(3),
		"createdAt": "2026-10-09T00:00:00Z", "updatedAt": "2026-10-09T00:00:05Z",
	}
	pausedInvocation := map[string]any{
		"id": "inv-1", "turnID": "ht-1", "capabilityKey": "runtime.agent",
		"executionClass": "agent", "status": "paused_budget",
		"attempt": float64(1), "outputRefs": []any{}, "revision": float64(3),
		"createdAt": "2026-10-09T00:00:00Z", "updatedAt": "2026-10-09T00:00:05Z",
	}
	pausedFeed := map[string]any{
		"seq": float64(3), "turnID": "ht-1", "type": "turn.paused_budget",
		"status": "paused_budget", "createdAt": "2026-10-09T00:00:05Z",
	}
	for _, check := range []struct {
		schema string
		data   any
	}{
		{"Run", pausedRun}, {"HarnessTurn", pausedTurn},
		{"HarnessCapabilityInvocation", pausedInvocation},
		{"HarnessTurnFeedEvent", pausedFeed},
	} {
		raw, err := json.Marshal(check.data)
		if err != nil {
			t.Fatal(err)
		}
		if err = wireSchema(t, check.schema).Validate(raw); err != nil {
			t.Fatalf("paused %s violates OpenAPI: %v", check.schema, err)
		}
	}
}
