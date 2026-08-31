package http

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/workbench"
)

var errInvalidJSONBody = errors.New("invalid JSON request body")

type ThreadRequest struct {
	Kind string `json:"kind" binding:"required,max=64"`
	ID   string `json:"id" binding:"required,max=128"`
}

// NormalizeThread converts the shared HTTP thread contract to a Kernel reference.
func NormalizeThread(request ThreadRequest) kernel.ThreadRef { return normalizeThread(request) }

// BindStrictJSON decodes one request body and rejects unknown fields.
func BindStrictJSON(context *gin.Context, target interface{}) error {
	return bindStrictJSON(context, target)
}

// SnapshotResponse projects one durable Kernel snapshot without Feature interpretation.
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
	Run        kernel.Run         `json:"run"`
	State      json.RawMessage    `json:"state"`
	Checkpoint *kernel.Checkpoint `json:"checkpoint,omitempty"`
	Result     *kernel.Result     `json:"result,omitempty"`
	EventHead  int64              `json:"eventHead"`
}

type RunEventPageResponse struct {
	Events    []kernel.Event `json:"events"`
	EventHead int64          `json:"eventHead"`
}

func snapshotResponse(snapshot kernel.Snapshot) RunSnapshotResponse {
	return RunSnapshotResponse{
		Run: snapshot.Run, State: append(json.RawMessage(nil), snapshot.State...),
		Checkpoint: snapshot.Checkpoint, Result: snapshot.Result,
		EventHead: snapshot.EventHead,
	}
}

func workbenchResponse(detail workbench.Detail) workbench.Detail { return detail }

func bindStrictJSON(context *gin.Context, target interface{}) error {
	if context == nil || context.Request == nil || context.Request.Body == nil {
		return errInvalidJSONBody
	}
	raw, err := io.ReadAll(context.Request.Body)
	if err != nil || len(bytes.TrimSpace(raw)) == 0 {
		return errInvalidJSONBody
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(target); err != nil {
		return errInvalidJSONBody
	}
	if decoder.More() {
		return errInvalidJSONBody
	}
	context.Request.Body = io.NopCloser(bytes.NewReader(raw))
	return nil
}

func normalizeThread(request ThreadRequest) kernel.ThreadRef {
	return kernel.ThreadRef{Kind: strings.TrimSpace(request.Kind), ID: strings.TrimSpace(request.ID)}
}
