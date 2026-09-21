package http

import (
	"encoding/json"
	stdhttp "net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/jsoncontract"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/runfeed"
	"gopkg.in/yaml.v3"
)

type wireFixture struct {
	File   string `json:"file"`
	Schema string `json:"schema"`
}

// These reviewed JSON files are also consumed by the packed TypeScript gate.
// Never regenerate them implicitly from changed Go structs: a wire change must
// be visible in the fixture diff and receive an upgrade note.
func TestHTTPWireFixturesMatchGoAndOpenAPI(t *testing.T) {
	var fixtures []wireFixture
	if err := json.Unmarshal(readWireContract(t, "fixtures", "manifest.json"), &fixtures); err != nil {
		t.Fatal(err)
	}
	bodies := wireFixtureBodies(t)
	if len(fixtures) != len(bodies) {
		t.Fatalf("fixture manifest has %d entries, Go produces %d", len(fixtures), len(bodies))
	}
	seen := make(map[string]bool)
	for _, fixture := range fixtures {
		t.Run(fixture.File, func(t *testing.T) {
			body, ok := bodies[fixture.File]
			if !ok || seen[fixture.File] {
				t.Fatalf("missing or duplicate Go fixture: %s", fixture.File)
			}
			seen[fixture.File] = true
			recorder := httptest.NewRecorder()
			context, _ := gin.CreateTestContext(recorder)
			WriteSuccess(context, body)
			actual := recorder.Body.Bytes()
			expected := readWireContract(t, "fixtures", fixture.File)
			var actualJSON, expectedJSON any
			if err := json.Unmarshal(actual, &actualJSON); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(expected, &expectedJSON); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(actualJSON, expectedJSON) {
				t.Fatalf("Go wire response drifted from reviewed fixture\nactual: %s\nfixture: %s", actual, expected)
			}
			if err := wireSchema(t, fixture.Schema).Validate(actual); err != nil {
				t.Fatalf("Go wire response violates OpenAPI %s: %v", fixture.Schema, err)
			}
		})
	}
}

func TestOpenAPIRunSnapshotRejectsContractViolations(t *testing.T) {
	validator := wireSchema(t, "RunSnapshot")
	cases := map[string]func(map[string]any){
		"string revision":           func(value map[string]any) { value["run"].(map[string]any)["revision"] = "3" },
		"missing result content":    func(value map[string]any) { delete(value["result"].(map[string]any), "content") },
		"invalid checkpoint status": func(value map[string]any) { value["checkpoint"].(map[string]any)["status"] = "unknown" },
		"unknown envelope field":    func(value map[string]any) { value["journal"] = []any{} },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			var value map[string]any
			if err := json.Unmarshal(readWireContract(t, "fixtures", "snapshot-completed.json"), &value); err != nil {
				t.Fatal(err)
			}
			mutate(value)
			raw, err := json.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			if err = validator.Validate(raw); err == nil {
				t.Fatal("OpenAPI accepted a contract violation")
			}
		})
	}
}

func TestOpenAPIRunSnapshotAllowsHostKind(t *testing.T) {
	var value map[string]any
	if err := json.Unmarshal(readWireContract(t, "fixtures", "snapshot-extension.json"), &value); err != nil {
		t.Fatal(err)
	}
	value["run"].(map[string]any)["kind"] = "host.custom_feature"
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err = wireSchema(t, "RunSnapshot").Validate(raw); err != nil {
		t.Fatalf("OpenAPI rejected an extensible Kernel Run kind: %v", err)
	}
}

func wireFixtureBodies(t *testing.T) map[string]any {
	t.Helper()
	created := time.Date(2026, time.September, 21, 0, 0, 0, 0, time.UTC)
	ended := created.Add(time.Minute)
	deadline := created.Add(time.Hour)
	run := kernel.Run{
		ID: "run-1", Kind: "agent", Actor: kernel.ActorRef{TenantID: "tenant", ActorID: "actor"},
		Thread: kernel.ThreadRef{Kind: "conversation", ID: "thread-1"}, Goal: "Say hello",
		Status: kernel.RunStatusRunning, Revision: 1, CreatedAt: created, UpdatedAt: created,
	}
	waiting, completed, extension, cancelled := run, run, run, run
	waiting.Status, waiting.Revision = kernel.RunStatusWaitingInput, 2
	completed.Status, completed.Revision = kernel.RunStatusCompleted, 3
	completed.RequestID, completed.DeadlineAt = "request-1", &deadline
	completed.EndedAt, completed.UpdatedAt = &ended, ended
	extension.Kind, extension.Status, extension.Revision = "a2a.remote", kernel.RunStatusCompleted, 2
	extension.EndedAt, extension.UpdatedAt = &ended, ended
	cancelled.Status, cancelled.Revision = kernel.RunStatusCancelled, 2
	cancelled.EndedAt, cancelled.UpdatedAt = &ended, ended
	cancelled.ErrorCode, cancelled.ErrorDetail = "run.cancelled", "stop"
	checkpoint := kernel.Checkpoint{
		ID: "checkpoint-1", Kind: "approval", Status: kernel.CheckpointPending,
		Payload: json.RawMessage(`{"prompt":"Approve"}`), CreatedAt: created,
	}
	resolved := checkpoint
	resolved.Status, resolved.Response, resolved.ResolvedAt = kernel.CheckpointResolved, json.RawMessage(`{"approved":true}`), &ended
	errorRecorder := httptest.NewRecorder()
	errorContext, _ := gin.CreateTestContext(errorRecorder)
	errorContext.Set(requestIDContextKey, "request-1")
	WriteKernelError(errorContext, "run", kernel.ErrConflict)
	if errorRecorder.Code != stdhttp.StatusConflict {
		t.Fatalf("error response status = %d", errorRecorder.Code)
	}
	return map[string]any{
		"snapshot-running.json": SnapshotResponse(kernel.Snapshot{Run: run, State: json.RawMessage(`{}`)}),
		"snapshot-waiting.json": SnapshotResponse(kernel.Snapshot{
			Run: waiting, State: json.RawMessage(`{"step":1}`), Checkpoint: &checkpoint, EventHead: 2,
		}),
		"snapshot-completed.json": SnapshotResponse(kernel.Snapshot{
			Run: completed, State: json.RawMessage(`{"step":2}`), Checkpoint: &resolved,
			Result: &kernel.Result{ContentType: "text", Content: json.RawMessage(`"done"`)}, EventHead: 3,
		}),
		"snapshot-extension.json": SnapshotResponse(kernel.Snapshot{
			Run: extension, State: json.RawMessage(`{"taskID":"remote-1"}`),
			Result: &kernel.Result{ContentType: "application/json", Content: json.RawMessage(`{"remoteTaskID":"remote-1"}`)}, EventHead: 2,
		}),
		"events.json": RunEventPageResponse{Events: []kernel.Event{
			{Seq: 1, Type: "run.created", CreatedAt: created},
			{Seq: 2, Type: "run.waiting", Message: "Approval required", Data: json.RawMessage(`{"checkpointID":"checkpoint-1"}`), CreatedAt: created},
		}, EventHead: 2},
		"events-empty.json": RunEventPageResponse{Events: []kernel.Event{}},
		"cancel.json":       CancelRunResponse{Run: cancelled},
		"error.json":        json.RawMessage(errorRecorder.Body.Bytes()),
		"feed-terminal.json": runfeed.Event{
			Seq: 3, RunID: "run-1", Type: "run.completed", Delta: "done", Message: "Run completed",
			Data: json.RawMessage(`{"contentType":"text"}`), Revision: 3, Status: "completed", Terminal: true, CreatedAt: ended,
		},
	}
}

func readWireContract(t *testing.T, parts ...string) []byte {
	t.Helper()
	path := filepath.Join(append([]string{"..", "..", "contracts", "agent-runtime", "v1"}, parts...)...)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func wireSchema(t *testing.T, name string) *jsoncontract.Validator {
	t.Helper()
	var document struct {
		Components struct {
			Schemas map[string]any `yaml:"schemas"`
		} `yaml:"components"`
	}
	if err := yaml.Unmarshal(readWireContract(t, "openapi.yaml"), &document); err != nil {
		t.Fatal(err)
	}
	// Only translate the OpenAPI 3.0 nullable keyword. Internal references and
	// all field constraints stay sourced from the canonical OpenAPI document.
	raw, err := json.Marshal(map[string]any{
		"$ref":       "#/components/schemas/" + name,
		"components": map[string]any{"schemas": nullableJSONSchema(document.Components.Schemas)},
	})
	if err != nil {
		t.Fatal(err)
	}
	validator, err := jsoncontract.Compile(raw)
	if err != nil {
		t.Fatal(err)
	}
	return validator
}

func nullableJSONSchema(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		result := make(map[string]any, len(typed))
		for key, child := range typed {
			if key != "nullable" {
				result[key] = nullableJSONSchema(child)
			}
		}
		if typed["nullable"] == true {
			return map[string]any{"anyOf": []any{result, map[string]any{"type": "null"}}}
		}
		return result
	case []any:
		result := make([]any, len(typed))
		for index, child := range typed {
			result[index] = nullableJSONSchema(child)
		}
		return result
	default:
		return value
	}
}
