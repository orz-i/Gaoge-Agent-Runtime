package http

import (
	"bytes"
	"encoding/json"
	stdhttp "net/http"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

// Public Run reads are not a channel for opaque Checkpoint execution data.
// The persisted original remains available to the Agent/interaction recovery path.
func TestOwnerRunHTTPHidesOpaqueCheckpointBodiesWithoutChangingDurableState(t *testing.T) {
	t.Parallel()
	const secret = "PRIVATE_APPROVAL_ARGUMENT_PR123"
	owner := kernel.ActorRef{TenantID: "tenant-checkpoint", ActorID: "owner"}
	createdAt := time.Date(2026, time.November, 16, 12, 0, 0, 0, time.UTC)
	resolvedAt := createdAt.Add(time.Minute)
	for _, tc := range []struct {
		name, kind string
		status     kernel.CheckpointStatus
		runStatus  kernel.RunStatus
		response   json.RawMessage
	}{
		{name: "pending tool approval", kind: "tool_approval", status: kernel.CheckpointPending, runStatus: kernel.RunStatusWaitingInput},
		{name: "resolved tool approval", kind: "tool_approval", status: kernel.CheckpointResolved, runStatus: kernel.RunStatusRunning,
			response: json.RawMessage(`{"decision":"reject","comment":"PRIVATE_APPROVAL_ARGUMENT_PR123"}`)},
		{name: "unknown host checkpoint", kind: "host_private", status: kernel.CheckpointPending, runStatus: kernel.RunStatusWaitingInput},
	} {
		t.Run(tc.name, func(t *testing.T) {
			engine, runtime := newAuthorizationHTTPTest(t, owner, nil)
			runID := "checkpoint-run"
			createAuthorizationRun(t, runtime, owner, runID)
			created, err := runtime.Load(t.Context(), runID)
			if err != nil {
				t.Fatal(err)
			}
			checkpoint := &kernel.Checkpoint{
				ID: "checkpoint-1", Kind: tc.kind, Status: tc.status, CreatedAt: createdAt,
				Payload:  json.RawMessage(`{"toolCallID":"private-internal-id","toolKey":"example.write","arguments":{"apiKey":"PRIVATE_APPROVAL_ARGUMENT_PR123"},"opaque":{"providerReplay":"PRIVATE_APPROVAL_ARGUMENT_PR123"}}`),
				Response: tc.response,
			}
			if tc.status == kernel.CheckpointResolved {
				checkpoint.ResolvedAt = &resolvedAt
			}
			_, err = runtime.Apply(t.Context(), runID, created.Run.Revision, kernel.Mutation{
				Status: tc.runStatus, State: json.RawMessage(`{"safe":"visible"}`),
				Checkpoint: checkpoint,
			})
			if err != nil {
				t.Fatal(err)
			}
			saved, err := runtime.Load(t.Context(), runID)
			if err != nil {
				t.Fatal(err)
			}
			path := "/api/v1/runs/" + runID
			result := performAuthorizationRequest(t, engine, stdhttp.MethodGet, path, nil)
			if result.Code != stdhttp.StatusOK {
				t.Fatalf("owner read status=%d body=%s", result.Code, result.Body.String())
			}
			if strings.Contains(result.Body.String(), secret) || strings.Contains(result.Body.String(), "private-internal-id") ||
				strings.Contains(result.Body.String(), "providerReplay") || strings.Contains(result.Body.String(), "\"arguments\"") {
				t.Fatalf("public snapshot leaked checkpoint proof: %s", result.Body.String())
			}
			var visible RunSnapshotResponse
			if err := json.Unmarshal(result.Body.Bytes(), &visible); err != nil {
				t.Fatal(err)
			}
			if visible.Checkpoint == nil || visible.Checkpoint.ID != checkpoint.ID || visible.Checkpoint.Kind != checkpoint.Kind ||
				visible.Checkpoint.Status != checkpoint.Status || !visible.Checkpoint.CreatedAt.Equal(createdAt) ||
				(visible.Checkpoint.ResolvedAt == nil) != (checkpoint.ResolvedAt == nil) ||
				(visible.Checkpoint.ResolvedAt != nil && !visible.Checkpoint.ResolvedAt.Equal(resolvedAt)) {
				t.Fatalf("checkpoint lifecycle and identity lost: %+v", visible.Checkpoint)
			}
			if string(visible.Checkpoint.Payload) != "null" || len(visible.Checkpoint.Response) != 0 ||
				string(visible.State) != `{"safe":"visible"}` {
				t.Fatalf("public checkpoint fields: %+v, state: %s", visible.Checkpoint, visible.State)
			}
			if saved.Checkpoint == nil || !bytes.Equal(saved.Checkpoint.Payload, checkpoint.Payload) ||
				!bytes.Equal(saved.Checkpoint.Response, checkpoint.Response) {
				t.Fatalf("public projection mutated original checkpoint: %+v", saved.Checkpoint)
			}
			projected := SnapshotResponse(saved)
			if projected.Checkpoint == saved.Checkpoint {
				t.Fatal("public projection reused mutable source checkpoint")
			}
			projected.Checkpoint.ID = "changed-by-observer"
			if projected.Checkpoint.ResolvedAt != nil {
				*projected.Checkpoint.ResolvedAt = resolvedAt.Add(time.Hour)
			}
			if saved.Checkpoint.ID != checkpoint.ID ||
				(saved.Checkpoint.ResolvedAt != nil && !saved.Checkpoint.ResolvedAt.Equal(resolvedAt)) {
				t.Fatal("public checkpoint mutated original identity or timestamp")
			}

			foreign := gin.New()
			NewModule(NewHandler(Dependencies{
				Runtime: runtime,
				Shared:  NewShared(authorizationPrincipal{actor: kernel.ActorRef{TenantID: "other", ActorID: "observer"}}, nil, runtime, nil),
			})).RegisterRoutes(foreign.Group("/api/v1"))
			forbidden := performAuthorizationRequest(t, foreign, stdhttp.MethodGet, path, nil)
			if forbidden.Code != stdhttp.StatusNotFound {
				t.Fatalf("foreign run read returned %d: %s", forbidden.Code, forbidden.Body.String())
			}
		})
	}
}

func TestPublicRunSnapshotWithoutCheckpoint(t *testing.T) {
	t.Parallel()
	if projected := SnapshotResponse(kernel.Snapshot{}); projected.Checkpoint != nil {
		t.Fatalf("nil checkpoint projected as %+v", projected.Checkpoint)
	}
}
