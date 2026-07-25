package domain

import "time"

const (
	ContinuationJobQueued     = "queued"
	ContinuationJobRunning    = "running"
	ContinuationJobRetryWait  = "retry_wait"
	ContinuationJobCompleted  = "completed"
	ContinuationJobDeadLetter = "dead_letter"
)

// ContinuationJob is the durable execution handoff for one committed runtime
// checkpoint. SegmentKey is the idempotency boundary across process restarts.
type ContinuationJob struct {
	JobID, SegmentKey, RunID, CheckpointID string
	Actor                                  ActorRef
	Source, Status                         string
	TraceParent, TraceState                string
	ReservationAmountNanousd               int64
	ReservationRefNo                       string
	AttemptCount, MaxAttempts              int
	AvailableAt                            time.Time
	LeaseOwner                             string
	LeaseExpiresAt, HeartbeatAt            *time.Time
	LastError                              string
	CreatedAt, UpdatedAt                   time.Time
}

type ContinuationJobFilter struct {
	TenantID string
	ActorID  string
	Status   string
	RunID    string
	JobID    string
	Source   string
	Limit    int
	Offset   int
}

type ContinuationJobPage struct {
	Items []ContinuationJob
	Total int64
}
