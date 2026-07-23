package domain

import "time"

// Event is the append-only runtime event contract.
type Event struct {
	EventID, RunID, EventType, StepID, Visibility string
	SchemaVersion                                 int
	Actor                                         ActorRef
	Thread                                        ThreadRef
	Projection                                    ProjectionRef
	Phase, Stage, RoundID, ParentEventID, Status  string
	Title, Summary, ContentMarkdown, PayloadJSON  string
	ToolCallID, ToolName                          string
	Seq                                           int64
	LatencyMS                                     int64
	InputJSON, OutputJSON, ErrorJSON              string
	StartedAt                                     time.Time
	EndedAt                                       *time.Time
	CreatedAt                                     time.Time
}
