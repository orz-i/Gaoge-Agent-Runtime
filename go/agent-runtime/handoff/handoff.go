package handoff

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

const CapabilityCoordinator kernel.Capability = "handoff.coordinator"

var (
	ErrInvalidDelegation = errors.New("invalid handoff delegation")
	ErrChildUnavailable  = errors.New("handoff child runner is unavailable")
	ErrInvalidJoin       = errors.New("invalid handoff join")
	ErrChildPending      = errors.New("handoff child run is not terminal")
	ErrChildFailed       = errors.New("handoff child run failed")
	ErrJoinPending       = errors.New("handoff join is not ready")
	ErrJoinFailed        = errors.New("handoff join failed")
)

// Status is the durable lifecycle of one delegated Child Run.
type Status string

const (
	StatusQueued    Status = "queued"
	StatusRunning   Status = "running"
	StatusCompleted Status = "completed"
	StatusFailed    Status = "failed"
	StatusCancelled Status = "cancelled"
)

// JoinMode defines the success threshold for a fixed delegation set.
type JoinMode string

const (
	JoinAll    JoinMode = "all"
	JoinAny    JoinMode = "any"
	JoinQuorum JoinMode = "quorum"
)

// FailurePolicy defines whether one child failure immediately fails the Join.
type FailurePolicy string

const (
	FailureCollect  FailurePolicy = "collect"
	FailureFailFast FailurePolicy = "fail_fast"
)

// JoinStatus is the monotonic status of one fan-in decision.
type JoinStatus string

const (
	JoinPending JoinStatus = "pending"
	JoinReady   JoinStatus = "ready"
	JoinFailed  JoinStatus = "failed"
)

// Delegation is a durable parent-owned reference to one stable Child Agent Run.
type Delegation struct {
	ID           string          `json:"id"`
	MemberID     string          `json:"memberID"`
	RoleID       string          `json:"roleID,omitempty"`
	RoleRevision uint64          `json:"roleRevision,omitempty"`
	RoleName     string          `json:"roleName,omitempty"`
	Instructions string          `json:"instructions,omitempty"`
	Limits       agent.Limits    `json:"limits,omitempty"`
	ChildRunID   string          `json:"childRunID"`
	Goal         string          `json:"goal"`
	Model        string          `json:"model,omitempty"`
	ModelOptions json.RawMessage `json:"modelOptions,omitempty"`
	ToolKeys     []string        `json:"toolKeys,omitempty"`
	Status       Status          `json:"status"`
	Result       json.RawMessage `json:"result,omitempty"`
	ErrorCode    string          `json:"errorCode,omitempty"`
	Error        string          `json:"error,omitempty"`
}

// Join describes a deterministic fan-in contract over stable Delegation IDs.
type Join struct {
	Mode          JoinMode      `json:"mode"`
	Quorum        int           `json:"quorum"`
	FailurePolicy FailurePolicy `json:"failurePolicy"`
	Status        JoinStatus    `json:"status"`
	Completed     int           `json:"completed"`
	Failed        int           `json:"failed"`
	Cancelled     int           `json:"cancelled"`
	Pending       int           `json:"pending"`
	ResultIDs     []string      `json:"resultIDs,omitempty"`
	ErrorCode     string        `json:"errorCode,omitempty"`
	Error         string        `json:"error,omitempty"`
}

// ChildRunner is the narrow direct Agent capability consumed by Handoff.
type ChildRunner interface {
	StartRun(context.Context, agent.StartRequest) (kernel.Snapshot, error)
	LoadRun(context.Context, string) (kernel.Snapshot, error)
}

// ChildRunnerResolver selects one explicitly composed child capability for a
// durable Delegation. It is a narrow routing port, not a service locator: the
// host owns the finite routing policy and returns an already constructed
// runner.
type ChildRunnerResolver interface {
	ResolveChild(context.Context, Delegation) (ChildRunner, error)
}

// ChildRunnerResolverFunc adapts one host-owned routing function.
type ChildRunnerResolverFunc func(context.Context, Delegation) (ChildRunner, error)

func (resolver ChildRunnerResolverFunc) ResolveChild(
	ctx context.Context,
	delegation Delegation,
) (ChildRunner, error) {
	if resolver == nil {
		return nil, ErrChildUnavailable
	}
	return resolver(ctx, delegation)
}

type staticChildRunnerResolver struct{ children ChildRunner }

func (resolver staticChildRunnerResolver) ResolveChild(context.Context, Delegation) (ChildRunner, error) {
	if resolver.children == nil {
		return nil, ErrChildUnavailable
	}
	return resolver.children, nil
}

// Coordinator starts or recovers stable delegated Child Agent Runs.
type Coordinator struct {
	resolver ChildRunnerResolver
	requires []kernel.Capability
}

// New constructs the Handoff capability without owning a root Run.
func New(children ChildRunner) (*Coordinator, error) {
	if children == nil {
		return nil, ErrInvalidDelegation
	}
	return &Coordinator{
		resolver: staticChildRunnerResolver{children: children},
		requires: []kernel.Capability{agent.CapabilityRunner},
	}, nil
}

// NewRouted constructs Handoff with a finite host-owned child routing policy.
// Unlike New, the resolver's dependencies are already explicitly composed, so
// the coordinator does not claim that every route requires agent.runner.
func NewRouted(resolver ChildRunnerResolver) (*Coordinator, error) {
	if resolver == nil {
		return nil, ErrInvalidDelegation
	}
	return &Coordinator{resolver: resolver}, nil
}

// Descriptor declares the reusable Handoff capability.
func (coordinator *Coordinator) Descriptor() kernel.FeatureDescriptor {
	return kernel.FeatureDescriptor{
		Name:     "handoff",
		Requires: append([]kernel.Capability(nil), coordinator.requires...),
		Provides: []kernel.Capability{CapabilityCoordinator},
	}
}

// StartOrLoad returns the existing Child Run or starts it with the persisted ID.
func (coordinator *Coordinator) StartOrLoad(
	ctx context.Context,
	parent kernel.Snapshot,
	delegation Delegation,
) (Delegation, error) {
	if coordinator == nil || coordinator.resolver == nil || !validDelegation(delegation) || parent.Run.ID == "" {
		return Delegation{}, ErrInvalidDelegation
	}
	children, err := coordinator.resolveChild(ctx, delegation)
	if err != nil {
		return Delegation{}, err
	}
	child, err := children.LoadRun(ctx, delegation.ChildRunID)
	if err == nil {
		return projectChild(delegation, child), childStateError(child)
	}
	if !errors.Is(err, kernel.ErrNotFound) {
		return Delegation{}, err
	}
	child, err = children.StartRun(ctx, agent.StartRequest{
		ID:           delegation.ChildRunID,
		Actor:        parent.Run.Actor,
		Thread:       parent.Run.Thread,
		RequestID:    parent.Run.ID + ":" + delegation.ID,
		Goal:         delegation.Goal,
		Instructions: delegation.Instructions,
		Limits:       delegation.Limits,
		Model:        delegation.Model,
		ModelOptions: append(json.RawMessage(nil), delegation.ModelOptions...),
		ToolKeys:     append([]string(nil), delegation.ToolKeys...),
	})
	if child.Run.ID == "" {
		return Delegation{}, err
	}
	return projectChild(delegation, child), errors.Join(childStateError(child), err)
}

// Refresh projects the current Child Run into one Delegation without starting it.
func (coordinator *Coordinator) Refresh(ctx context.Context, delegation Delegation) (Delegation, error) {
	if coordinator == nil || coordinator.resolver == nil || !validDelegation(delegation) {
		return Delegation{}, ErrInvalidDelegation
	}
	children, err := coordinator.resolveChild(ctx, delegation)
	if err != nil {
		return Delegation{}, err
	}
	child, err := children.LoadRun(ctx, delegation.ChildRunID)
	if err != nil {
		return Delegation{}, err
	}
	return projectChild(delegation, child), childStateError(child)
}

func (coordinator *Coordinator) resolveChild(ctx context.Context, delegation Delegation) (ChildRunner, error) {
	children, err := coordinator.resolver.ResolveChild(ctx, cloneDelegation(delegation))
	if err != nil {
		return nil, errors.Join(ErrChildUnavailable, err)
	}
	if children == nil {
		return nil, ErrChildUnavailable
	}
	return children, nil
}

// ResolveJoin deterministically projects Delegation status into a monotonic Join decision.
func ResolveJoin(input Join, delegations []Delegation) (Join, error) {
	if !validJoin(input, delegations) {
		return Join{}, ErrInvalidJoin
	}
	if input.Status == JoinReady || input.Status == JoinFailed {
		return cloneJoin(input), nil
	}
	resolved := input
	resolved.Completed, resolved.Failed, resolved.Cancelled, resolved.Pending = 0, 0, 0, 0
	resolved.ResultIDs = resolved.ResultIDs[:0]
	for _, delegation := range delegations {
		countDelegation(&resolved, delegation)
	}
	resolved.Status, resolved.ErrorCode, resolved.Error = resolveJoinStatus(resolved)
	return cloneJoin(resolved), joinStateError(resolved)
}

func countDelegation(join *Join, delegation Delegation) {
	switch delegation.Status {
	case StatusCompleted:
		join.Completed++
		join.ResultIDs = append(join.ResultIDs, delegation.ID)
	case StatusFailed:
		join.Failed++
	case StatusCancelled:
		join.Cancelled++
	case StatusQueued, StatusRunning:
		join.Pending++
	default:
		join.Pending++
	}
}

func resolveJoinStatus(join Join) (JoinStatus, string, string) {
	failures := join.Failed + join.Cancelled
	if join.FailurePolicy == FailureFailFast && failures > 0 {
		return JoinFailed, "handoff.child_failed", "one or more delegated runs failed or were cancelled"
	}
	switch join.Mode {
	case JoinAll:
		return resolveAllJoin(join)
	case JoinAny:
		return resolveAnyJoin(join)
	case JoinQuorum:
		return resolveQuorumJoin(join)
	}
	return JoinPending, "", ""
}

func resolveAllJoin(join Join) (JoinStatus, string, string) {
	if join.Pending != 0 {
		return JoinPending, "", ""
	}
	if join.Completed == 0 && join.Failed+join.Cancelled > 0 {
		return JoinFailed, "handoff.no_success", "no delegated run completed successfully"
	}
	return JoinReady, "", ""
}

func resolveAnyJoin(join Join) (JoinStatus, string, string) {
	if join.Completed > 0 {
		return JoinReady, "", ""
	}
	if join.Pending == 0 {
		return JoinFailed, "handoff.no_success", "no delegated run completed successfully"
	}
	return JoinPending, "", ""
}

func resolveQuorumJoin(join Join) (JoinStatus, string, string) {
	if join.Completed >= join.Quorum {
		return JoinReady, "", ""
	}
	if join.Completed+join.Pending < join.Quorum {
		return JoinFailed, "handoff.quorum_unreachable", "delegated run quorum can no longer be reached"
	}
	return JoinPending, "", ""
}

func projectChild(delegation Delegation, child kernel.Snapshot) Delegation {
	projected := cloneDelegation(delegation)
	projected.Result = nil
	projected.ErrorCode = child.Run.ErrorCode
	projected.Error = child.Run.ErrorDetail
	switch child.Run.Status {
	case kernel.RunStatusCompleted:
		projected.Status = StatusCompleted
		if child.Result != nil {
			projected.Result = append(json.RawMessage(nil), child.Result.Content...)
		}
	case kernel.RunStatusFailed:
		projected.Status = StatusFailed
	case kernel.RunStatusCancelled:
		projected.Status = StatusCancelled
	case kernel.RunStatusRunning, kernel.RunStatusWaitingInput:
		projected.Status = StatusRunning
	default:
		projected.Status = StatusRunning
	}
	return projected
}

func childStateError(child kernel.Snapshot) error {
	switch child.Run.Status {
	case kernel.RunStatusCompleted:
		return nil
	case kernel.RunStatusFailed, kernel.RunStatusCancelled:
		return ErrChildFailed
	case kernel.RunStatusRunning, kernel.RunStatusWaitingInput:
		return ErrChildPending
	default:
		return ErrChildPending
	}
}

func joinStateError(join Join) error {
	switch join.Status {
	case JoinReady:
		return nil
	case JoinFailed:
		return ErrJoinFailed
	case JoinPending:
		return ErrJoinPending
	default:
		return ErrJoinPending
	}
}

func validDelegation(delegation Delegation) bool {
	return strings.TrimSpace(delegation.ID) != "" && strings.TrimSpace(delegation.MemberID) != "" &&
		strings.TrimSpace(delegation.ChildRunID) != "" && strings.TrimSpace(delegation.Goal) != "" &&
		(delegation.Status == StatusQueued || delegation.Status == StatusRunning || delegation.Status == StatusCompleted ||
			delegation.Status == StatusFailed || delegation.Status == StatusCancelled)
}

func validJoin(join Join, delegations []Delegation) bool {
	if len(delegations) == 0 || len(delegations) > 16 ||
		(join.FailurePolicy != FailureCollect && join.FailurePolicy != FailureFailFast) {
		return false
	}
	switch join.Mode {
	case JoinAll, JoinAny:
		return join.Quorum == 1
	case JoinQuorum:
		return join.Quorum > 0 && join.Quorum <= len(delegations)
	default:
		return false
	}
}

func cloneDelegation(delegation Delegation) Delegation {
	delegation.ModelOptions = append(json.RawMessage(nil), delegation.ModelOptions...)
	delegation.ToolKeys = append([]string(nil), delegation.ToolKeys...)
	delegation.Result = append(json.RawMessage(nil), delegation.Result...)
	return delegation
}

func cloneJoin(join Join) Join {
	join.ResultIDs = append([]string(nil), join.ResultIDs...)
	return join
}
