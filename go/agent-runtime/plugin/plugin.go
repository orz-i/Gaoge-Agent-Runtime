// Package plugin defines the small, typed, statically composed extension points used by Agent Runtime features.
package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"

	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/model"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/tools"
)

var (
	ErrInvalidRegistration = errors.New("invalid runtime plugin registration")
	ErrDuplicateName       = errors.New("duplicate runtime plugin name")
	ErrNextAlreadyCalled   = errors.New("runtime plugin next already called")
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
	Operation RunOperation
	Kind      kernel.RunKind
	RunID     string
	Actor     kernel.ActorRef
	Thread    kernel.ThreadRef
	Goal      string
}

// RunNext executes the next Run middleware or the Feature operation exactly once.
type RunNext func(context.Context) (kernel.Snapshot, error)

// RunMiddleware wraps one public Feature Run operation.
type RunMiddleware interface {
	Name() string
	Run(context.Context, RunInvocation, RunNext) (kernel.Snapshot, error)
}

// ModelNext executes the next Model middleware or provider exactly once.
type ModelNext func(context.Context, model.Request, model.StreamSink) (model.Response, error)

// ModelMiddleware wraps one provider-neutral Model call.
type ModelMiddleware interface {
	Name() string
	Model(context.Context, model.Request, model.StreamSink, ModelNext) (model.Response, error)
}

// ToolInvocation contains immutable Tool identity and current Run facts.
type ToolInvocation struct {
	Run        kernel.Run
	Definition tools.Definition
	Request    tools.ExecutionRequest
}

// ToolNext executes the next Tool middleware or executor exactly once.
type ToolNext func(context.Context) (tools.ExecutionResult, error)

// ToolMiddleware wraps one stable Tool execution without allowing call identity replacement.
type ToolMiddleware interface {
	Name() string
	Tool(context.Context, ToolInvocation, ToolNext) (tools.ExecutionResult, error)
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

// RunChain freezes explicit Run middleware order.
type RunChain struct{ middleware []RunMiddleware }

// NewRunChain validates and freezes Run middleware in outermost-to-innermost order.
func NewRunChain(middleware ...RunMiddleware) (*RunChain, error) {
	if err := validateNamed(middlewareNames(middleware)); err != nil {
		return nil, err
	}
	return &RunChain{middleware: append([]RunMiddleware(nil), middleware...)}, nil
}

// Invoke executes one Run chain with single-next enforcement.
func (chain *RunChain) Invoke(ctx context.Context, invocation RunInvocation, terminal RunNext) (kernel.Snapshot, error) {
	if chain == nil || terminal == nil {
		return kernel.Snapshot{}, ErrInvalidRegistration
	}
	next := terminal
	for index := len(chain.middleware) - 1; index >= 0; index-- {
		current := chain.middleware[index]
		downstream := next
		next = func(callCtx context.Context) (kernel.Snapshot, error) {
			return current.Run(callCtx, invocation, onceRunNext(downstream))
		}
	}
	return next(ctx)
}

// ModelChain freezes explicit Model middleware order.
type ModelChain struct{ middleware []ModelMiddleware }

// NewModelChain validates and freezes Model middleware in outermost-to-innermost order.
func NewModelChain(middleware ...ModelMiddleware) (*ModelChain, error) {
	if err := validateNamed(modelMiddlewareNames(middleware)); err != nil {
		return nil, err
	}
	return &ModelChain{middleware: append([]ModelMiddleware(nil), middleware...)}, nil
}

// Invoke executes one Model chain with isolated request copies and single-next enforcement.
func (chain *ModelChain) Invoke(
	ctx context.Context,
	request model.Request,
	emit model.StreamSink,
	terminal ModelNext,
) (model.Response, error) {
	if chain == nil || terminal == nil {
		return model.Response{}, ErrInvalidRegistration
	}
	next := terminal
	for index := len(chain.middleware) - 1; index >= 0; index-- {
		current := chain.middleware[index]
		downstream := next
		next = func(callCtx context.Context, callRequest model.Request, callEmit model.StreamSink) (model.Response, error) {
			return current.Model(callCtx, model.CloneRequest(callRequest), callEmit, onceModelNext(downstream))
		}
	}
	return next(ctx, model.CloneRequest(request), emit)
}

// ToolChain freezes explicit Tool middleware order.
type ToolChain struct{ middleware []ToolMiddleware }

// NewToolChain validates and freezes Tool middleware in outermost-to-innermost order.
func NewToolChain(middleware ...ToolMiddleware) (*ToolChain, error) {
	if err := validateNamed(toolMiddlewareNames(middleware)); err != nil {
		return nil, err
	}
	return &ToolChain{middleware: append([]ToolMiddleware(nil), middleware...)}, nil
}

// Invoke executes one Tool chain with an isolated invocation and single-next enforcement.
func (chain *ToolChain) Invoke(
	ctx context.Context,
	invocation ToolInvocation,
	terminal ToolNext,
) (tools.ExecutionResult, error) {
	if chain == nil || terminal == nil {
		return tools.ExecutionResult{}, ErrInvalidRegistration
	}
	next := terminal
	for index := len(chain.middleware) - 1; index >= 0; index-- {
		current := chain.middleware[index]
		downstream := next
		next = func(callCtx context.Context) (tools.ExecutionResult, error) {
			return current.Tool(callCtx, cloneToolInvocation(invocation), onceToolNext(downstream))
		}
	}
	return next(ctx)
}

func onceRunNext(next RunNext) RunNext {
	var called atomic.Bool
	return func(ctx context.Context) (kernel.Snapshot, error) {
		if !called.CompareAndSwap(false, true) {
			return kernel.Snapshot{}, ErrNextAlreadyCalled
		}
		return next(ctx)
	}
}

func onceModelNext(next ModelNext) ModelNext {
	var called atomic.Bool
	return func(ctx context.Context, request model.Request, emit model.StreamSink) (model.Response, error) {
		if !called.CompareAndSwap(false, true) {
			return model.Response{}, ErrNextAlreadyCalled
		}
		return next(ctx, model.CloneRequest(request), emit)
	}
}

func onceToolNext(next ToolNext) ToolNext {
	var called atomic.Bool
	return func(ctx context.Context) (tools.ExecutionResult, error) {
		if !called.CompareAndSwap(false, true) {
			return tools.ExecutionResult{}, ErrNextAlreadyCalled
		}
		return next(ctx)
	}
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

func middlewareNames(values []RunMiddleware) []string {
	result := make([]string, len(values))
	for index, value := range values {
		if value == nil {
			return append(result[:index], "")
		}
		result[index] = value.Name()
	}
	return result
}

func modelMiddlewareNames(values []ModelMiddleware) []string {
	result := make([]string, len(values))
	for index, value := range values {
		if value == nil {
			return append(result[:index], "")
		}
		result[index] = value.Name()
	}
	return result
}

func toolMiddlewareNames(values []ToolMiddleware) []string {
	result := make([]string, len(values))
	for index, value := range values {
		if value == nil {
			return append(result[:index], "")
		}
		result[index] = value.Name()
	}
	return result
}

func cloneToolInvocation(value ToolInvocation) ToolInvocation {
	value.Definition = tools.CloneDefinition(value.Definition)
	value.Request = tools.CloneExecutionRequest(value.Request)
	return value
}
