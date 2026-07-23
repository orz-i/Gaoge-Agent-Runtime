package domain

import "time"

const (
	QueueQueued      = "queued"
	QueueDispatching = "dispatching"
	QueueStarted     = "started"
	QueueFailed      = "failed"
	QueueCancelled   = "cancelled"
)

type QueueItem struct {
	QueueID, ClientQueueID, RequestFingerprint string
	Actor                                      ActorRef
	Thread                                     ThreadRef
	Status                                     string
	Position, Revision, AttemptCount           int
	RequestJSON                                string
	AnchorProjection                           ProjectionRef
	AnchorRunID, StartedRunID                  string
	ErrorCode, ErrorMessage                    string
	NextAttemptAt                              *time.Time
	CreatedAt, UpdatedAt                       time.Time
}
