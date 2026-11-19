package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/internal/callbackscope"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/jsoncontract"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
)

const runInputBatchLimit = 16
const runInputTextLimit = 16 << 10
const runInputBatchBytes = 64 << 10

// ErrInputQueueFull describes Host queue capacity, not an exhausted Run budget.
var ErrInputQueueFull = errors.New("agent Host input queue is full")

// RunInput is authenticated Host user input, never a system instruction, Tool
// result, approval, model selection or permission grant. Sequence is assigned
// by the source; IDs and text must remain immutable within the original Run.
type RunInput struct {
	Sequence uint64
	ID       string
	Text     string
}

// RunInputSource is a trusted, read-only Host input log. ReadRunInputs must
// honor ctx, return contiguous records after the cursor, respect both the count
// and 64 KiB total-text bounds, and never dispatch external effects. The Host owns
// authentication, retention and durable storage. BindingID is immutable and
// nonsecret; it is an identity label, not a credential.
type RunInputSource interface {
	BindingID() string
	ReadRunInputs(context.Context, kernel.Run, uint64, int) ([]RunInput, error)
}

type runInputScopeKey struct{}
type runInputBoundary struct{}

func (*runInputBoundary) Name() string { return "agent.host_input_boundary" }
func (boundary *runInputBoundary) Run(ctx context.Context, invocation plugin.RunInvocation, next plugin.RunNext) (kernel.Snapshot, error) {
	// Keep Host input reads inside the original Run callback. This deliberately
	// participates as a Host boundary, not a callback-free native/deferred
	// authority shortcut.
	result, err := next(context.WithValue(ctx, runInputScopeKey{}, invocation.RunID))
	// This records only this concrete SDK frame's return after Next. Other
	// middleware tickets and outstanding physical receipts remain independent.
	callbackscope.Record(ctx, boundary, "input.boundary.returned", invocation.RunID)
	return result, err
}

func validInputBinding(value string) bool {
	return value != "" && len(value) <= 128 && value == strings.TrimSpace(value) &&
		jsoncontract.ValidateCanonicalUTF8(value) == nil && !strings.ContainsAny(value, "\r\n\x00")
}

func validRunInput(input RunInput) bool {
	return validInputBinding(input.ID) && len(input.Text) <= runInputTextLimit &&
		strings.TrimSpace(input.Text) != "" && jsoncontract.ValidateCanonicalUTF8(input.Text) == nil
}

func (runner *Runner) admitRunInputs(ctx context.Context, snapshot kernel.Snapshot, state runState, previous *model.Response) (kernel.Snapshot, runState, bool, error) {
	if state.InputBinding == "" {
		return snapshot, state, false, nil // old Runs never silently enable inputs
	}
	if snapshot.Run.ID == "" || ctx.Value(runInputScopeKey{}) != snapshot.Run.ID {
		return snapshot, state, false, ErrInvalidRequest
	}
	if runner.inputs == nil || runner.inputBinding != state.InputBinding || runner.inputs.BindingID() != state.InputBinding {
		return snapshot, state, false, ErrInvalidRequest
	}
	inputs, err := runner.inputs.ReadRunInputs(ctx, snapshot.Run, state.InputCursor, runInputBatchLimit)
	if err != nil {
		return snapshot, state, false, err
	}
	if len(inputs) == 0 {
		return snapshot, state, false, nil
	}
	if len(inputs) > runInputBatchLimit {
		return snapshot, state, false, ErrInvalidRequest
	}
	cursor, size := state.InputCursor, 0
	seen := make(map[string]bool, len(inputs))
	for _, input := range inputs {
		if cursor == ^uint64(0) || input.Sequence != cursor+1 || !validRunInput(input) || seen[input.ID] {
			return snapshot, state, false, ErrInvalidRequest
		}
		seen[input.ID] = true
		cursor = input.Sequence
		size += len(input.Text)
		if size > runInputBatchBytes {
			return snapshot, state, false, ErrInvalidRequest
		}
	}
	if previous != nil {
		state.Messages = append(state.Messages, model.Message{Role: model.RoleAssistant,
			Content: model.TextParts(previous.Content), ProviderReplay: model.CloneProviderReplay(previous.ProviderReplay)})
	}
	for _, input := range inputs {
		state.Messages = append(state.Messages, model.Message{Role: model.RoleUser, Content: model.TextParts(input.Text)})
	}
	state.InputCursor = cursor
	encoded, err := encodeState(state)
	if err != nil {
		return snapshot, state, false, err
	}
	next, err := runner.runtime.Apply(ctx, snapshot.Run.ID, snapshot.Run.Revision, kernel.Mutation{
		Status: kernel.RunStatusRunning, State: encoded, Checkpoint: snapshot.Checkpoint,
		Events: []kernel.EventDraft{{Type: "agent.input_applied", Message: "Host user input applied at a model boundary", Wakeup: true,
			WakeupAt: agentWakeupAt(runner.clock.Now())}},
	})
	return next, state, true, err
}

// MemoryRunInputs is a bounded process-local example input log. It does not
// survive process exit. Use a Host durable RunInputSource for durable queued
// delivery. Queued is not applied: late input may require a new Host Turn.
type MemoryRunInputs struct {
	mu      sync.Mutex
	binding string
	logs    map[string][]RunInput
	bytes   int
}

func NewMemoryRunInputs(binding string) (*MemoryRunInputs, error) {
	if !validInputBinding(binding) {
		return nil, ErrInvalidRequest
	}
	return &MemoryRunInputs{binding: binding, logs: make(map[string][]RunInput)}, nil
}

func (queue *MemoryRunInputs) BindingID() string { return queue.binding }

func runInputScope(run kernel.Run) (string, error) {
	if run.Kind != RunKind || run.ID == "" || run.Actor.TenantID == "" || run.Actor.ActorID == "" || run.Thread.Kind == "" || run.Thread.ID == "" {
		return "", ErrInvalidRequest
	}
	value := struct {
		ID     string
		Actor  kernel.ActorRef
		Thread kernel.ThreadRef
	}{run.ID, run.Actor, run.Thread}
	if jsoncontract.ValidateCanonicalUTF8(value) != nil {
		return "", ErrInvalidRequest
	}
	encoded, err := json.Marshal(value)
	return string(encoded), err
}

// Submit appends an idempotent queued input after the Host authenticates and
// loads the original Run. The receipt does not claim atomicity with Run
// completion. Inspect View.InputCursor to determine whether it was applied.
func (queue *MemoryRunInputs) Submit(ctx context.Context, run kernel.Run, id, text string) (RunInput, error) {
	if err := ctx.Err(); err != nil {
		return RunInput{}, err
	}
	input := RunInput{ID: id, Text: text}
	scope, err := runInputScope(run)
	if err != nil || !validRunInput(input) || run.Status != kernel.RunStatusRunning {
		return RunInput{}, ErrInvalidRequest
	}
	queue.mu.Lock()
	defer queue.mu.Unlock()
	for _, old := range queue.logs[scope] {
		if old.ID == id {
			if old.Text != text {
				return RunInput{}, kernel.ErrConflict
			}
			return old, nil
		}
	}
	if queue.bytes+len(text) > 1<<20 || len(queue.logs[scope]) >= 256 || len(queue.logs) >= 256 && len(queue.logs[scope]) == 0 {
		return RunInput{}, ErrInputQueueFull
	}
	input.Sequence = uint64(len(queue.logs[scope])) + 1
	queue.logs[scope] = append(queue.logs[scope], input)
	queue.bytes += len(text)
	return input, nil
}

func (queue *MemoryRunInputs) ReadRunInputs(ctx context.Context, run kernel.Run, after uint64, limit int) ([]RunInput, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	scope, err := runInputScope(run)
	if err != nil || limit < 1 || limit > runInputBatchLimit {
		return nil, ErrInvalidRequest
	}
	queue.mu.Lock()
	defer queue.mu.Unlock()
	log := queue.logs[scope]
	if after > uint64(len(log)) {
		return nil, ErrInvalidRequest // lost history is not an empty successful read
	}
	var result []RunInput
	bytes := 0
	for _, input := range log[after:] {
		if len(result) == limit || bytes+len(input.Text) > runInputBatchBytes {
			break
		}
		result = append(result, input)
		bytes += len(input.Text)
	}
	return result, nil
}
