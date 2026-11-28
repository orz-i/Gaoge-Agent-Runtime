// Package plugin defines the small, typed, statically composed extension points used by Agent Runtime features.
package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

var (
	ErrInvalidRegistration = errors.New("invalid runtime plugin registration")
	ErrDuplicateName       = errors.New("duplicate runtime plugin name")
)

// RunOperation identifies the public Feature operation wrapped by Run middleware.
type RunOperation string

const (
	RunStart           RunOperation = "start"
	RunResume          RunOperation = "resume"
	RunResolveApproval RunOperation = "resolve_approval"
)

// RunInvocation contains only common immutable Run facts. Middleware cannot rewrite it through next.
type RunInvocation struct {
	Operation   RunOperation
	Kind        kernel.RunKind
	RunID       string
	ParentRunID string
	Actor       kernel.ActorRef
	Thread      kernel.ThreadRef
	Goal        string
}

func approvalPolicyNames(values []ApprovalPolicy) []string {
	result := make([]string, len(values))
	for index, value := range values {
		if value == nil {
			return append(result[:index], "")
		}
		result[index] = value.Name()
	}
	return result
}

// ToolInvocation contains immutable Tool identity and current Run facts.
type ToolInvocation struct {
	Run        kernel.Run
	Definition tools.Definition
	Request    tools.ExecutionRequest
}

// Event is one non-authoritative live Runtime fact delivered to optional observers.
type Event struct {
	RunID    string          `json:"runID"`
	RunKind  kernel.RunKind  `json:"runKind,omitempty"`
	Type     string          `json:"type"`
	Delta    string          `json:"delta,omitempty"`
	Message  string          `json:"message,omitempty"`
	Data     json.RawMessage `json:"data,omitempty"`
	Revision uint64          `json:"revision,omitempty"`
	Status   string          `json:"status,omitempty"`
	Terminal bool            `json:"terminal,omitempty"`
}

// Observer receives best-effort live facts and must not own Runtime state transitions.
type Observer interface {
	Name() string
	Observe(context.Context, Event)
}

// ObserverSet freezes explicit observer order and isolates best-effort fan-out failures.
type ObserverSet struct{ observers []Observer }

// NewObserverSet validates and freezes observers in delivery order.
func NewObserverSet(observers ...Observer) (*ObserverSet, error) {
	if err := validateNamed(observerNames(observers)); err != nil {
		return nil, err
	}
	return &ObserverSet{observers: append([]Observer(nil), observers...)}, nil
}

// Observe delivers isolated Event copies in registration order. Observer panics are contained.
func (set *ObserverSet) Observe(ctx context.Context, event Event) {
	if set == nil {
		return
	}
	for _, observer := range set.observers {
		observeSafely(ctx, observer, cloneEvent(event))
	}
}

// ApprovalRequirement is one Tool approval policy outcome.
type ApprovalRequirement string

const (
	ApprovalNotRequired ApprovalRequirement = "not_required"
	ApprovalRequired    ApprovalRequirement = "required"
)

// ApprovalPolicy decides whether one Tool invocation requires an approval checkpoint.
type ApprovalPolicy interface {
	Name() string
	Approval(context.Context, ToolInvocation) (ApprovalRequirement, error)
}

// ApprovalPolicySet freezes explicit approval policy order and OR-composes requirements.
type ApprovalPolicySet struct{ policies []ApprovalPolicy }

// NewApprovalPolicySet validates and freezes approval policies in evaluation order.
func NewApprovalPolicySet(policies ...ApprovalPolicy) (*ApprovalPolicySet, error) {
	if err := validateNamed(approvalPolicyNames(policies)); err != nil {
		return nil, err
	}
	return &ApprovalPolicySet{policies: append([]ApprovalPolicy(nil), policies...)}, nil
}

// RequiresApproval evaluates every policy. Any error fails closed; any required result wins.
func (set *ApprovalPolicySet) RequiresApproval(ctx context.Context, invocation ToolInvocation) (bool, error) {
	if set == nil {
		return false, ErrInvalidRegistration
	}
	required := false
	for _, policy := range set.policies {
		result, err := policy.Approval(ctx, cloneToolInvocation(invocation))
		if err != nil {
			return false, err
		}
		switch result {
		case ApprovalNotRequired:
		case ApprovalRequired:
			required = true
		default:
			return false, ErrInvalidRegistration
		}
	}
	return required, nil
}

// ApprovalDecision is the explicit outcome of an approval interaction.
type ApprovalDecision string

const (
	ApprovalApprove ApprovalDecision = "approve"
	ApprovalReject  ApprovalDecision = "reject"
)

// ApprovalResponse is the minimum provider-neutral approval resolution contract.
type ApprovalResponse struct {
	Decision ApprovalDecision `json:"decision"`
	Comment  string           `json:"comment,omitempty"`
}

// ApprovalHandler owns approval checkpoint preparation/resolution, not policy.
type ApprovalHandler interface {
	PrepareToolApproval(tools.Call, tools.Definition) (*kernel.Checkpoint, error)
	ResolveToolApproval(*kernel.Checkpoint, ApprovalResponse) (*kernel.Checkpoint, error)
}

func validateNamed(names []string) error {
	seen := make(map[string]struct{}, len(names))
	for _, raw := range names {
		name := strings.TrimSpace(raw)
		if name == "" {
			return ErrInvalidRegistration
		}
		if _, duplicate := seen[name]; duplicate {
			return ErrDuplicateName
		}
		seen[name] = struct{}{}
	}
	return nil
}

func observerNames(values []Observer) []string {
	result := make([]string, len(values))
	for index, value := range values {
		if value == nil {
			return append(result[:index], "")
		}
		result[index] = value.Name()
	}
	return result
}

func observeSafely(ctx context.Context, observer Observer, event Event) {
	defer func() {
		_ = recover()
	}()
	observer.Observe(ctx, event)
}

func cloneEvent(event Event) Event {
	event.Data = append(json.RawMessage(nil), event.Data...)
	return event
}

func cloneToolInvocation(value ToolInvocation) ToolInvocation {
	value.Definition = tools.CloneDefinition(value.Definition)
	value.Request = tools.CloneExecutionRequest(value.Request)
	return value
}
