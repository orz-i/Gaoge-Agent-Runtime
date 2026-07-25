package domain

import (
	"strconv"
	"strings"
	"time"
)

const (
	AgentManifestKind           = "agent_manifest"
	AgentManifestStatusActive   = "active"
	AgentManifestStatusDisabled = "disabled"

	RunHandoffStatusQueued    = "queued"
	RunHandoffStatusCompleted = "completed"
	RunHandoffStatusFailed    = "failed"
	RunHandoffStatusCancelled = "cancelled"

	RunHandoffJoinModeAll    = "all"
	RunHandoffJoinModeAny    = "any"
	RunHandoffJoinModeQuorum = "quorum"

	RunHandoffJoinFailureCollect  = "collect"
	RunHandoffJoinFailureFailFast = "fail_fast"

	RunHandoffJoinTimeoutCancelPending = "cancel_pending"
	RunHandoffJoinTimeoutLeaveRunning  = "leave_running"

	RunHandoffJoinStatusPending   = "pending"
	RunHandoffJoinStatusReady     = "ready"
	RunHandoffJoinStatusFailed    = "failed"
	RunHandoffJoinStatusCancelled = "cancelled"
)

// AgentManifest is one immutable revision of a tenant-visible Agent contract.
// Environment policies remain the authorization ceiling; a manifest may only
// narrow the model, execution mode, tools, and skills used by a delegated Run.
type AgentManifest struct {
	ManifestID         string
	Revision           int
	TenantID           string
	Name               string
	Description        string
	Instructions       string
	Status             string
	ModelName          string
	ExecutionMode      string
	ToolKeys           []string
	SkillRefs          []ResourceRef
	MaxChildRuns       int
	MaxDepth           int
	CreatedBy          ActorRef
	RequestID          string
	RequestFingerprint string
	RevisionNote       string
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

// ExpireRunHandoffJoin applies the frozen deadline to one pending fan-in
// contract. It returns applied=false when the join is terminal or not due.
func ExpireRunHandoffJoin(join RunHandoffJoin, now time.Time) (RunHandoffJoin, bool) {
	if RunHandoffJoinTerminal(join.Status) || join.DeadlineAt == nil || join.DeadlineAt.After(now) {
		return join, false
	}
	join.Status = RunHandoffJoinStatusFailed
	join.ErrorCode = "handoff_join_timeout"
	join.ErrorMessage = "delegated task wait exceeded its deadline"
	join.UpdatedAt = now
	resolvedAt := now
	join.ResolvedAt = &resolvedAt
	return join, true
}

func validRunHandoffJoinTimeout(input RunHandoffJoin) bool {
	if input.TimeoutSeconds == 0 {
		return input.TimeoutPolicy == "" && input.DeadlineAt == nil
	}
	validPolicy := input.TimeoutPolicy == RunHandoffJoinTimeoutCancelPending || input.TimeoutPolicy == RunHandoffJoinTimeoutLeaveRunning
	return validPolicy && input.DeadlineAt != nil
}

func (m AgentManifest) Ref() ResourceRef {
	return ResourceRef{Kind: AgentManifestKind, ID: m.ManifestID, Revision: manifestRevisionString(m.Revision)}
}

type AgentManifestFilter struct {
	Status string
	Limit  int
	Offset int
}

type AgentManifestPage struct {
	Total   int64
	Results []AgentManifest
}

// RunHandoff is the durable delegation boundary between one parent Run and one
// child Run. ClientHandoffID plus RequestFingerprint is the replay boundary.
type RunHandoff struct {
	HandoffID          string
	ClientHandoffID    string
	RequestFingerprint string
	Actor              ActorRef
	RootRunID          string
	ParentRunID        string
	ChildRunID         string
	AgentManifest      ResourceRef
	AgentName          string
	Goal               string
	Status             string
	Depth              int
	InputProjection    ProjectionRef
	ResultSummary      string
	ResultOutputIDs    []string
	ErrorCode          string
	ErrorMessage       string
	CreatedAt          time.Time
	UpdatedAt          time.Time
	CompletedAt        *time.Time
}

type RunHandoffFilter struct {
	RootRunID   string
	ParentRunID string
	ChildRunID  string
	Status      string
	Limit       int
	Offset      int
}

type RunHandoffPage struct {
	Total   int64
	Results []RunHandoff
}

// RunHandoffJoin is the durable fan-in contract for a fixed set of sibling
// handoffs. Its terminal state is monotonic: once ready, failed, or cancelled,
// later child completions cannot reopen or reverse the decision.
type RunHandoffJoin struct {
	JoinID             string
	ClientJoinID       string
	RequestFingerprint string
	Actor              ActorRef
	RootRunID          string
	ParentRunID        string
	HandoffIDs         []string
	ResumeCheckpointID string
	Mode               string
	Quorum             int
	FailurePolicy      string
	TimeoutSeconds     int
	TimeoutPolicy      string
	DeadlineAt         *time.Time
	Status             string
	CompletedCount     int
	FailedCount        int
	CancelledCount     int
	PendingCount       int
	ResultHandoffIDs   []string
	ErrorCode          string
	ErrorMessage       string
	CreatedAt          time.Time
	UpdatedAt          time.Time
	ResolvedAt         *time.Time
}

type RunHandoffJoinFilter struct {
	RootRunID   string
	ParentRunID string
	Status      string
	Limit       int
	Offset      int
}

type RunHandoffJoinPage struct {
	Total   int64
	Results []RunHandoffJoin
}

type RunHandoffCompletionResult struct {
	Handoff       RunHandoff
	ResolvedJoins []RunHandoffJoin
	Reused        bool
}

func ValidRunHandoffJoin(input *RunHandoffJoin) bool {
	return input != nil && validRunHandoffJoinIdentity(*input) && validRunHandoffJoinSelection(*input) && validRunHandoffJoinPolicy(*input)
}

func validRunHandoffJoinIdentity(input RunHandoffJoin) bool {
	for _, value := range []string{
		input.JoinID, input.ClientJoinID, input.RequestFingerprint,
		input.Actor.TenantID, input.Actor.ActorID, input.RootRunID, input.ParentRunID,
	} {
		if strings.TrimSpace(value) == "" {
			return false
		}
	}
	return true
}

func validRunHandoffJoinSelection(input RunHandoffJoin) bool {
	return input.Status == RunHandoffJoinStatusPending && len(input.HandoffIDs) > 0 && len(input.HandoffIDs) <= 16
}

func validRunHandoffJoinPolicy(input RunHandoffJoin) bool {
	if input.FailurePolicy != RunHandoffJoinFailureCollect && input.FailurePolicy != RunHandoffJoinFailureFailFast {
		return false
	}
	if !validRunHandoffJoinTimeout(input) {
		return false
	}
	switch input.Mode {
	case RunHandoffJoinModeAll, RunHandoffJoinModeAny:
		return input.Quorum == 1
	case RunHandoffJoinModeQuorum:
		return input.Quorum > 0 && input.Quorum <= len(input.HandoffIDs)
	default:
		return false
	}
}

// ResolveRunHandoffJoin deterministically projects current child handoff state
// into the join aggregate. Input validation and ownership checks belong to the
// Store transaction; this function owns only the fan-in decision table.
func ResolveRunHandoffJoin(join RunHandoffJoin, handoffs []RunHandoff, now time.Time) RunHandoffJoin {
	if RunHandoffJoinTerminal(join.Status) {
		return join
	}
	statusByID := make(map[string]string, len(handoffs))
	for _, handoff := range handoffs {
		statusByID[handoff.HandoffID] = handoff.Status
	}
	join.CompletedCount, join.FailedCount, join.CancelledCount, join.PendingCount = 0, 0, 0, 0
	join.ResultHandoffIDs = join.ResultHandoffIDs[:0]
	for _, handoffID := range join.HandoffIDs {
		switch statusByID[handoffID] {
		case RunHandoffStatusCompleted:
			join.CompletedCount++
			join.ResultHandoffIDs = append(join.ResultHandoffIDs, handoffID)
		case RunHandoffStatusFailed:
			join.FailedCount++
		case RunHandoffStatusCancelled:
			join.CancelledCount++
		default:
			join.PendingCount++
		}
	}
	join.Status, join.ErrorCode, join.ErrorMessage = resolveRunHandoffJoinStatus(join)
	join.UpdatedAt = now
	if RunHandoffJoinTerminal(join.Status) {
		resolvedAt := now
		join.ResolvedAt = &resolvedAt
	}
	return join
}

func resolveRunHandoffJoinStatus(join RunHandoffJoin) (string, string, string) {
	failures := join.FailedCount + join.CancelledCount
	if join.FailurePolicy == RunHandoffJoinFailureFailFast && failures > 0 {
		return RunHandoffJoinStatusFailed, "handoff_join_child_failed", "one or more joined child runs failed or were cancelled"
	}
	switch join.Mode {
	case RunHandoffJoinModeAll:
		return resolveAllRunHandoffJoin(join)
	case RunHandoffJoinModeAny:
		return resolveAnyRunHandoffJoin(join)
	case RunHandoffJoinModeQuorum:
		return resolveQuorumRunHandoffJoin(join)
	}
	return RunHandoffJoinStatusPending, "", ""
}

func resolveAllRunHandoffJoin(join RunHandoffJoin) (string, string, string) {
	if join.PendingCount == 0 {
		return RunHandoffJoinStatusReady, "", ""
	}
	return RunHandoffJoinStatusPending, "", ""
}

func resolveAnyRunHandoffJoin(join RunHandoffJoin) (string, string, string) {
	if join.CompletedCount > 0 {
		return RunHandoffJoinStatusReady, "", ""
	}
	if join.PendingCount == 0 {
		return RunHandoffJoinStatusFailed, "handoff_join_no_success", "no joined child run completed successfully"
	}
	return RunHandoffJoinStatusPending, "", ""
}

func resolveQuorumRunHandoffJoin(join RunHandoffJoin) (string, string, string) {
	if join.CompletedCount >= join.Quorum {
		return RunHandoffJoinStatusReady, "", ""
	}
	if join.CompletedCount+join.PendingCount < join.Quorum {
		return RunHandoffJoinStatusFailed, "handoff_join_quorum_unreachable", "joined child run quorum can no longer be reached"
	}
	return RunHandoffJoinStatusPending, "", ""
}

func RunHandoffJoinTerminal(status string) bool {
	return status == RunHandoffJoinStatusReady || status == RunHandoffJoinStatusFailed || status == RunHandoffJoinStatusCancelled
}

// CancelRunHandoffJoin transitions one pending fan-in contract to a stable
// cancelled terminal state. Terminal joins are immutable and returned as-is.
func CancelRunHandoffJoin(join RunHandoffJoin, now time.Time, code, message string) RunHandoffJoin {
	if RunHandoffJoinTerminal(join.Status) {
		return join
	}
	if now.IsZero() {
		now = time.Now()
	}
	join.Status = RunHandoffJoinStatusCancelled
	join.ErrorCode = strings.TrimSpace(code)
	join.ErrorMessage = strings.TrimSpace(message)
	join.UpdatedAt = now
	resolvedAt := now
	join.ResolvedAt = &resolvedAt
	return join
}

type RunHandoffCompletion struct {
	Status          string
	ResultSummary   string
	ResultOutputIDs []string
	ErrorCode       string
	ErrorMessage    string
	CompletedAt     time.Time
}

func manifestRevisionString(revision int) string {
	if revision <= 0 {
		return ""
	}
	return strconv.Itoa(revision)
}
