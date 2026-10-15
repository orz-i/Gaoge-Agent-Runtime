package http

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gin-gonic/gin/binding"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/workbench"
)

var errInvalidJSONBody = errors.New("invalid JSON request body")

// Bound memory use for nested/raw JSON request fields that have no size tag.
const maxStrictJSONBodyBytes int64 = 8 << 20

type ThreadRequest struct {
	Kind string `json:"kind" binding:"required,max=64"`
	ID   string `json:"id" binding:"required,max=128"`
}

// NormalizeThread converts the shared HTTP thread contract to a Kernel reference.
func NormalizeThread(request ThreadRequest) kernel.ThreadRef { return normalizeThread(request) }

// BindStrictJSON decodes exactly one JSON value, rejects unknown fields,
// and applies the declared binding validation rules.
func BindStrictJSON(context *gin.Context, target interface{}) error {
	return bindStrictJSON(context, target)
}

// SnapshotResponse projects feature-neutral durable Run facts.
func SnapshotResponse(snapshot kernel.Snapshot) RunSnapshotResponse {
	return snapshotResponse(snapshot)
}

type TextInputRequest struct {
	Content string `json:"content" binding:"required,max=200000"`
}

type CancelRunRequest struct {
	ExpectedRevision uint64 `json:"expectedRevision" binding:"required,min=1"`
	Reason           string `json:"reason" binding:"omitempty,max=2000"`
}

type CancelRunResponse struct {
	Run kernel.Run `json:"run"`
}

type RunSnapshotResponse struct {
	Run         kernel.Run           `json:"run"`
	State       json.RawMessage      `json:"state"`
	Checkpoint  *kernel.Checkpoint   `json:"checkpoint,omitempty"`
	Result      *kernel.Result       `json:"result,omitempty"`
	EventHead   int64                `json:"eventHead"`
	PendingTool *PendingToolResponse `json:"pendingTool,omitempty"`
}

// PendingToolResponse is bounded transport data supplied by a feature projector.
type PendingToolResponse struct {
	Kind       tools.PendingKind `json:"kind"`
	CallID     string            `json:"callID"`
	ToolKey    string            `json:"toolKey"`
	DeadlineAt *time.Time        `json:"deadlineAt,omitempty"`
	Ready      bool              `json:"ready"`
}

// PendingToolProjector reads feature-owned state after object authorization.
type PendingToolProjector func(kernel.Snapshot) *PendingToolResponse

type RunEventPageResponse struct {
	Events    []kernel.Event `json:"events"`
	EventHead int64          `json:"eventHead"`
}

func snapshotResponse(snapshot kernel.Snapshot) RunSnapshotResponse {
	response := RunSnapshotResponse{
		Run: PublicRun(snapshot.Run), State: PublicDiagnosticPayload(snapshot.State),
		Checkpoint: snapshot.Checkpoint, Result: snapshot.Result,
		EventHead: snapshot.EventHead,
	}
	return response
}

func workbenchResponse(detail workbench.Detail) workbench.Detail { return publicWorkbench(detail) }

func bindStrictJSON(context *gin.Context, target interface{}) error {
	if context == nil || context.Request == nil || context.Request.Body == nil {
		return errInvalidJSONBody
	}
	raw, err := io.ReadAll(io.LimitReader(context.Request.Body, maxStrictJSONBodyBytes+1))
	if err != nil || len(raw) > int(maxStrictJSONBodyBytes) || len(bytes.TrimSpace(raw)) == 0 {
		return errInvalidJSONBody
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(target); err != nil {
		return errInvalidJSONBody
	}
	// More only checks elements within an array/object; a second decode
	// must reach EOF to reject trailing delimiters or another JSON value.
	var trailing json.RawMessage
	if err = decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errInvalidJSONBody
	}
	if err = binding.Validator.ValidateStruct(target); err != nil {
		return errInvalidJSONBody
	}
	context.Request.Body = io.NopCloser(bytes.NewReader(raw))
	return nil
}

func normalizeThread(request ThreadRequest) kernel.ThreadRef {
	return kernel.ThreadRef{Kind: strings.TrimSpace(request.Kind), ID: strings.TrimSpace(request.ID)}
}
