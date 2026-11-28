package http

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/gin-gonic/gin/binding"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

var (
	errInvalidJSONBody  = errors.New("invalid JSON request body")
	errJSONBodyTooLarge = errors.New("JSON request body exceeds the 8 MiB limit")
)

// strictJSONBodyValidator is an optional feature-owned validation hook for
// constraints that cannot be expressed by Go binding tags, such as the
// presence of a non-pointer nested struct's required JSON members.
type strictJSONBodyValidator interface {
	ValidateStrictJSONBody([]byte) error
}

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

type TextInputRequest struct {
	Content string `json:"content" binding:"required,max=200000"`
}

type CancelRunRequest struct {
	ExpectedRevision uint64 `json:"expectedRevision" binding:"required,min=1"`
	Reason           string `json:"reason" binding:"omitempty,max=2000"`
}

type RunSnapshotResponse struct {
	Run         kernel.Run             `json:"run"`
	State       json.RawMessage        `json:"state"`
	Checkpoint  *kernel.Checkpoint     `json:"checkpoint,omitempty"`
	Result      *kernel.Result         `json:"result,omitempty"`
	EventHead   int64                  `json:"eventHead"`
	PendingTool *agent.PendingToolView `json:"pendingTool,omitempty"`
}

type RunEventPageResponse struct {
	Events    []kernel.Event `json:"events"`
	EventHead int64          `json:"eventHead"`
}

// SnapshotResponse is the single public projection for start, read and cancel.
// Callers must authorize the Run before projecting its Agent-owned wait view.
func SnapshotResponse(snapshot kernel.Snapshot) RunSnapshotResponse {
	response := RunSnapshotResponse{
		Run: PublicRun(snapshot.Run), State: PublicDiagnosticPayload(snapshot.State),
		Checkpoint: publicRunCheckpoint(snapshot.Checkpoint), Result: snapshot.Result,
		EventHead: snapshot.EventHead,
	}
	if snapshot.Run.Kind == agent.RunKind {
		if view, err := agent.ViewState(snapshot); err == nil {
			response.PendingTool = view.PendingTool
		}
	}
	return response
}

// Public Run reads expose checkpoint lifecycle and identity, not feature-owned
// execution payloads or responses. The original checkpoint remains in Kernel
// for approval verification, cancellation and recovery.
func publicRunCheckpoint(checkpoint *kernel.Checkpoint) *kernel.Checkpoint {
	if checkpoint == nil {
		return nil
	}
	public := *checkpoint
	if checkpoint.ResolvedAt != nil {
		resolvedAt := *checkpoint.ResolvedAt
		public.ResolvedAt = &resolvedAt
	}
	public.Payload = nil // Required wire field encodes as JSON null.
	public.Response = nil
	return &public
}

func bindStrictJSON(context *gin.Context, target interface{}) error {
	if context == nil || context.Request == nil || context.Request.Body == nil {
		return errInvalidJSONBody
	}
	raw, err := io.ReadAll(io.LimitReader(context.Request.Body, maxStrictJSONBodyBytes+1))
	if err != nil {
		return errInvalidJSONBody
	}
	if len(raw) > int(maxStrictJSONBodyBytes) {
		return errJSONBodyTooLarge
	}
	if len(bytes.TrimSpace(raw)) == 0 {
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
	if validator, ok := target.(strictJSONBodyValidator); ok {
		if err = validator.ValidateStrictJSONBody(raw); err != nil {
			return errInvalidJSONBody
		}
	}
	context.Request.Body = io.NopCloser(bytes.NewReader(raw))
	return nil
}

func normalizeThread(request ThreadRequest) kernel.ThreadRef {
	return kernel.ThreadRef{Kind: strings.TrimSpace(request.Kind), ID: strings.TrimSpace(request.ID)}
}
