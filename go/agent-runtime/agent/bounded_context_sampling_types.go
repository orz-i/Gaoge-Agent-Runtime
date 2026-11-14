package agent

import (
	"context"
	"encoding/json"
	runtimecontext "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/context"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
	"time"
)

// SamplingTurn is the complete durable Turn projection needed by a checkpoint
// CAS. Core deliberately does not depend on the Harness package.
type SamplingTurn struct {
	ID, SessionID                         string
	HostTurn                              struct{ Kind, ID string }
	ConfigSnapshotID, ContextCheckpointID string
	ContextRef                            SamplingCheckpointRef
	Status                                string
	Revision                              uint64
	ErrorCode, ErrorDetail                string
	CreatedAt, UpdatedAt                  time.Time
}
type SamplingCheckpointRef struct {
	ID                                               string
	Generation, Revision                             int
	LineageHash, CoveredThroughSourceID, ContentHash string
}
type SamplingCursor struct {
	Turn                                               SamplingTurn
	Checkpoint                                         runtimecontext.Checkpoint
	ExpectedTurnCheckpointID, ExpectedHeadCheckpointID string
	UpdatedAt                                          time.Time
}
type SamplingPlan struct {
	Source                kernel.Snapshot
	Request               model.Request
	Checkpoint            *runtimecontext.Checkpoint
	Cursor                SamplingCursor
	Owner, ArtifactReader bool
	ModelWindow           runtimecontext.ModelWindow
	WindowFingerprint     string
	Policy                runtimecontext.Policy
	Guidance              []model.Message
	GuidanceStart         int
	// DetachedScope is an authorized artifact scope, not Context ownership.
	DetachedScope string
	// Data freezes host-specific canonical construction inputs. It is not proof.
	Data json.RawMessage
}
type SamplingEvaluation struct {
	Request    model.Request
	Assessment runtimecontext.Assessment
	Artifacts  []runtimecontext.Artifact
}
type SamplingCommitReceipt struct {
	Turn                                               SamplingTurn
	Checkpoint                                         runtimecontext.Checkpoint
	ExpectedTurnCheckpointID, ExpectedHeadCheckpointID string
}
type SamplingBindingReceipt struct {
	TurnID, Access    string
	Previous, Current runtimecontext.Checkpoint
}

// SamplingContextPorts expose individual authorized operations, never Guard,
// Next, a completion flag or an error classifier. Evaluate must not persist.
type SamplingContextPorts interface {
	Hydrate(context.Context, plugin.RunInvocation) (context.Context, BoundedContextHydration, error)
	PlanSampling(context.Context, model.Request, kernel.Snapshot) (SamplingPlan, error)
	EvaluateSampling(context.Context, SamplingPlan, model.Request, runtimecontext.Window, bool) (SamplingEvaluation, error)
	PutArtifact(context.Context, runtimecontext.Artifact) (runtimecontext.Artifact, error)
	CommitSampling(context.Context, SamplingCursor, runtimecontext.Checkpoint) (SamplingCommitReceipt, error)
	BindSampling(context.Context, SamplingCursor, runtimecontext.Checkpoint) (SamplingBindingReceipt, error)
}

func NewSamplingBoundedContextAdapter(manager *runtimecontext.Manager, ports SamplingContextPorts) (*BoundedContextAdapter, error) {
	if manager == nil || ports == nil {
		return nil, ErrInvalidRequest
	}
	return &BoundedContextAdapter{manager: manager, sampling: ports}, nil
}
func samplingRef(c runtimecontext.Checkpoint) SamplingCheckpointRef {
	return SamplingCheckpointRef{ID: c.ID, Generation: c.Generation, Revision: c.Revision, LineageHash: c.LineageHash, CoveredThroughSourceID: c.CoveredThroughSourceID, ContentHash: c.ContentHash}
}
func cloneSampling[T any](v T) T {
	// Only the closed, typed DTOs in this file use this helper.
	raw, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	var out T
	if err = json.Unmarshal(raw, &out); err != nil {
		panic(err)
	}
	return out
}
