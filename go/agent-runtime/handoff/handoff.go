package handoff

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

// RemoteAgentRunKind is the protocol-neutral remote-Agent shadow RunKind. Handoff may
// consume an Agent Run or this explicitly bound remote Agent proxy, but never
// an unrelated custom Kernel state machine.
const RemoteAgentRunKind kernel.RunKind = "agent.remote"

var (
	ErrInvalidDelegation = errors.New("invalid handoff delegation")
	ErrChildUnavailable  = errors.New("handoff child runner is unavailable")
	ErrInvalidJoin       = errors.New("invalid handoff join")
	ErrChildPending      = errors.New("handoff child run is not terminal")
	ErrChildFailed       = errors.New("handoff child run failed")
	// ErrChildRunUnverified means a ChildRunner snapshot does not prove the
	// requested Run identity and a recognized Kernel lifecycle state.
	ErrChildRunUnverified = errors.New("handoff child run snapshot is unverified")
	ErrJoinPending        = errors.New("handoff join is not ready")
	ErrJoinFailed         = errors.New("handoff join failed")
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
	ID             string       `json:"id"`
	MemberID       string       `json:"memberID"`
	MemberRevision string       `json:"memberRevision,omitempty"`
	Remote         bool         `json:"remote,omitempty"`
	Instructions   string       `json:"instructions,omitempty"`
	Limits         agent.Limits `json:"limits,omitempty"`
	// Full Host-selected subtree ceilings belong to the shared ledger. The
	// Agent runner accepts only local model/tool ceilings in Limits.
	SubtreeLimits        *budget.Limits          `json:"subtreeLimits,omitempty"`
	ChildRunID           string                  `json:"childRunID"`
	Goal                 string                  `json:"goal"`
	Model                string                  `json:"model,omitempty"`
	ModelOptions         json.RawMessage         `json:"modelOptions,omitempty"`
	OutputContract       *model.OutputContract   `json:"outputContract,omitempty"`
	MaxModelOutputTokens int64                   `json:"maxModelOutputTokens,omitempty"`
	ToolKeys             []string                `json:"toolKeys,omitempty"`
	InitialToolKeys      []string                `json:"initialToolKeys,omitempty"`
	HostedToolGrants     []agent.HostedToolGrant `json:"hostedToolGrants,omitempty"`
	Status               Status                  `json:"status"`
	Result               json.RawMessage         `json:"result,omitempty"`
	ErrorCode            string                  `json:"errorCode,omitempty"`
	Error                string                  `json:"error,omitempty"`
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

// ChildRunner is the narrow Agent Run capability consumed by Handoff.
type ChildRunner interface {
	StartRun(context.Context, agent.StartRequest) (kernel.Snapshot, error)
	LoadRun(context.Context, string) (kernel.Snapshot, error)
}

// RemoteShadowObserver is a first-party, read-only route-binding proof.
// A generic ChildRunner or a same-ID remote Kernel twin cannot implement this
// contract accidentally: the plugin must verify its original Kernel instance,
// frozen target revision and persisted remote discovery before accepting it.
type RemoteShadowObserver interface {
	ChildRunner
	// A single original executor owns the frozen binding, its Kernel, and
	// control. Merely returning a remote-kind Run cannot satisfy this port.
	RemoteRuntime() *kernel.Runtime
	MatchesRemoteDelegation(Delegation) bool
	ObservesFrozenRemoteShadow(*kernel.Runtime, Delegation, kernel.Snapshot) bool
	CancelRemote(context.Context, string, uint64) (kernel.Snapshot, error)
}

// LocalKernelChildRunner is an explicit Host contract that its ChildRunner
// executes directly in the supplied Kernel Runtime, not in a remote ChildRunner
// or A2A shadow. Only a fixed New(children) Coordinator can use this proof;
// custom route implementations must never claim it based on Run metadata.
type LocalKernelChildRunner interface {
	ChildRunner
	OwnsKernelRuntime(*kernel.Runtime) bool
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
	resolver      ChildRunnerResolver
	deferredAgent *agent.Runner
}

// New constructs the Handoff capability without owning a root Run.
func New(children ChildRunner) (*Coordinator, error) {
	if children == nil {
		return nil, ErrInvalidDelegation
	}
	return &Coordinator{
		resolver: staticChildRunnerResolver{children: children},
	}, nil
}

// NewDeferredAgent binds one concrete local Agent while leaving physical
// continuation to the caller's existing worker. PrepareRun may execute Host
// middleware; it does not dispatch Model or Tool work. Observation, continuation,
// and cancellation retain New's separate identity and ownership checks.
// This is a static local composition, not proof about any routed/remote child.
func NewDeferredAgent(original *agent.Runner) (*Coordinator, error) {
	if original == nil {
		return nil, ErrInvalidDelegation
	}
	coordinator, err := New(original)
	if err != nil {
		return nil, err
	}
	coordinator.deferredAgent = original
	return coordinator, nil
}

// NewRouted constructs Handoff with a finite host-owned child routing policy.
// The Host explicitly owns the resolver and concrete ChildRunner dependencies.
func NewRouted(resolver ChildRunnerResolver) (*Coordinator, error) {
	if resolver == nil {
		return nil, ErrInvalidDelegation
	}
	return &Coordinator{resolver: resolver}, nil
}

// OwnsLocalKernelRunner is a narrow composition proof, not an identity
// comparison of Kernel Runs. Only New's permanently bound ChildRunner may
// certify ownership of the exact Kernel used by Harness. A dynamically
// resolved route (NewRouted), a custom HandoffStarter or a ChildRunner that
// cannot attest to its runtime must fail closed during local cancellation.
// The Host remains responsible for keeping static routes stable for the
// lifetime of admitted delegated children, including process recovery.
func (coordinator *Coordinator) OwnsLocalKernelRunner(runtime *kernel.Runtime) bool {
	if coordinator == nil || runtime == nil {
		return false
	}
	fixed, ok := coordinator.resolver.(staticChildRunnerResolver)
	if !ok {
		return false
	}
	owner, ok := fixed.children.(LocalKernelChildRunner)
	return ok && owner.OwnsKernelRuntime(runtime)
}

// ObservesLocalKernelRunner is read-only ownership evidence for the fixed
// ChildRunner's LoadRun. Unlike cancellation ownership, a first-party Agent
// may attest to its Kernel even with Host middleware: observation itself
// does not allow Kernel cancellation or continued physical execution.
// Dynamically resolved or custom routes do not have stable ownership proof.
func (coordinator *Coordinator) ObservesLocalKernelRunner(runtime *kernel.Runtime) bool {
	if coordinator == nil || runtime == nil {
		return false
	}
	fixed, ok := coordinator.resolver.(staticChildRunnerResolver)
	if !ok {
		return false
	}
	if child, ok := fixed.children.(*agent.Runner); ok {
		return child.ObservesKernelRuntime(runtime)
	}
	owner, ok := fixed.children.(LocalKernelChildRunner)
	return ok && owner.OwnsKernelRuntime(runtime)
}

// UsesLocalAgentRunner proves physical continuation will use the original
// fixed first-party Agent, not another runner sharing its Kernel. Read-only
// ownership alone cannot authorize a substitute Model/Tool/middleware route.
func (coordinator *Coordinator) UsesLocalAgentRunner(runtime *kernel.Runtime, candidate *agent.Runner) bool {
	if coordinator == nil || runtime == nil || candidate == nil {
		return false
	}
	fixed, ok := coordinator.resolver.(staticChildRunnerResolver)
	if !ok {
		return false
	}
	original, ok := fixed.children.(*agent.Runner)
	return ok && original == candidate && candidate.ObservesKernelRuntime(runtime)
}

// StartOrLoad returns the existing Child Run or starts it with the persisted ID.
func (coordinator *Coordinator) StartOrLoad(
	ctx context.Context,
	parent kernel.Snapshot,
	delegation Delegation,
) (Delegation, error) {
	if coordinator == nil || coordinator.resolver == nil || !validDelegation(delegation) || !validParentOrigin(parent.Run) {
		return Delegation{}, ErrInvalidDelegation
	}
	delegation, err := boundDelegationModelOutput(parent, delegation)
	if err != nil {
		return Delegation{}, err
	}
	children, err := coordinator.resolveChild(ctx, delegation)
	if err != nil {
		return Delegation{}, err
	}
	if err = verifyRemoteBeforeObservation(ctx, children, delegation, parent.Run); err != nil {
		return Delegation{}, err
	}
	child, err := children.LoadRun(ctx, delegation.ChildRunID)
	if err == nil {
		if !verifiedRoutedChild(children, delegation, parent.Run, child) {
			return Delegation{}, ErrChildRunUnverified
		}
		return projectChild(delegation, child), childStateError(child)
	}
	if !errors.Is(err, kernel.ErrNotFound) {
		return Delegation{}, err
	}
	start := children.StartRun
	if coordinator.deferredAgent != nil {
		start = coordinator.deferredAgent.PrepareRun
	}
	childCtx := ctx
	childDeadline := parent.Run.DeadlineAt
	if delegation.Remote {
		// A remote Agent cannot prove that it enforces a native Kernel
		// DeadlineAt. Preserve the parent's physical-dispatch deadline on
		// the local transport context, without claiming remote capability.
		childDeadline = nil
		if parent.Run.DeadlineAt != nil {
			var cancel context.CancelFunc
			childCtx, cancel = context.WithDeadline(ctx, *parent.Run.DeadlineAt)
			defer cancel()
		}
		if err := childCtx.Err(); err != nil {
			return Delegation{}, err // No new remote execution after expiry/revocation.
		}
	}
	child, err = start(childCtx, agent.StartRequest{
		ID:                   delegation.ChildRunID,
		Actor:                parent.Run.Actor,
		Thread:               parent.Run.Thread,
		RequestID:            parent.Run.ID + ":" + delegation.ID,
		Goal:                 delegation.Goal,
		DeadlineAt:           childDeadline,
		Instructions:         delegation.Instructions,
		Limits:               delegation.Limits,
		Model:                delegation.Model,
		ModelOptions:         append(json.RawMessage(nil), delegation.ModelOptions...),
		OutputContract:       model.CloneOutputContract(delegation.OutputContract),
		MaxModelOutputTokens: delegation.MaxModelOutputTokens,
		ToolKeys:             append([]string(nil), delegation.ToolKeys...),
		InitialToolKeys:      append([]string(nil), delegation.InitialToolKeys...),
		HostedToolGrants:     append([]agent.HostedToolGrant(nil), delegation.HostedToolGrants...),
		DisableInputs:        !delegation.Remote,
	})
	if child.Run.ID == "" && err != nil {
		return Delegation{}, err
	}
	if !verifiedRoutedChild(children, delegation, parent.Run, child) {
		return Delegation{}, errors.Join(ErrChildRunUnverified, err)
	}
	return projectChild(delegation, child), errors.Join(childStateError(child), err)
}

// Refresh projects one Child Run without starting it. The caller must supply
// the authoritative parent Run: the Delegation alone does not freeze an
// Actor/Thread proof and cannot authorize an unrelated Run on lookup.
func (coordinator *Coordinator) Refresh(ctx context.Context, parent kernel.Snapshot, delegation Delegation) (Delegation, error) {
	if coordinator == nil || coordinator.resolver == nil || !validDelegation(delegation) || !validParentOrigin(parent.Run) {
		return Delegation{}, ErrInvalidDelegation
	}
	delegation, err := boundDelegationModelOutput(parent, delegation)
	if err != nil {
		return Delegation{}, err
	}
	children, err := coordinator.resolveChild(ctx, delegation)
	if err != nil {
		return Delegation{}, err
	}
	if err = verifyRemoteBeforeObservation(ctx, children, delegation, parent.Run); err != nil {
		return Delegation{}, err
	}
	child, err := children.LoadRun(ctx, delegation.ChildRunID)
	if err != nil {
		return Delegation{}, err
	}
	if !verifiedRoutedChild(children, delegation, parent.Run, child) {
		return Delegation{}, ErrChildRunUnverified
	}
	return projectChild(delegation, child), childStateError(child)
}

// A Delegation's ChildRunID comes from the request, not the ChildRunner's
// snapshot. Never let projection copy that requested ID into a seemingly
// verified receipt for another (or an invalid) physical Run.
func validParentOrigin(parent kernel.Run) bool {
	return strings.TrimSpace(parent.ID) != "" &&
		strings.TrimSpace(parent.Actor.TenantID) != "" && strings.TrimSpace(parent.Actor.ActorID) != "" &&
		strings.TrimSpace(parent.Thread.Kind) != "" && strings.TrimSpace(parent.Thread.ID) != ""
}

// VerifiedChildRun is the shared frozen task-and-origin check for Handoff
// projections and Harness cancellation. A local Kernel Run with the same ID
// is not proof of ChildRunner ownership without these exact identities.
// Kernel Create canonicalizes ID, Goal and RequestID; compare against that
// frozen request representation, without normalizing returned snapshots or
// Actor/Thread identities (which Kernel preserves exactly).
func VerifiedChildRun(delegation Delegation, parent kernel.Run, child kernel.Snapshot) bool {
	if !validParentOrigin(parent) || !validDelegation(delegation) ||
		child.Run.ID != strings.TrimSpace(delegation.ChildRunID) ||
		child.Run.Actor != parent.Actor || child.Run.Thread != parent.Thread ||
		child.Run.Goal != strings.TrimSpace(delegation.Goal) || child.Run.RequestID != strings.TrimSpace(parent.ID+":"+delegation.ID) ||
		(child.Run.Kind != agent.RunKind && child.Run.Kind != RemoteAgentRunKind) {
		return false
	}
	if child.Run.Kind == RemoteAgentRunKind && !RemoteGoalOnly(delegation) {
		return false // Remote protocol cannot attest local sampling or structured output.
	}
	if child.Run.Kind == agent.RunKind && delegation.Remote {
		return false // A native Run can never prove a Host-selected remote effect.
	}
	if child.Run.Kind == agent.RunKind &&
		(delegation.MaxModelOutputTokens > 0 || delegation.OutputContract != nil) {
		view, err := agent.ViewState(child)
		if err != nil || delegation.MaxModelOutputTokens > 0 && view.MaxModelOutputTokens != delegation.MaxModelOutputTokens ||
			!reflect.DeepEqual(view.OutputContract, delegation.OutputContract) {
			return false
		}
	}
	switch child.Run.Status {
	case kernel.RunStatusRunning, kernel.RunStatusWaitingInput, kernel.RunStatusPausedBudget,
		kernel.RunStatusCompleted, kernel.RunStatusFailed, kernel.RunStatusCancelled:
		return true
	default:
		return false
	}
}

// verifiedRoutedChild is the common execution/observation receipt boundary.
func verifiedRoutedChild(children ChildRunner, d Delegation, parent kernel.Run, child kernel.Snapshot) bool {
	if !VerifiedChildRun(d, parent, child) {
		return false
	}
	if !d.Remote {
		return true
	}
	remote, ok := children.(RemoteShadowObserver)
	return ok && remote.MatchesRemoteDelegation(d) &&
		remote.ObservesFrozenRemoteShadow(remote.RemoteRuntime(), d, child)
}

// An existing remote shadow must be authenticated against the original
// delegation BEFORE LoadRun is allowed to perform a protocol observation.
func verifyRemoteBeforeObservation(ctx context.Context, children ChildRunner, d Delegation, parent kernel.Run) error {
	if !d.Remote {
		return nil
	}
	remote, ok := children.(RemoteShadowObserver)
	if !ok || remote.RemoteRuntime() == nil || !remote.MatchesRemoteDelegation(d) {
		return ErrChildRunUnverified
	}
	snapshot, err := remote.RemoteRuntime().Load(ctx, d.ChildRunID)
	if errors.Is(err, kernel.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if !verifiedRoutedChild(children, d, parent, snapshot) {
		return ErrChildRunUnverified
	}
	return nil
}

func boundDelegationModelOutput(parent kernel.Snapshot, delegation Delegation) (Delegation, error) {
	if parent.Run.Kind != agent.RunKind || delegation.Remote {
		return delegation, nil
	}
	view, err := agent.ViewState(parent)
	if err != nil {
		return Delegation{}, ErrInvalidDelegation
	}
	if view.MaxModelOutputTokens > 0 {
		if delegation.MaxModelOutputTokens == 0 {
			delegation.MaxModelOutputTokens = view.MaxModelOutputTokens
		} else if delegation.MaxModelOutputTokens > view.MaxModelOutputTokens {
			return Delegation{}, ErrInvalidDelegation
		}
	}
	return delegation, nil
}

func (coordinator *Coordinator) resolveChild(ctx context.Context, delegation Delegation) (ChildRunner, error) {
	children, err := coordinator.resolver.ResolveChild(ctx, cloneDelegation(delegation))
	if err != nil {
		return nil, errors.Join(ErrChildUnavailable, err)
	}
	if children == nil {
		return nil, ErrChildUnavailable
	}
	// Strict JSON output is a native Agent model contract. Non-native child
	// routes (including remote shadows) have no proof they can enforce it;
	// deny before LoadRun/StartRun can dispatch external work. This keeps
	// protocol-specific capabilities out of this Handoff boundary.
	remote, isRemote := children.(RemoteShadowObserver)
	if isRemote != delegation.Remote || (delegation.Remote &&
		(!RemoteGoalOnly(delegation) || remote.RemoteRuntime() == nil || !remote.MatchesRemoteDelegation(delegation))) {
		// A remote route must prove original protocol identity and local
		// receipt before ANY StartRun/LoadRun. An ordinary ChildRunner must
		// never impersonate either a remote shadow or a native Agent.
		return nil, ErrInvalidDelegation
	}
	if delegation.OutputContract != nil {
		switch child := children.(type) {
		case *agent.Runner:
			if child == nil {
				return nil, ErrInvalidDelegation
			}
		case deferredAgentChild:
			if child.original == nil {
				return nil, ErrInvalidDelegation
			}
		default:
			return nil, ErrInvalidDelegation
		}
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
	case kernel.RunStatusRunning, kernel.RunStatusWaitingInput, kernel.RunStatusPausedBudget:
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
	case kernel.RunStatusRunning, kernel.RunStatusWaitingInput, kernel.RunStatusPausedBudget:
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

// RemoteGoalOnly is the durable boundary of what an unextended external
// protocol can prove. A local AgentSpec is not a negotiated remote capability.
func RemoteGoalOnly(delegation Delegation) bool {
	return delegation.Remote && delegation.Model == "" &&
		len(delegation.ModelOptions) == 0 && delegation.Instructions == "" &&
		delegation.OutputContract == nil && delegation.MaxModelOutputTokens == 0 &&
		delegation.Limits == (agent.Limits{}) && delegation.SubtreeLimits == nil &&
		len(delegation.ToolKeys) == 0 && len(delegation.InitialToolKeys) == 0 &&
		len(delegation.HostedToolGrants) == 0
}

func validDelegation(delegation Delegation) bool {
	validStatus := delegation.Status == StatusQueued || delegation.Status == StatusRunning ||
		delegation.Status == StatusCompleted || delegation.Status == StatusFailed || delegation.Status == StatusCancelled
	if delegation.MaxModelOutputTokens < 0 || model.ValidateOutputContract(delegation.OutputContract) != nil || strings.TrimSpace(delegation.ID) == "" || strings.TrimSpace(delegation.MemberID) == "" ||
		strings.TrimSpace(delegation.ChildRunID) == "" || strings.TrimSpace(delegation.Goal) == "" || !validStatus {
		return false
	}
	for _, key := range delegation.InitialToolKeys {
		if key == "" || !slices.Contains(delegation.ToolKeys, key) {
			return false
		}
	}
	return true
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
	if delegation.SubtreeLimits != nil {
		copy := *delegation.SubtreeLimits
		delegation.SubtreeLimits = &copy
	}
	delegation.ModelOptions = append(json.RawMessage(nil), delegation.ModelOptions...)
	delegation.OutputContract = model.CloneOutputContract(delegation.OutputContract)
	delegation.ToolKeys = append([]string(nil), delegation.ToolKeys...)
	delegation.InitialToolKeys = append([]string(nil), delegation.InitialToolKeys...)
	delegation.HostedToolGrants = append([]agent.HostedToolGrant(nil), delegation.HostedToolGrants...)
	delegation.Result = append(json.RawMessage(nil), delegation.Result...)
	return delegation
}

func cloneJoin(join Join) Join {
	join.ResultIDs = append([]string(nil), join.ResultIDs...)
	return join
}
