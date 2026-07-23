package domain

import "time"

const (
	OutputDraft     = "draft"
	OutputPublished = "published"
	OutputAborted   = "aborted"
)

type OutputIdentity struct {
	OutputID, Kind string
	Actor          ActorRef
	CreatedAt      time.Time
}

type OutputRef struct {
	OutputID, RunID, StepID, ToolCallID       string
	SourceToolCallID, SourceEventID           string
	Kind, Title, Summary, FileID, PreviewJSON string
	Projection                                ProjectionRef
	Version                                   int
	ParentOutputID, Status, SourceSnapshotID  string
	FileSHA256, FileMIMEType                  string
	CreatedAt, UpdatedAt                      time.Time
}

type OutputListItem struct {
	OutputRef
	SourceRunGoal, SourceRunStatus, SourceRunModel string
	Thread                                         ThreadRef
	ThreadTitle                                    string
}
