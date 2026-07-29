package agentruntime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"

	model "github.com/orz-i/Gaoge/sdk/go/agent-runtime/domain"
)

const RuntimeExecutionProvenanceSchemaVersion = 1

var ErrRuntimeExecutionProvenanceNotFrozen = errors.New("runtime execution provenance is not frozen")

// RuntimeResourceRevisionRefV1 is a neutral, revision-aware resource reference.
// It intentionally carries no resource body or host persistence identifier.
type RuntimeResourceRevisionRefV1 struct {
	Kind     string `json:"kind"`
	ID       string `json:"id"`
	Revision string `json:"revision,omitempty"`
}

// RuntimeModelRoutingSummaryV1 records the effective model route without
// exposing credentials, endpoints, system prompts, or tool arguments.
type RuntimeModelRoutingSummaryV1 struct {
	RequestedModelName string `json:"requestedModelName,omitempty"`
	PlatformModelName  string `json:"platformModelName,omitempty"`
	Provider           string `json:"provider,omitempty"`
	ProviderProtocol   string `json:"providerProtocol,omitempty"`
	RoutedBindingCode  string `json:"routedBindingCode,omitempty"`
	ModelVendor        string `json:"modelVendor,omitempty"`
	UpstreamModelName  string `json:"upstreamModelName,omitempty"`
}

// RuntimeExecutionProvenanceV1 is a frozen, provider-neutral execution source.
// It is available only after a Run reaches a terminal state. SnapshotHash
// covers the exact persisted Run configuration; StateHash covers terminal
// Runtime state while exposing none of that state's payload.
type RuntimeExecutionProvenanceV1 struct {
	SchemaVersion         int                           `json:"schemaVersion"`
	RunID                 string                        `json:"runID"`
	RootRunID             string                        `json:"rootRunID"`
	RuntimeKind           string                        `json:"runtimeKind"`
	EnvironmentRef        *RuntimeResourceRevisionRefV1 `json:"environmentRef,omitempty"`
	AgentManifestRef      *RuntimeResourceRevisionRefV1 `json:"agentManifestRef,omitempty"`
	WorkflowDefinitionRef *RuntimeResourceRevisionRefV1 `json:"workflowDefinitionRef,omitempty"`
	ModelRouting          RuntimeModelRoutingSummaryV1  `json:"modelRouting"`
	SnapshotHash          string                        `json:"snapshotHash"`
	StateHash             string                        `json:"stateHash"`
}

type runtimeExecutionStateFingerprintV1 struct {
	RunID                    string
	RootRunID                string
	RuntimeKind              string
	RequestFingerprint       string
	Status                   string
	ErrorCode                string
	CurrentStepID            string
	CurrentPlanID            string
	LastEventSeq             int64
	LastPresentationEventSeq int64
	ContextContentHash       string
	ResultContentHash        string
	WorkflowStateHash        string
}

type runtimeWorkflowStateFingerprintV1 struct {
	Version            int64
	Status             string
	CompletionSeq      int64
	ErrorCode          string
	StateJSON          string
	VarsJSON           string
	WaitsJSON          string
	CompensationJSON   string
	BudgetJSON         string
	ThreadSnapshotHash string
}

func (s *Engine) GetRuntimeExecutionProvenance(
	ctx context.Context,
	actor model.ActorRef,
	runID string,
) (*RuntimeExecutionProvenanceV1, error) {
	if s == nil || s.repo == nil || !validActorRef(actor) || strings.TrimSpace(runID) == "" {
		return nil, ErrInvalidInput
	}
	run, err := s.repo.GetRun(ctx, actor, strings.TrimSpace(runID))
	if err != nil {
		return nil, err
	}
	if !runtimeExecutionProvenanceFrozen(run.Status) {
		return nil, ErrRuntimeExecutionProvenanceNotFrozen
	}
	snapshotHash := hashRuntimeExecutionBytes([]byte(run.RunConfigSnapshotJSON))
	stateHash, err := s.runtimeExecutionStateHash(ctx, actor, *run)
	if err != nil {
		return nil, err
	}
	rootRunID := strings.TrimSpace(run.RootRunID)
	if rootRunID == "" {
		rootRunID = run.RunID
	}
	return &RuntimeExecutionProvenanceV1{
		SchemaVersion:         RuntimeExecutionProvenanceSchemaVersion,
		RunID:                 run.RunID,
		RootRunID:             rootRunID,
		RuntimeKind:           model.NormalizeRuntimeKind(run.RuntimeKind),
		EnvironmentRef:        runtimeExecutionRevisionRef(run.Environment),
		AgentManifestRef:      runtimeExecutionRevisionRef(run.AgentManifest),
		WorkflowDefinitionRef: runtimeExecutionRevisionRef(run.WorkflowDefinition),
		ModelRouting:          runtimeExecutionModelRouting(*run),
		SnapshotHash:          snapshotHash,
		StateHash:             stateHash,
	}, nil
}

func (s *Engine) runtimeExecutionStateHash(
	ctx context.Context,
	actor model.ActorRef,
	run model.Run,
) (string, error) {
	contextHash, err := s.runtimeExecutionContextHash(ctx, actor, run.RunID)
	if err != nil {
		return "", err
	}
	resultHash, err := s.runtimeExecutionResultHash(ctx, actor, run.RunID)
	if err != nil {
		return "", err
	}
	workflowHash, err := s.runtimeExecutionWorkflowHash(ctx, actor, run)
	if err != nil {
		return "", err
	}
	return hashRuntimeExecutionValue(runtimeExecutionStateFingerprintV1{
		RunID: run.RunID, RootRunID: run.RootRunID, RuntimeKind: model.NormalizeRuntimeKind(run.RuntimeKind),
		RequestFingerprint: run.RequestFingerprint, Status: run.Status, ErrorCode: run.ErrorCode,
		CurrentStepID: run.CurrentStepID, CurrentPlanID: run.CurrentPlanID,
		LastEventSeq: run.LastEventSeq, LastPresentationEventSeq: run.LastPresentationEventSeq,
		ContextContentHash: contextHash, ResultContentHash: resultHash, WorkflowStateHash: workflowHash,
	})
}

func (s *Engine) runtimeExecutionContextHash(
	ctx context.Context,
	actor model.ActorRef,
	runID string,
) (string, error) {
	snapshot, err := s.repo.GetRunContextSnapshot(ctx, actor, runID)
	if errors.Is(err, ErrNotFound) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(snapshot.ContentHash), nil
}

func (s *Engine) runtimeExecutionResultHash(
	ctx context.Context,
	actor model.ActorRef,
	runID string,
) (string, error) {
	result, err := s.repo.GetRunResult(ctx, actor, runID)
	if errors.Is(err, ErrNotFound) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(result.ContentHash), nil
}

func (s *Engine) runtimeExecutionWorkflowHash(
	ctx context.Context,
	actor model.ActorRef,
	run model.Run,
) (string, error) {
	if model.NormalizeRuntimeKind(run.RuntimeKind) != model.RuntimeKindWorkflow {
		return "", nil
	}
	execution, err := s.repo.GetWorkflowExecution(ctx, actor, run.RunID)
	if err != nil {
		return "", err
	}
	return hashRuntimeExecutionValue(runtimeWorkflowStateFingerprintV1{
		Version: execution.Version, Status: execution.Status, CompletionSeq: execution.CompletionSeq,
		ErrorCode: execution.ErrorCode, StateJSON: execution.StateJSON, VarsJSON: execution.VarsJSON,
		WaitsJSON: execution.WaitsJSON, CompensationJSON: execution.CompensationJSON,
		BudgetJSON: execution.BudgetJSON, ThreadSnapshotHash: execution.ThreadSnapshotHash,
	})
}

func runtimeExecutionProvenanceFrozen(status string) bool {
	switch strings.TrimSpace(status) {
	case model.RunStatusCompleted, model.RunStatusFailed, model.RunStatusCancelled:
		return true
	default:
		return false
	}
}

func runtimeExecutionRevisionRef(ref model.ResourceRef) *RuntimeResourceRevisionRefV1 {
	if strings.TrimSpace(ref.ID) == "" {
		return nil
	}
	return &RuntimeResourceRevisionRefV1{
		Kind: strings.TrimSpace(ref.Kind), ID: strings.TrimSpace(ref.ID),
		Revision: strings.TrimSpace(ref.Revision),
	}
}

func runtimeExecutionModelRouting(run model.Run) RuntimeModelRoutingSummaryV1 {
	return RuntimeModelRoutingSummaryV1{
		RequestedModelName: strings.TrimSpace(run.RequestedModelName),
		PlatformModelName:  strings.TrimSpace(run.PlatformModelName),
		Provider:           strings.TrimSpace(run.Provider),
		ProviderProtocol:   strings.TrimSpace(run.ProviderProtocol),
		RoutedBindingCode:  strings.TrimSpace(run.RoutedBindingCode),
		ModelVendor:        strings.TrimSpace(run.ModelVendor),
		UpstreamModelName:  strings.TrimSpace(run.UpstreamModelName),
	}
}

func hashRuntimeExecutionValue(value interface{}) (string, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return hashRuntimeExecutionBytes(raw), nil
}

func hashRuntimeExecutionBytes(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
