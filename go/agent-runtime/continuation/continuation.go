// Package continuation provides durable, idempotent resumption delivery without
// moving feature-owned state transitions into the Kernel.
package continuation

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	queuecore "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/queue"
)

const CapabilityDispatcher kernel.Capability = "continuation.dispatcher"

const (
	QueueName     = "runtime-continuations"
	JobKind       = "runtime.continuation.v1"
	SchemaVersion = 1
)

var (
	ErrInvalidInput       = errors.New("invalid continuation input")
	ErrUnsupportedRunKind = errors.New("unsupported continuation run kind")
	ErrAlreadyStarted     = errors.New("continuation worker already started")
	ErrClosed             = errors.New("continuation worker is closed")
)

// Trigger identifies the committed fact that permits a Run to resume.
type Trigger string

const (
	TriggerChildTerminal    Trigger = "child_terminal"
	TriggerApprovalResolved Trigger = "approval_resolved"
	TriggerWaitResolved     Trigger = "wait_resolved"
	TriggerSegmentYielded   Trigger = "segment_yielded"
)

// Payload is the immutable continuation Job body. ExpectedRevision prevents a
// stale delivery from advancing a newer state generation.
type Payload struct {
	SchemaVersion    int     `json:"schemaVersion"`
	RunID            string  `json:"runID"`
	ExpectedRevision uint64  `json:"expectedRevision"`
	Trigger          Trigger `json:"trigger"`
	SourceRunID      string  `json:"sourceRunID"`
	SourceRevision   uint64  `json:"sourceRevision"`
}

// Enqueuer is the narrow durable delivery capability consumed by Scheduler.
type Enqueuer interface {
	Enqueue(context.Context, queuecore.EnqueueRequest) (queuecore.EnqueueResult, error)
}

// DeliveryQueue is the leased delivery capability owned by Worker.
type DeliveryQueue interface {
	kernel.Feature
	Enqueuer
	Claim(context.Context, queuecore.ClaimRequest) ([]queuecore.Delivery, error)
	Ack(context.Context, queuecore.LeaseRequest) (queuecore.Job, error)
	Nack(context.Context, queuecore.NackRequest) (queuecore.Job, error)
}

// SnapshotLoader resolves current durable Run state.
type SnapshotLoader interface {
	Load(context.Context, string) (kernel.Snapshot, error)
}

// LoaderFunc adapts a late-bound host function into SnapshotLoader.
type LoaderFunc func(context.Context, string) (kernel.Snapshot, error)

// Load implements SnapshotLoader.
func (loader LoaderFunc) Load(ctx context.Context, runID string) (kernel.Snapshot, error) {
	if loader == nil {
		return kernel.Snapshot{}, ErrInvalidInput
	}
	return loader(ctx, runID)
}

// Resumer is the shared explicit recovery surface implemented by every Runtime feature.
type Resumer interface {
	Resume(context.Context, string, uint64) (kernel.Snapshot, error)
}

// Handler consumes one decoded continuation payload.
type Handler interface {
	Dispatch(context.Context, Payload) error
}

// Reconciler recovers missed child-terminal delivery from durable Run relations.
type Reconciler interface {
	Reconcile(context.Context) error
}

func normalizePayload(payload Payload) (Payload, error) {
	payload.RunID = strings.TrimSpace(payload.RunID)
	payload.SourceRunID = strings.TrimSpace(payload.SourceRunID)
	payload.Trigger = Trigger(strings.TrimSpace(string(payload.Trigger)))
	if payload.SchemaVersion == 0 {
		payload.SchemaVersion = SchemaVersion
	}
	if payload.SchemaVersion != SchemaVersion || payload.RunID == "" || payload.SourceRunID == "" ||
		payload.ExpectedRevision == 0 || payload.SourceRevision == 0 || !validTrigger(payload.Trigger) {
		return Payload{}, ErrInvalidInput
	}
	return payload, nil
}

func decodePayload(encoded json.RawMessage) (Payload, error) {
	var payload Payload
	if err := json.Unmarshal(encoded, &payload); err != nil {
		return Payload{}, errors.Join(ErrInvalidInput, err)
	}
	return normalizePayload(payload)
}

func validTrigger(trigger Trigger) bool {
	return trigger == TriggerChildTerminal || trigger == TriggerApprovalResolved ||
		trigger == TriggerWaitResolved || trigger == TriggerSegmentYielded
}

func terminal(status kernel.RunStatus) bool {
	return status == kernel.RunStatusCompleted || status == kernel.RunStatusFailed ||
		status == kernel.RunStatusCancelled
}
