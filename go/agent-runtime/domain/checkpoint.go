package domain

import "time"

const (
	CheckpointReady      = "ready"
	CheckpointConsumed   = "consumed"
	CheckpointSuperseded = "superseded"
)

type Checkpoint struct {
	CheckpointID, RunID, ParentCheckpointID string
	EventSeq                                int64
	StepID, ToolCallID                      string
	ContextSnapshotID                       string
	ContextHash, ManifestHash, Kind, Status string
	ResumeStateJSON                         string
	ResumeRequestID, ResumeFingerprint      string
	CreatedAt, UpdatedAt                    time.Time
}
