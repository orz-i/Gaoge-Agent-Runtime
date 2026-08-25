package workflow

import (
	"context"
	"errors"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

// Trace is a redacted operational projection. It deliberately excludes all
// workflow inputs, effect inputs/outputs, wait payloads/responses, and event data.
type Trace struct {
	RunID         string              `json:"runID"`
	Status        kernel.RunStatus    `json:"status"`
	Revision      uint64              `json:"revision"`
	Definition    DefinitionReference `json:"definition"`
	CurrentNodeID string              `json:"currentNodeID,omitempty"`
	NestedDepth   int                 `json:"nestedDepth"`
	Budget        Budget              `json:"budget"`
	Activations   []ActivationTrace   `json:"activations"`
	Effects       []EffectTrace       `json:"effects"`
	Waits         []WaitTrace         `json:"waits"`
	Compensations []CompensationTrace `json:"compensations"`
	Events        []TraceEvent        `json:"events"`
}

type ActivationTrace struct {
	ID        string           `json:"id"`
	NodeID    string           `json:"nodeID"`
	NodeType  NodeType         `json:"nodeType"`
	Status    ActivationStatus `json:"status"`
	Attempt   int              `json:"attempt"`
	EffectIDs []string         `json:"effectIDs,omitempty"`
	WaitID    string           `json:"waitID,omitempty"`
	ErrorCode string           `json:"errorCode,omitempty"`
}

type EffectTrace struct {
	ID           string       `json:"id"`
	NodeID       string       `json:"nodeID"`
	Class        EffectClass  `json:"class"`
	Kind         string       `json:"kind"`
	Revision     string       `json:"revision,omitempty"`
	Status       EffectStatus `json:"status"`
	Attempt      int          `json:"attempt"`
	MaxAttempts  int          `json:"maxAttempts"`
	ChildRunID   string       `json:"childRunID,omitempty"`
	ReceiptID    string       `json:"receiptID,omitempty"`
	CostUnits    int64        `json:"costUnits"`
	MaxCostUnits int64        `json:"maxCostUnits"`
	Compensation bool         `json:"compensation,omitempty"`
	ErrorCode    string       `json:"errorCode,omitempty"`
}

type WaitTrace struct {
	ID     string     `json:"id"`
	NodeID string     `json:"nodeID"`
	Kind   string     `json:"kind"`
	Status WaitStatus `json:"status"`
}

type CompensationTrace struct {
	ID        string             `json:"id"`
	NodeID    string             `json:"nodeID"`
	Kind      string             `json:"kind"`
	Status    CompensationStatus `json:"status"`
	EffectID  string             `json:"effectID,omitempty"`
	ReceiptID string             `json:"receiptID,omitempty"`
	ErrorCode string             `json:"errorCode,omitempty"`
}

type TraceEvent struct {
	Seq       int64     `json:"seq"`
	Type      string    `json:"type"`
	CreatedAt time.Time `json:"createdAt"`
}

// LoadRun returns one Workflow-owned snapshot for host authorization checks.
func (runner *Runner) LoadRun(ctx context.Context, runID string) (kernel.Snapshot, error) {
	if runner == nil || runner.runtime == nil {
		return kernel.Snapshot{}, ErrInvalidExecution
	}
	snapshot, err := runner.runtime.Load(ctx, runID)
	if err != nil {
		return kernel.Snapshot{}, err
	}
	if snapshot.Run.Kind != RunKind {
		return kernel.Snapshot{}, kernel.ErrNotFound
	}
	return snapshot, nil
}

// TraceForActor returns a redacted trace only to the owning tenant actor.
func (runner *Runner) TraceForActor(
	ctx context.Context,
	runID string,
	actor kernel.ActorRef,
) (Trace, error) {
	snapshot, err := runner.LoadRun(ctx, runID)
	if err != nil {
		return Trace{}, err
	}
	if snapshot.Run.Actor != actor {
		return Trace{}, ErrEffectForbidden
	}
	state, err := decodeExecutionState(snapshot.State)
	if err != nil || ValidateDefinition(state.Definition) != nil {
		return Trace{}, errors.Join(ErrInvalidExecution, err)
	}
	trace := Trace{
		RunID: snapshot.Run.ID, Status: snapshot.Run.Status, Revision: snapshot.Run.Revision,
		Definition: DefinitionReference{
			ID: state.Definition.ID, Revision: state.Definition.Revision, Hash: state.Definition.Hash,
		},
		NestedDepth: state.NestedDepth, Budget: state.Budget,
		Activations:   make([]ActivationTrace, 0, len(state.Activations)),
		Effects:       make([]EffectTrace, 0, len(state.Effects)),
		Waits:         make([]WaitTrace, 0, len(state.Waits)),
		Compensations: make([]CompensationTrace, 0, len(state.Compensations)),
		Events:        make([]TraceEvent, 0, len(snapshot.Events)),
	}
	if state.CurrentNode >= 0 && state.CurrentNode < len(state.Definition.Nodes) {
		trace.CurrentNodeID = state.Definition.Nodes[state.CurrentNode].ID
	}
	for _, activation := range state.Activations {
		nodeType := NodeType("")
		if activation.NodeIndex >= 0 && activation.NodeIndex < len(state.Definition.Nodes) {
			nodeType = state.Definition.Nodes[activation.NodeIndex].Type
		}
		effectIDs := append([]string(nil), activation.EffectIDs...)
		if len(effectIDs) == 0 && activation.EffectID != "" {
			effectIDs = []string{activation.EffectID}
		}
		trace.Activations = append(trace.Activations, ActivationTrace{
			ID: activation.ID, NodeID: activation.NodeID, NodeType: nodeType,
			Status: activation.Status, Attempt: activation.Attempt, EffectIDs: effectIDs,
			WaitID: activation.WaitID, ErrorCode: activation.ErrorCode,
		})
	}
	for _, effect := range state.Effects {
		trace.Effects = append(trace.Effects, EffectTrace{
			ID: effect.ID, NodeID: effect.NodeID, Class: effect.Class, Kind: effect.Kind,
			Revision: effect.Revision, Status: effect.Status, Attempt: effect.Attempt,
			MaxAttempts: effect.Retry.MaxAttempts, ChildRunID: effect.ChildRunID,
			ReceiptID: effect.ReceiptID, CostUnits: effect.CostUnits,
			MaxCostUnits: effect.MaxCostUnits, Compensation: effect.Compensation,
			ErrorCode: effect.ErrorCode,
		})
	}
	for _, wait := range state.Waits {
		trace.Waits = append(trace.Waits, WaitTrace{
			ID: wait.ID, NodeID: wait.NodeID, Kind: wait.Kind, Status: wait.Status,
		})
	}
	for _, compensation := range state.Compensations {
		trace.Compensations = append(trace.Compensations, CompensationTrace{
			ID: compensation.ID, NodeID: compensation.NodeID, Kind: compensation.Call.Kind,
			Status: compensation.Status, EffectID: compensation.EffectID,
			ReceiptID: compensation.ReceiptID, ErrorCode: compensation.ErrorCode,
		})
	}
	for _, event := range snapshot.Events {
		trace.Events = append(trace.Events, TraceEvent{
			Seq: event.Seq, Type: event.Type, CreatedAt: event.CreatedAt,
		})
	}
	return trace, nil
}
