package domain

import "time"

type WorkbenchProjection struct {
	RunID                      string
	ProjectionVersion          int
	SourcePresentationEventSeq int64
	CreatedAt, UpdatedAt       time.Time
}

type PhaseProjection struct {
	PhaseID, RunID, Kind, Title, Summary, Status string
	StartSeq, EndSeq                             int64
	StepIDsJSON, ToolCallIDsJSON, OutputIDsJSON  string
	StartedAt                                    time.Time
	EndedAt                                      *time.Time
	CreatedAt, UpdatedAt                         time.Time
}

type WorkbenchSnapshot struct {
	Run          Run
	Workflow     *WorkflowExecution
	Result       *RunResult
	Steps        []Step
	Context      *ContextSnapshot
	Plans        []Plan
	Interactions []Interaction
	Checkpoints  []Checkpoint
	Outputs      []OutputRef
	Handoffs     []RunHandoff
	Projection   *WorkbenchProjection
	Phases       []PhaseProjection
	Events       []Event
}

const (
	ActivityContext     = "context"
	ActivityCommentary  = "commentary"
	ActivityPlan        = "plan"
	ActivityOperation   = "operation"
	ActivityStepResult  = "step_result"
	ActivityInteraction = "interaction"
	ActivityError       = "error"
)

type ActivityItem struct {
	ID, Kind, StepID, Status, Title, Summary, ContentMarkdown string
	OperationKind, InteractionType                            string
	Seq, EndSeq                                               int64
	Count                                                     int
	Details                                                   []ActivityDetail
	Sources                                                   []ActivitySource
	Context                                                   *ActivityContextSummary
}

type ActivityDetail struct{ ID, Title, Status, Summary string }
type ActivitySource struct{ Type, ID, Title string }

type ActivityContextSummary struct {
	FileCount, RAGCount, SkillCount, MemoryCount, OutputCount int
	EvidenceCount, RetrievalFallbackCount                     int
	IncludedCount, SkippedCount                               int
	Workspace                                                 *ActivityWorkspaceSummary
}

type ActivityWorkspaceSummary struct {
	ResourceID, SnapshotID, SelectionKind, TargetKind, TargetID  string
	TargetLabel, ActionID, ArtifactContract                      string
	Revision                                                     uint64
	SelectedBlockCount, AdjacentUnitCount, LinkedEntityCount     int
	RelevantFactCount, RelevantRelationCount, AvailableToolCount int
}
