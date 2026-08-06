package queue

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/kernel"
)

const CapabilityQueue kernel.Capability = "queue.delivery"

var (
	ErrInvalidInput = errors.New("invalid queue input")
	ErrConflict     = errors.New("queue job conflict")
	ErrNotFound     = errors.New("queue job not found")
	ErrLeaseLost    = errors.New("queue lease lost")
	ErrLeaseExpired = errors.New("queue lease expired")
	ErrJobTerminal  = errors.New("queue job is terminal")
)

// Status is the durable delivery state of one Job.
type Status string

const (
	StatusQueued     Status = "queued"
	StatusLeased     Status = "leased"
	StatusCompleted  Status = "completed"
	StatusDeadLetter Status = "dead_letter"
)

// Policy defines bounded delivery and retry behavior.
type Policy struct {
	MaxAttempts       int           `json:"maxAttempts"`
	VisibilityTimeout time.Duration `json:"visibilityTimeout"`
	InitialBackoff    time.Duration `json:"initialBackoff"`
	MaxBackoff        time.Duration `json:"maxBackoff"`
	BackoffMultiplier int           `json:"backoffMultiplier"`
}

// PrepareEnqueue validates one request and returns the canonical initial Job.
// Adapters must call this before their atomic insert operation.
func PrepareEnqueue(request EnqueueRequest, now time.Time) (Job, error) {
	normalized, err := normalizeEnqueueRequest(request, now.UTC())
	if err != nil {
		return Job{}, err
	}
	jobID, fingerprint, err := jobIdentity(normalized)
	if err != nil {
		return Job{}, err
	}
	return Job{
		ID: jobID, Queue: normalized.Queue, ClientJobID: normalized.ClientJobID,
		Fingerprint: fingerprint, Kind: normalized.Kind, Payload: cloneJSON(normalized.Payload),
		Priority: normalized.Priority, Policy: normalized.Policy,
		Status: StatusQueued, AvailableAt: normalized.AvailableAt.UTC(),
		CreatedAt: now.UTC(), UpdatedAt: now.UTC(),
	}, nil
}

// Lease is one monotonic claim generation.
type Lease struct {
	ID         string    `json:"id"`
	WorkerID   string    `json:"workerID"`
	Generation uint64    `json:"generation"`
	Attempt    int       `json:"attempt"`
	ClaimedAt  time.Time `json:"claimedAt"`
	ExpiresAt  time.Time `json:"expiresAt"`
}

// Job is one immutable request plus mutable delivery state.
type Job struct {
	ID            string          `json:"id"`
	Queue         string          `json:"queue"`
	ClientJobID   string          `json:"clientJobID"`
	Fingerprint   string          `json:"fingerprint"`
	Kind          string          `json:"kind"`
	Payload       json.RawMessage `json:"payload"`
	Priority      int             `json:"priority"`
	Policy        Policy          `json:"policy"`
	Status        Status          `json:"status"`
	Attempt       int             `json:"attempt"`
	Generation    uint64          `json:"generation"`
	AvailableAt   time.Time       `json:"availableAt"`
	Lease         *Lease          `json:"lease,omitempty"`
	LastErrorCode string          `json:"lastErrorCode,omitempty"`
	LastError     string          `json:"lastError,omitempty"`
	CreatedAt     time.Time       `json:"createdAt"`
	UpdatedAt     time.Time       `json:"updatedAt"`
	CompletedAt   *time.Time      `json:"completedAt,omitempty"`
	DeadLetterAt  *time.Time      `json:"deadLetterAt,omitempty"`
}

// EnqueueRequest creates or reuses one stable Job identity.
type EnqueueRequest struct {
	Queue       string
	ClientJobID string
	Kind        string
	Payload     json.RawMessage
	Priority    int
	AvailableAt time.Time
	Policy      Policy
}

// EnqueueResult reports whether an existing identical Job was reused.
type EnqueueResult struct {
	Job    Job
	Reused bool
}

// ClaimRequest claims up to Limit eligible jobs for one worker.
type ClaimRequest struct {
	Queue    string
	WorkerID string
	Limit    int
}

// Delivery is one leased Job snapshot.
type Delivery struct {
	Job   Job
	Lease Lease
}

// LeaseRequest targets the current lease generation.
type LeaseRequest struct {
	JobID    string
	LeaseID  string
	WorkerID string
}

// NackRequest releases one delivery for retry or dead-lettering.
type NackRequest struct {
	LeaseRequest
	ErrorCode string
	Error     string
}

// normalizePolicy freezes safe defaults and upper bounds.
func normalizePolicy(policy Policy) Policy {
	if policy.MaxAttempts <= 0 {
		policy.MaxAttempts = 5
	}
	if policy.VisibilityTimeout <= 0 {
		policy.VisibilityTimeout = 30 * time.Second
	}
	if policy.InitialBackoff < 0 {
		policy.InitialBackoff = 0
	}
	if policy.MaxBackoff <= 0 {
		policy.MaxBackoff = 5 * time.Minute
	}
	if policy.BackoffMultiplier <= 1 {
		policy.BackoffMultiplier = 2
	}
	return policy
}

func validPolicy(policy Policy) bool {
	return policy.MaxAttempts > 0 && policy.MaxAttempts <= 1_000 &&
		policy.VisibilityTimeout > 0 && policy.VisibilityTimeout <= 24*time.Hour &&
		policy.InitialBackoff >= 0 && policy.MaxBackoff >= policy.InitialBackoff &&
		policy.MaxBackoff <= 7*24*time.Hour && policy.BackoffMultiplier >= 2 && policy.BackoffMultiplier <= 100
}

func normalizeEnqueueRequest(request EnqueueRequest, now time.Time) (EnqueueRequest, error) {
	request.Queue = strings.TrimSpace(request.Queue)
	request.ClientJobID = strings.TrimSpace(request.ClientJobID)
	request.Kind = strings.TrimSpace(request.Kind)
	request.Payload = normalizeJSON(request.Payload, json.RawMessage(`null`))
	request.Policy = normalizePolicy(request.Policy)
	if request.AvailableAt.IsZero() {
		request.AvailableAt = now
	}
	if request.Queue == "" || request.ClientJobID == "" || request.Kind == "" ||
		!json.Valid(request.Payload) || request.Priority < -1_000 || request.Priority > 1_000 || !validPolicy(request.Policy) {
		return EnqueueRequest{}, ErrInvalidInput
	}
	return request, nil
}

func jobIdentity(request EnqueueRequest) (string, string, error) {
	material := struct {
		Queue    string          `json:"queue"`
		ClientID string          `json:"clientID"`
		Kind     string          `json:"kind"`
		Payload  json.RawMessage `json:"payload"`
		Priority int             `json:"priority"`
		Policy   Policy          `json:"policy"`
	}{
		Queue: request.Queue, ClientID: request.ClientJobID, Kind: request.Kind,
		Payload: request.Payload, Priority: request.Priority, Policy: request.Policy,
	}
	encoded, err := json.Marshal(material)
	if err != nil {
		return "", "", errors.Join(ErrInvalidInput, err)
	}
	jobID := stableID("job", request.Queue, request.ClientJobID)
	return jobID, hashBytes(encoded), nil
}

func normalizeJSON(value json.RawMessage, fallback json.RawMessage) json.RawMessage {
	if len(value) == 0 {
		value = fallback
	}
	var normalized any
	if err := json.Unmarshal(value, &normalized); err != nil {
		return cloneJSON(value)
	}
	encoded, err := json.Marshal(normalized)
	if err != nil {
		return cloneJSON(value)
	}
	return encoded
}

func stableID(prefix string, values ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(values, "\x1f")))
	return prefix + "_" + hex.EncodeToString(sum[:])[:32]
}

func hashBytes(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}

func cloneJSON(value json.RawMessage) json.RawMessage {
	return append(json.RawMessage(nil), value...)
}

func cloneJob(job Job) Job {
	job.Payload = cloneJSON(job.Payload)
	if job.Lease != nil {
		lease := *job.Lease
		job.Lease = &lease
	}
	if job.CompletedAt != nil {
		completedAt := *job.CompletedAt
		job.CompletedAt = &completedAt
	}
	if job.DeadLetterAt != nil {
		deadLetterAt := *job.DeadLetterAt
		job.DeadLetterAt = &deadLetterAt
	}
	return job
}
