// Package observability defines content-safe, best-effort runtime observations
// for separately composed telemetry adapters. It has no OpenTelemetry or
// provider dependency and deliberately exposes no prompt/result payload field.
package observability

import (
	"context"
	"errors"
	"strings"
	"time"

	runtimebudget "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

var (
	ErrInvalidRecorder = errors.New("invalid runtime observability recorder")
	ErrDuplicateName   = errors.New("duplicate runtime observability recorder")
)

type Scope string

const (
	ScopeHarnessTurn     Scope = "harness_turn"
	ScopeRun             Scope = "run"
	ScopeModelInvocation Scope = "model_invocation"
	ScopeToolInvocation  Scope = "tool_invocation"
	ScopeWorkflowEffect  Scope = "workflow_effect"
	ScopeA2AInvocation   Scope = "a2a_invocation"
)

type Phase string

const (
	PhaseStarted   Phase = "started"
	PhaseCompleted Phase = "completed"
	PhaseFailed    Phase = "failed"
	PhaseCancelled Phase = "cancelled"
)

// Event contains only structural telemetry facts. Text/model prompts,
// completions, Tool arguments/results, Workflow inputs/outputs and arbitrary
// attribute maps are intentionally absent so adapters are content-off by
// construction unless a separate explicit content policy is added later.
type Event struct {
	Scope        Scope               `json:"scope"`
	Phase        Phase               `json:"phase"`
	RunID        string              `json:"runID"`
	RunKind      kernel.RunKind      `json:"runKind"`
	Revision     uint64              `json:"revision,omitempty"`
	Status       string              `json:"status,omitempty"`
	OperationID  string              `json:"operationID,omitempty"`
	Operation    string              `json:"operation,omitempty"`
	Provider     string              `json:"provider,omitempty"`
	Model        string              `json:"model,omitempty"`
	ResponseID   string              `json:"responseID,omitempty"`
	Attempt      int                 `json:"attempt,omitempty"`
	Compensation bool                `json:"compensation,omitempty"`
	ChildRunID   string              `json:"childRunID,omitempty"`
	ErrorCode    string              `json:"errorCode,omitempty"`
	ObservedAt   time.Time           `json:"observedAt"`
	Duration     time.Duration       `json:"duration,omitempty"`
	Usage        runtimebudget.Usage `json:"usage,omitempty"`
}

// Recorder consumes best-effort structural observations. Implementations may
// be called concurrently, must be concurrency-safe, return promptly and honor
// ctx cancellation/deadlines; Runtime deliberately does not spawn unbounded
// goroutines to isolate a recorder that blocks.
type Recorder interface {
	Name() string
	Record(context.Context, Event)
}

type RecorderFunc struct {
	RecorderName string
	RecordFunc   func(context.Context, Event)
}

func (recorder RecorderFunc) Name() string { return strings.TrimSpace(recorder.RecorderName) }

func (recorder RecorderFunc) Record(ctx context.Context, event Event) {
	if recorder.RecordFunc != nil {
		recorder.RecordFunc(ctx, event)
	}
}

// Set freezes recorder order. Observability is best effort: recorder panics are
// contained and never become runtime correctness signals.
type Set struct{ recorders []Recorder }

func NewSet(recorders ...Recorder) (*Set, error) {
	seen := make(map[string]struct{}, len(recorders))
	for _, recorder := range recorders {
		if recorder == nil || strings.TrimSpace(recorder.Name()) == "" {
			return nil, ErrInvalidRecorder
		}
		name := strings.TrimSpace(recorder.Name())
		if _, duplicate := seen[name]; duplicate {
			return nil, ErrDuplicateName
		}
		seen[name] = struct{}{}
	}
	return &Set{recorders: append([]Recorder(nil), recorders...)}, nil
}

func (set *Set) Record(ctx context.Context, event Event) {
	if set == nil || strings.TrimSpace(event.RunID) == "" || event.Scope == "" || event.Phase == "" {
		return
	}
	for _, recorder := range set.recorders {
		recordSafely(ctx, recorder, event)
	}
}

func recordSafely(ctx context.Context, recorder Recorder, event Event) {
	defer func() { _ = recover() }()
	recorder.Record(ctx, event)
}
